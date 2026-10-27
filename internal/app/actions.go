package app

import (
	"fmt"
	"github.com/Theyashsawarkar/sandesh/internal/google"
	"github.com/Theyashsawarkar/sandesh/internal/mime"
	"strings"

	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/Theyashsawarkar/sandesh/internal/config"
	"github.com/Theyashsawarkar/sandesh/internal/store"
)

// Mail actions change the local copy first (the screen reacts at once),
// then Google. If Google fails, the next sync puts the truth back.

func (a *App) Modify(ctx context.Context, account, thread string, add, remove []string) error {
	if err := a.Store.SetThreadLabels(account, thread, add, remove); err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	c, err := a.Client(account)
	if err != nil {
		return err
	}
	return c.ModifyThread(ctx, thread, add, remove)
}

func (a *App) Archive(ctx context.Context, account, thread string) error {
	return a.Modify(ctx, account, thread, nil, []string{"INBOX"})
}

func (a *App) Unarchive(ctx context.Context, account, thread string) error {
	return a.Modify(ctx, account, thread, []string{"INBOX"}, nil)
}

func (a *App) MarkRead(ctx context.Context, account, thread string, read bool) error {
	if read {
		return a.Modify(ctx, account, thread, nil, []string{"UNREAD"})
	}
	return a.Modify(ctx, account, thread, []string{"UNREAD"}, nil)
}

func (a *App) Star(ctx context.Context, account, thread string, on bool) error {
	if on {
		return a.Modify(ctx, account, thread, []string{"STARRED"}, nil)
	}
	return a.Modify(ctx, account, thread, nil, []string{"STARRED"})
}

func (a *App) Trash(ctx context.Context, account, thread string) error {
	_ = a.Store.SetThreadLabels(account, thread, []string{"TRASH"}, []string{"INBOX"})
	c, err := a.Client(account)
	if err != nil {
		return err
	}
	return c.TrashThread(ctx, thread)
}

func (a *App) Untrash(ctx context.Context, account, thread string) error {
	_ = a.Store.SetThreadLabels(account, thread, []string{"INBOX"}, []string{"TRASH"})
	c, err := a.Client(account)
	if err != nil {
		return err
	}
	return c.UntrashThread(ctx, thread)
}

// MarkSpaceRead marks a chat space read up to now, here and in Chat.
func (a *App) MarkSpaceRead(ctx context.Context, account, space string) error {
	t := time.Now()
	if err := a.Store.SetSpaceRead(account, space, t.UnixMilli()); err != nil {
		return err
	}
	c, err := a.Client(account)
	if err != nil {
		return err
	}
	err = c.SetReadTime(ctx, space, t)
	if google.IsChatAppMissing(err) {
		a.ChatAppMissing.Store(true)
		return nil // read here; Chat keeps its own state until it's set up
	}
	return err
}

// MarkSpaceUnread marks the latest message from someone else unread, here
// and in Chat (moving the read time back is Chat's own "mark as unread").
func (a *App) MarkSpaceUnread(ctx context.Context, account, space string) error {
	last := a.Store.LastOtherMessage(account, space)
	if last == 0 {
		return errors.New("no one else has written here yet")
	}
	t := time.UnixMilli(last - 1)
	if err := a.Store.ForceSpaceRead(account, space, t.UnixMilli()); err != nil {
		return err
	}
	c, err := a.Client(account)
	if err != nil {
		return err
	}
	err = c.SetReadTime(ctx, space, t)
	if google.IsChatAppMissing(err) {
		a.ChatAppMissing.Store(true)
		return nil
	}
	return err
}

// Signature is the account's Gmail signature as text: from a cache file
// (fresh for a day), else Gmail's settings. "" when there is none.
func (a *App) Signature(ctx context.Context, account string, refresh bool) string {
	path := filepath.Join(a.Paths.State, "signature-"+account+".txt")
	if st, err := os.Stat(path); err == nil && (!refresh || time.Since(st.ModTime()) < 24*time.Hour) {
		b, _ := os.ReadFile(path)
		return string(b)
	}
	if !refresh {
		return ""
	}
	c, err := a.Client(account)
	if err != nil {
		return ""
	}
	html, err := c.Signature(ctx)
	if err != nil {
		return ""
	}
	sig := strings.TrimSpace(mime.Clean(mime.HTMLToText(html)))
	_ = os.WriteFile(path, []byte(sig), 0o600)
	return sig
}

