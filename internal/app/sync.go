package app

import (
	"context"
	"fmt"
	"html"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Theyashsawarkar/sandesh/internal/google"
	"github.com/Theyashsawarkar/sandesh/internal/mime"
	"github.com/Theyashsawarkar/sandesh/internal/store"
)

// News is what a sync found that deserves a notification.
type News struct {
	Account string
	Title   string
	Body    string
	Key     string // thread id or space name, to open it
	Chat    bool
}

// SyncMail brings an account's mail up to date: the change feed when
// there is a history id, else (first run, or the id expired) a fresh
// sync of the last InitialDays.
func (a *App) SyncMail(ctx context.Context, account string) ([]News, error) {
	acc, err := a.Store.Account(account)
	if err != nil {
		return nil, err
	}
	c, err := a.Client(account)
	if err != nil {
		return nil, err
	}
	news, err := a.syncMail(ctx, c, acc)
	if err != nil {
		_ = a.Store.SetError(account, err.Error())
	}
	return news, err
}

func (a *App) syncMail(ctx context.Context, c *google.Client, acc store.Account) ([]News, error) {
	var touched []string
	var historyID string
	fresh := acc.HistoryID == ""
	if !fresh {
		ids, hid, err := c.History(ctx, acc.HistoryID)
		if google.IsNotFound(err) {
			fresh = true
		} else if err != nil {
			return nil, err
		} else {
			touched, historyID = ids, hid
		}
	}
	if fresh {
		email, hid, err := c.Profile(ctx)
		if err != nil {
			return nil, err
		}
		if acc.Email == "" && email != "" {
			acc.Email = email
			_ = a.Store.AddAccount(acc)
		}
		ids, err := c.ListThreads(ctx, fmt.Sprintf("newer_than:%dd", a.Config.InitialDays), nil, a.Config.MaxThreads)
		if err != nil {
			return nil, err
		}
		touched, historyID = ids, hid
		if labels, err := c.Labels(ctx); err == nil {
			var ls []store.Label
			for _, l := range labels {
				ls = append(ls, store.Label{ID: l.ID, Name: l.Name, Type: strings.ToLower(l.Type)})
			}
			_ = a.Store.PutLabels(acc.Name, ls)
		}
	}
	// sections with a server-side query
	sectionIDs := map[string][]string{}
	for _, s := range a.allSections() {
		if s.Query == "" {
			continue
		}
		ids, err := c.ListThreads(ctx, s.Query, nil, 100)
		if err != nil {
			return nil, fmt.Errorf("section %s: %w", s.Name, err)
		}
		sectionIDs[s.Name] = ids
		for _, id := range ids {
			if !a.Store.HasThread(acc.Name, id) {
				touched = append(touched, id)
			}
		}
	}
	news, err := a.fetchThreads(ctx, c, acc, dedupe(touched), !fresh)
	if err != nil {
		return nil, err
	}
	for name, ids := range sectionIDs {
		if err := a.Store.SetSectionThreads(acc.Name, name, ids); err != nil {
			return nil, err
		}
	}
	return news, a.Store.SetHistory(acc.Name, historyID)
}

