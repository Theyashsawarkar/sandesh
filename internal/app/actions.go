package app

import (
	"github.com/Theyashsawarkar/sandesh/internal/google"

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

// SearchServer runs a Gmail search on the server, caches the threads it
// finds and returns them (newest first).
func (a *App) SearchServer(ctx context.Context, account, query string, max int) ([]store.Thread, error) {
	acc, err := a.Store.Account(account)
	if err != nil {
		return nil, err
	}
	c, err := a.Client(account)
	if err != nil {
		return nil, err
	}
	ids, err := c.ListThreads(ctx, query, nil, max)
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