// RenameSpace renames a chat. A named space is renamed on Google Chat
// (everyone sees it); a DM or an unnamed group can't be, so the name is
// kept on this machine. "" removes your own name.
func (a *App) RenameSpace(ctx context.Context, account, space, name string) (onGoogle bool, err error) {
	sp, err := a.Store.Space(account, space)
	if err != nil {
		return false, err
	}
	name = strings.TrimSpace(mime.Clean(name))
	if sp.Type != "SPACE" || name == "" {
		return false, a.Store.SetAlias(account, space, name)
	}
	c, err := a.Client(account)
	if err != nil {
		return false, err
	}
	if err := c.RenameSpace(ctx, space, name); err != nil {
		switch {
		case google.IsMissingScope(err):
			return false, fmt.Errorf("renaming spaces needs one more Google permission: sign in again with `sandesh auth add %s`", account)
		case strings.Contains(err.Error(), "permission") || strings.Contains(err.Error(), "403"):
			return false, fmt.Errorf("Google Chat says you can't rename this space (only its managers can)")
		}
		return false, err
	}
	_ = a.Store.SetAlias(account, space, "")
	return true, a.Store.RenameSpace(account, space, name)
}

// OpenConversation opens your chat with these people: the DM you already
// have with one person (found on Google even when it's not in the local
// copy), else a new DM, or a new group chat for several. people are
// users/{id} or email addresses; names (optional) title it until a sync
// names it. Its recent messages are fetched, so the earlier conversation
// is there at once.
func (a *App) OpenConversation(ctx context.Context, account string, people, names []string) (store.Space, error) {
	if len(people) == 0 {
		return store.Space{}, errors.New("who with?")
	}
	c, err := a.Client(account)
	if err != nil {
		return store.Space{}, err
	}
	var users []string
	for _, p := range people {
		if !strings.HasPrefix(p, "users/") {
			p = "users/" + strings.ToLower(strings.TrimSpace(p))
		}
		users = append(users, p)
	}
	var gs google.ChatSpace
	if len(users) == 1 {
		gs, err = c.FindDM(ctx, users[0])
		if google.IsNotFound(err) {
			gs, err = c.StartConversation(ctx, users)
		}
	} else {
		gs, err = c.StartConversation(ctx, users)
	}
	switch {
	case google.IsChatAppMissing(err):
		return store.Space{}, errors.New("Google Chat isn't set up in your Cloud project (Chat API → Configuration)")
	case google.IsMissingScope(err):
		return store.Space{}, fmt.Errorf("starting chats needs one more Google permission: sign in again with `sandesh auth add %s`", account)
	case err != nil:
		return store.Space{}, err
	}
	sp := store.Space{Account: account, Name: gs.Name, DisplayName: mime.Clean(gs.DisplayName), Type: gs.SpaceType, LastActive: time.Now().UnixMilli()}
	if prev, err := a.Store.Space(account, gs.Name); err == nil {
		sp.LastActive = prev.LastActive
	}
	if err := a.Store.PutSpace(sp); err != nil {
		return sp, err
	}
	if sp.DisplayName == "" && len(names) > 0 {
		if prev, _ := a.Store.Space(account, gs.Name); prev.DisplayName == "" {
			_ = a.Store.RenameSpace(account, gs.Name, strings.Join(names, ", "))
		}
	}
	// the conversation so far
	if msgs, err := c.Messages(ctx, gs.Name, time.Time{}, 50); err == nil {
		var out []store.ChatMessage
		var newest int64
		for _, m := range msgs {
			cm := a.chatMessage(ctx, c, account, gs.Name, m)
			out = append(out, cm)
			newest = max(newest, cm.Created)
		}
		_ = a.Store.PutChatMessages(out)
		if newest > 0 {
			_ = a.Store.SetSpaceSynced(account, gs.Name, newest)
			if prev, err := a.Store.Space(account, gs.Name); err == nil && prev.LastRead < newest {
				_ = a.Store.ForceSpaceRead(account, gs.Name, newest) // opening it reads it
			}
		}
	}
	return a.Store.Space(account, gs.Name)
}

// Retry sends a failed message again (straight away).
func (a *App) Retry(id int64) error {
	ok, err := a.Store.Transition(id, store.StatusFailed, store.StatusQueued, time.Now().UnixMilli())
	if err == nil && !ok {
		err = fmt.Errorf("#%d hasn't failed", id)
	}
	return err
}

// SearchDirectory looks colleagues up in the Workspace directory and keeps
// them as contacts. Without the People API it fails once and then stays
// quiet for the rest of the run (ok=false).
func (a *App) SearchDirectory(ctx context.Context, account, query string) (found []store.Contact, ok bool) {
	if a.directoryOff.Load() {
		return nil, false
	}
	c, err := a.Client(account)
	if err != nil {
		return nil, false
	}
	people, err := c.SearchDirectory(ctx, query)
	if err != nil {
		a.directoryOff.Store(true)
		return nil, false
	}
	for _, p := range people {
		name, addr := mime.Clean(p.Name), mime.Clean(p.Email)
		_ = a.Store.PutContact(account, addr, name)
		found = append(found, store.Contact{Name: name, Addr: addr})
	}
	return found, true
}

// ---------------------------------------------------------------- sections

// Sections are the configured ones plus searches saved from the screen
// (state/sections.json).
func (a *App) allSections() []config.Section {
	return append(append([]config.Section{}, a.Config.Sections...), a.SavedSearches()...)
}