func dedupe(ids []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

func (a *App) fetchThreads(ctx context.Context, c *google.Client, acc store.Account, ids []string, notify bool) ([]News, error) {
	type res struct {
		id  string
		t   google.GmailThread
		err error
	}
	jobs := make(chan string)
	out := make(chan res)
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for id := range jobs {
				t, err := c.GetThread(ctx, id)
				out <- res{id, t, err}
			}
		}()
	}
	go func() {
		defer close(jobs)
		for _, id := range ids {
			select {
			case jobs <- id:
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() { wg.Wait(); close(out) }()

	since := acc.MailSynced - 60_000
	var news []News
	var firstErr error
	done := 0
	for r := range out {
		done++
		if a.Progress != nil {
			a.Progress(done, len(ids))
		}
		if r.err != nil {
			if google.IsNotFound(r.err) {
				_ = a.Store.DeleteThread(acc.Name, r.id)
				continue
			}
			if firstErr == nil {
				firstErr = r.err
			}
			continue
		}
		t, msgs := convertThread(acc, r.t)
		if err := a.Store.PutThread(acc.Name, t, msgs); err != nil && firstErr == nil {
			firstErr = err
		}
		for _, m := range msgs {
			sent := has(m.Labels, "SENT")
			if sent {
				for _, addr := range append(mime.ParseList(m.To), mime.ParseList(m.Cc)...) {
					_ = a.Store.NoteContact(acc.Name, addr, "", true, m.Date)
				}
			} else {
				_ = a.Store.NoteContact(acc.Name, m.FromAddr, m.FromName, false, m.Date)
			}
			if notify && acc.MailSynced > 0 && !sent && m.Date > since && has(m.Labels, "UNREAD") && has(m.Labels, "INBOX") &&
				a.Store.FirstNotify(acc.Name, "m:"+m.ID) {
				news = append(news, News{Account: acc.Name, Title: displayName(m.FromName, m.FromAddr), Body: m.Subject, Key: t.ID})
			}
		}
	}
	if firstErr != nil {
		return news, firstErr
	}
	return news, ctx.Err()
}

func has(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func displayName(name, addr string) string {
	if name != "" {
		return name
	}
	return addr
}

func convertThread(acc store.Account, gt google.GmailThread) (store.Thread, []store.Message) {
	t := store.Thread{Account: acc.Name, ID: gt.ID}
	labels := map[string]bool{}
	allTrash := true
	var names []string
	seenName := map[string]bool{}
	var msgs []store.Message
	for _, gm := range gt.Messages {
		body, htmlBody, atts := mime.Bodies(gm.Payload)
		fromName, fromAddr := mime.ParseAddress(mime.DecodeHeader(gm.Payload.Header("From")))
		date, _ := strconv.ParseInt(gm.InternalDate, 10, 64)
		m := store.Message{
			Account: acc.Name, ID: gm.ID, ThreadID: gt.ID, FromAddr: fromAddr, FromName: fromName,
			To: mime.DecodeHeader(gm.Payload.Header("To")), Cc: mime.DecodeHeader(gm.Payload.Header("Cc")),
			ReplyTo: gm.Payload.Header("Reply-To"), Subject: mime.DecodeHeader(gm.Payload.Header("Subject")),
			Date: date, Labels: gm.LabelIDs, Body: body, HTML: htmlBody, MessageID: gm.Payload.Header("Message-ID"),
			References: gm.Payload.Header("References"),
		}
		for _, at := range atts {
			m.Attachments = append(m.Attachments, store.Attachment{Name: at.Name, Type: at.Type, Size: at.Size})
		}
		msgs = append(msgs, m)
		trashed := has(gm.LabelIDs, "TRASH") || has(gm.LabelIDs, "SPAM")
		if !trashed {
			allTrash = false
			for _, l := range gm.LabelIDs {
				labels[l] = true
			}
		}
		who := "me"
		if !has(gm.LabelIDs, "SENT") && !strings.EqualFold(fromAddr, acc.Email) {
			who = firstName(displayName(fromName, fromAddr))
		}
		if !seenName[who] {
			seenName[who] = true
			names = append(names, who)
		}
		if date >= t.LastDate {
			t.LastDate = date
			t.LastFrom = displayName(fromName, fromAddr)
			if who == "me" {
				t.LastFrom = "me"
			}
			t.Snippet = mime.Clean(html.UnescapeString(gm.Snippet))
		}
		if t.Subject == "" {
			t.Subject = m.Subject
		}
	}
	if allTrash {
		for _, gm := range gt.Messages {
			for _, l := range gm.LabelIDs {
				labels[l] = true
			}
		}
	}
	for l := range labels {
		t.Labels = append(t.Labels, l)
	}
	sort.Strings(t.Labels)
	t.Participants = strings.Join(names, ", ")
	if t.Subject == "" {
		t.Subject = "(no subject)"
	}
	return t, msgs
}

func firstName(s string) string {
	if i := strings.IndexAny(s, " @"); i > 0 {
		return s[:i]
	}
	return s
}

// SyncChat brings an account's Google Chat spaces up to date.
func (a *App) SyncChat(ctx context.Context, account string) ([]News, error) {
	acc, err := a.Store.Account(account)
	if err != nil {
		return nil, err
	}
	if !acc.Chat {
		return nil, nil
	}
	c, err := a.Client(account)
	if err != nil {
		return nil, err
	}
	news, err := a.syncChat(ctx, c, acc)
	if err != nil {
		_ = a.Store.SetError(account, "chat: "+err.Error())
	}
	return news, err
}

func (a *App) syncChat(ctx context.Context, c *google.Client, acc store.Account) ([]News, error) {
	me := acc.ChatUser
	myName := ""
	if me == "" {
		// Chat's user ids are People ids. Without the People API, chat
		// still works; only your own messages and others' names can't be
		// told, so names fall back to "user 1234".
		if res, name, email, err := c.Person(ctx, "me"); err == nil {
			me = "users/" + strings.TrimPrefix(res, "people/")
			myName = name
			_ = a.Store.PutUser(acc.Name, me, name, email)
			_ = a.Store.SetChatSynced(acc.Name, me)
		}
	} else {
		myName, _, _ = a.Store.User(acc.Name, me)
	}
	spaces, err := c.Spaces(ctx)
	if err != nil {
		return nil, err
	}
	firstRun := acc.ChatSynced == 0
	cutoff := time.Now().AddDate(0, 0, -a.Config.InitialDays)
	var news []News
	for _, gs := range spaces {
		if gs.SingleUserBotDM {
			continue
		}
		active := google.ParseTime(gs.LastActiveTime)
		cached, cerr := a.Store.Space(acc.Name, gs.Name)
		known := cerr == nil
		sp := store.Space{Account: acc.Name, Name: gs.Name, DisplayName: mime.Clean(gs.DisplayName), Type: gs.SpaceType, LastActive: active.UnixMilli()}
		if sp.DisplayName == "" && (!known || cached.DisplayName == "") {
			sp.DisplayName = a.dmName(ctx, c, acc.Name, gs.Name, me)
		}
		if err := a.Store.PutSpace(sp); err != nil {
			return nil, err
		}
		if known && cached.SyncedTo >= active.UnixMilli() {
			continue // nothing new
		}
		if !known && active.Before(cutoff) {
			continue // old and never opened: skip until it moves
		}
		var after time.Time
		if known && cached.SyncedTo > 0 {
			after = time.UnixMilli(cached.SyncedTo)
		}
		msgs, err := c.Messages(ctx, gs.Name, after, 50)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", gs.Name, err)
		}
		var out []store.ChatMessage
		var newest int64
		for _, m := range msgs {
			created := google.ParseTime(m.CreateTime).UnixMilli()
			name := m.Sender.DisplayName
			if name == "" {
				name = a.userName(ctx, c, acc.Name, m.Sender.Name)
			}
			cm := store.ChatMessage{Account: acc.Name, Name: m.Name, Space: gs.Name, Thread: m.Thread.Name, Sender: m.Sender.Name,
				SenderName: mime.Clean(name), Text: mime.Clean(m.Text), Created: created}
			out = append(out, cm)
			if created > newest {
				newest = created
			}
			mention := myName != "" && strings.Contains(strings.ToLower(m.Text), "@"+strings.ToLower(firstName(myName)))
			if !firstRun && (me == "" || m.Sender.Name != me) && (gs.SpaceType == "DIRECT_MESSAGE" || mention) && a.Store.FirstNotify(acc.Name, "c:"+m.Name) {
				title := name
				if gs.SpaceType != "DIRECT_MESSAGE" {
					title = name + " in " + sp.DisplayName
				}
				news = append(news, News{Account: acc.Name, Title: title, Body: m.Text, Key: gs.Name, Chat: true})
			}
		}
		if err := a.Store.PutChatMessages(out); err != nil {
			return nil, err
		}
		if newest == 0 {
			newest = active.UnixMilli()
		}
		_ = a.Store.SetSpaceSynced(acc.Name, gs.Name, newest)
		if rt, err := c.ReadTime(ctx, gs.Name); err == nil && !rt.IsZero() {
			_ = a.Store.ForceSpaceRead(acc.Name, gs.Name, rt.UnixMilli())
		} else if !known {
			_ = a.Store.ForceSpaceRead(acc.Name, gs.Name, newest)
		}
	}
	return news, a.Store.SetChatSynced(acc.Name, me)
}

func (a *App) userName(ctx context.Context, c *google.Client, account, user string) string {
	if user == "" {
		return ""
	}
	if name, _, ok := a.Store.User(account, user); ok {
		return name
	}
	_, name, email, err := c.Person(ctx, user)
	if err != nil || name == "" {
		name = email
	}
	if name == "" {
		name = "user " + tail(strings.TrimPrefix(user, "users/"), 4)
	}
	name = mime.Clean(name)
	_ = a.Store.PutUser(account, user, name, mime.Clean(email)) // cache misses too, so we ask once
	return name
}

func tail(s string, n int) string {
	if len(s) > n {
		return s[len(s)-n:]
	}
	return s
}

func (a *App) dmName(ctx context.Context, c *google.Client, account, space, me string) string {
	members, err := c.Members(ctx, space)
	if err != nil {
		return ""
	}
	var names []string
	for _, m := range members {
		if m == me {
			continue
		}
		if n := a.userName(ctx, c, account, m); n != "" {
			names = append(names, n)
		}
	}
	return strings.Join(names, ", ")
}

// SyncAll syncs every account's mail and chat; errors are per account.
func (a *App) SyncAll(ctx context.Context) ([]News, map[string]error) {
	errs := map[string]error{}
	var news []News
	for _, name := range a.AccountNames() {
		n, err := a.SyncMail(ctx, name)
		news = append(news, n...)
		if err != nil {
			errs[name] = err
		}
		n, err = a.SyncChat(ctx, name)
		news = append(news, n...)
		if err != nil && errs[name] == nil {
			errs[name] = err
		}
	}
	return news, errs
}