func (a *App) Sections() []config.Section { return a.allSections() }

func (a *App) savedFile() string { return filepath.Join(a.Paths.State, "sections.json") }

func (a *App) SavedSearches() []config.Section {
	b, err := os.ReadFile(a.savedFile())
	if err != nil {
		return nil
	}
	var s []config.Section
	_ = json.Unmarshal(b, &s)
	return s
}

func (a *App) SaveSearch(name, query string) error {
	s := a.SavedSearches()
	for _, x := range append(a.Config.Sections, s...) {
		if x.Name == name {
			return errors.New("a section called " + name + " already exists")
		}
	}
	s = append(s, config.Section{Name: name, Query: query})
	b, _ := json.MarshalIndent(s, "", "  ")
	return os.WriteFile(a.savedFile(), b, 0o600)
}

// RemoveSearch removes a saved search; configured sections stay.
func (a *App) RemoveSearch(name string) bool {
	s := a.SavedSearches()
	for i, x := range s {
		if x.Name == name {
			s = append(s[:i], s[i+1:]...)
			b, _ := json.MarshalIndent(s, "", "  ")
			_ = os.WriteFile(a.savedFile(), b, 0o600)
			return true
		}
	}
	return false
}

// SectionThreads lists a section's threads from the local copy.
func (a *App) SectionThreads(s config.Section, accounts []string) ([]store.Thread, error) {
	q := store.ThreadQuery{Accounts: accounts, Label: s.Label, Limit: 1000}
	if s.Query != "" {
		q.Section = s.Name
	}
	if s.Today {
		y, m, d := time.Now().Date()
		q.Since = time.Date(y, m, d, 0, 0, 0, 0, time.Local).UnixMilli()
	}
	ts, err := a.Store.Threads(q)
	if err != nil {
		return nil, err
	}
	// trash and spam only show in sections that ask for them
	if s.Label == "TRASH" || s.Label == "SPAM" {
		return ts, nil
	}
	out := ts[:0]
	for _, t := range ts {
		if !t.Has("TRASH") && !t.Has("SPAM") {
			out = append(out, t)
		}
	}
	return out, nil
}

// LoadOlder fetches up to n threads of a section (label sections
// and searches) older than the oldest one here: Gmail's own search with
// before:, so nothing has to be downloaded ahead of time. Returns how many
// it added; 0 means the section has nothing older.
func (a *App) LoadOlder(ctx context.Context, account string, sec config.Section, oldest int64, n int) (int, error) {
	n = max(1, min(n, 500))
	if sec.Today {
		return 0, nil
	}
	acc, err := a.Store.Account(account)
	if err != nil {
		return 0, err
	}
	c, err := a.Client(account)
	if err != nil {
		return 0, err
	}
	q := strings.TrimSpace(sec.Query)
	if oldest > 0 {
		// before: is a date: take the day after, the ones we have are skipped
		q = strings.TrimSpace(q + " before:" + time.UnixMilli(oldest).AddDate(0, 0, 1).Format("2006/01/02"))
	}
	var labels []string
	if sec.Label != "" {
		labels = []string{sec.Label}
	}
	var missing []string
	page := ""
	for pages := 0; len(missing) < n && pages < 10; pages++ {
		ids, next, err := c.ThreadsPage(ctx, q, page, min(500, max(n, 50)), labels...)
		if err != nil {
			return 0, err
		}
		for _, id := range ids {
			if len(missing) < n && !a.Store.HasThread(account, id) {
				missing = append(missing, id)
			}
		}
		if next == "" {
			break
		}
		page = next
	}
	if len(missing) == 0 {
		return 0, nil
	}
	if _, err := a.fetchThreads(ctx, c, acc, missing, false); err != nil {
		return 0, err
	}
	if sec.Query != "" && sec.Label == "" {
		_ = a.Store.AddSectionThreads(account, sec.Name, missing)
	}
	return len(missing), nil
}

// SearchServer runs a Gmail search on the server, caches the threads it
// finds and returns them (newest first).
func (a *App) SearchServer(ctx context.Context, account, query string, max int, labels ...string) ([]store.Thread, error) {
	acc, err := a.Store.Account(account)
	if err != nil {
		return nil, err
	}
	c, err := a.Client(account)
	if err != nil {
		return nil, err
	}
	ids, err := c.ListThreads(ctx, query, labels, max)
	if err != nil {
		return nil, err
	}
	var missing []string
	for _, id := range ids {
		if !a.Store.HasThread(account, id) {
			missing = append(missing, id)
		}
	}
	if _, err := a.fetchThreads(ctx, c, acc, missing, false); err != nil {
		return nil, err
	}
	var out []store.Thread
	for _, id := range ids {
		if t, err := a.Store.Thread(account, id); err == nil {
			out = append(out, t)
		}
	}
	return out, nil
}
