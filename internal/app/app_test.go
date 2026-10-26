package app

import (
	"context"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Theyashsawarkar/sandesh/internal/config"
	"github.com/Theyashsawarkar/sandesh/internal/fake"
	"github.com/Theyashsawarkar/sandesh/internal/store"
)

func setup(t *testing.T) (*App, *fake.Server) {
	t.Helper()
	g := fake.New()
	g.Seed()
	srv := httptest.NewServer(g)
	t.Cleanup(srv.Close)
	t.Setenv("SANDESH_GOOGLE_URL", srv.URL)
	t.Setenv("SANDESH_HOME", t.TempDir())
	a, err := Open()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	a.Config.UndoSeconds = 0
	if err := a.Store.AddAccount(store.Account{Name: "work", Chat: true}); err != nil {
		t.Fatal(err)
	}
	return a, g
}

func TestSyncAndSections(t *testing.T) {
	a, g := setup(t)
	ctx := context.Background()
	if _, errs := a.SyncAll(ctx); len(errs) > 0 {
		t.Fatal(errs)
	}
	acc, _ := a.Store.Account("work")
	if acc.Email != "you@example.com" || acc.HistoryID == "" || acc.ChatUser != "users/1" {
		t.Fatalf("account after sync: %+v", acc)
	}
	inbox, err := a.SectionThreads(config.Section{Name: "Inbox", Label: "INBOX"}, nil)
	if err != nil || len(inbox) != 6 {
		t.Fatalf("inbox: %d %v", len(inbox), err)
	}
	if inbox[0].Subject != "Release cut on Thursday?" || !inbox[0].Unread() || !inbox[0].Starred() {
		t.Fatalf("newest thread: %+v", inbox[0])
	}
	msgs, _ := a.Store.Messages("work", "t-priya")
	if len(msgs) != 1 || !strings.Contains(msgs[0].Body, "QA signed off") || msgs[0].FromName != "Priya Kulkarni" {
		t.Fatalf("message: %+v", msgs)
	}
	rohan, _ := a.Store.Thread("work", "t-rohan")
	if rohan.Participants != "Rohan, me" || rohan.LastFrom != "me" {
		t.Fatalf("participants: %+v", rohan)
	}
	if !a.Store.KnownContact("work", "rohan.d@example.com") || a.Store.KnownContact("work", "priya.k@example.com") {
		t.Fatal("contacts: rohan was written to, priya never")
	}
	// local full-text search
	found, _ := a.Store.Threads(store.ThreadQuery{Text: "vault stag"})
	if len(found) != 1 || found[0].ID != "t-arjun" {
		t.Fatalf("fts: %+v", found)
	}

	// chat: DM named after the other person, unread from Chat's read state
	spaces, _ := a.Store.Spaces(nil, nil)
	if len(spaces) != 3 || spaces[0].DisplayName != "Priya Kulkarni" || spaces[0].Unread != 1 {
		t.Fatalf("spaces: %+v", spaces)
	}
	msgsC, _ := a.Store.ChatMessages("work", "spaces/portal-dev", 0)
	if len(msgsC) != 3 || msgsC[0].SenderName != "Arjun Mehta" {
		t.Fatalf("chat: %+v", msgsC)
	}

	// incremental: a new mail arrives and is noticed
	g.AddMail(fake.Message{Labels: []string{"INBOX", "UNREAD"}, From: "Neha Rao <neha.r@example.com>", To: "you@example.com", Subject: "Coffee?", Body: "3pm?"})
	time.Sleep(5 * time.Millisecond)
	news, err := a.SyncMail(ctx, "work")
	if err != nil || len(news) != 1 || news[0].Title != "Neha Rao" {
		t.Fatalf("news: %+v %v", news, err)
	}
	news, _ = a.SyncMail(ctx, "work")
	if len(news) != 0 {
		t.Fatalf("notified twice: %+v", news)
	}
}

func TestActions(t *testing.T) {
	a, g := setup(t)
	ctx := context.Background()
	a.SyncAll(ctx)
	if err := a.Archive(ctx, "work", "t-ci"); err != nil {
		t.Fatal(err)
	}
	th, _ := a.Store.Thread("work", "t-ci")
	if th.Has("INBOX") {
		t.Fatal("archive did not change the local copy")
	}
	if _, err := a.SyncMail(ctx, "work"); err != nil {
		t.Fatal(err)
	}
	th, _ = a.Store.Thread("work", "t-ci")
	if th.Has("INBOX") {
		t.Fatal("archive did not reach google")
	}
	_ = a.Unarchive(ctx, "work", "t-ci")
	_ = a.MarkRead(ctx, "work", "t-priya", true)
	_ = a.Trash(ctx, "work", "t-hr")
	a.SyncMail(ctx, "work")
	inbox, _ := a.SectionThreads(config.Section{Label: "INBOX"}, nil)
	ids := []string{}
	for _, t := range inbox {
		ids = append(ids, t.ID)
	}
	if strings.Contains(strings.Join(ids, ","), "t-hr") || !strings.Contains(strings.Join(ids, ","), "t-ci") {
		t.Fatalf("inbox after trash/unarchive: %v", ids)
	}
	p, _ := a.Store.Thread("work", "t-priya")
	if p.Unread() {
		t.Fatal("still unread")
	}
	_ = g
	if err := a.MarkSpaceRead(ctx, "work", "spaces/dm-priya"); err != nil {
		t.Fatal(err)
	}
	sp, _ := a.Store.Space("work", "spaces/dm-priya")
	if sp.Unread != 0 {
		t.Fatalf("space still unread: %+v", sp)
	}
}

func TestSendPathAndPolicy(t *testing.T) {
	a, g := setup(t)
	ctx := context.Background()
	a.SyncAll(ctx)

	// yours: straight to the undo window, then delivered
	it, err := a.Queue("work", "mail", store.Outgoing{To: []string{"priya.k@example.com"}, Subject: "Re: Release cut on Thursday?",
		ThreadID: "t-priya", Body: "Yes, Thursday works."}, OriginYou)
	if err != nil || it.Status != store.StatusQueued {
		t.Fatalf("queue: %+v %v", it, err)
	}
	done := a.DeliverDue(ctx)
	if len(done) != 1 || done[0].Status != store.StatusSent {
		t.Fatalf("deliver: %+v", done)
	}
	if len(g.Sent) != 1 || !strings.Contains(g.Sent[0], "To: priya.k@example.com") {
		t.Fatalf("sent: %q", g.Sent)
	}
	msgs, _ := a.Store.Messages("work", "t-priya")
	if len(msgs) != 2 || msgs[1].Body != "Yes, Thursday works." {
		t.Fatalf("sent message not pulled into the thread: %+v", msgs)
	}

	// undo window: cancel before delivery
	a.Config.UndoSeconds = 60
	it, _ = a.Queue("work", "mail", store.Outgoing{To: []string{"x@example.com"}, Subject: "s", Body: "b"}, OriginYou)
	if err := a.Cancel(it.ID); err != nil {
		t.Fatal(err)
	}
	if len(a.DeliverDue(ctx)) != 0 {
		t.Fatal("cancelled item delivered")
	}
	a.Config.UndoSeconds = 0

	// agent + confirm: waits for approval
	it, _ = a.Queue("work", "mail", store.Outgoing{To: []string{"rohan.d@example.com"}, Subject: "hi", Body: "b"}, OriginAgent)
	if it.Status != store.StatusApproval {
		t.Fatalf("confirm policy let an agent send: %+v", it)
	}
	if len(a.DeliverDue(ctx)) != 0 {
		t.Fatal("unapproved item delivered")
	}
	if err := a.Approve(it.ID, nil); err != nil {
		t.Fatal(err)
	}
	if done := a.DeliverDue(ctx); len(done) != 1 {
		t.Fatalf("approved item not delivered: %+v", done)
	}

	// agent + allow: known contact goes, stranger asks
	a.Config.SendPolicy = config.PolicyAllow
	it, _ = a.Queue("work", "mail", store.Outgoing{To: []string{"rohan.d@example.com"}, Subject: "hi", Body: "b"}, OriginAgent)
	if it.Status != store.StatusQueued {
		t.Fatalf("allow + known contact should send: %+v", it)
	}
	it, _ = a.Queue("work", "mail", store.Outgoing{To: []string{"attacker@evil.example"}, Subject: "hi", Body: "b"}, OriginAgent)
	if it.Status != store.StatusApproval || !strings.Contains(it.Reason, "never written") {
		t.Fatalf("stranger should ask: %+v", it)
	}
	// known-contacts: only replies
	a.Config.SendPolicy = config.PolicyKnownContacts
	it, _ = a.Queue("work", "mail", store.Outgoing{To: []string{"rohan.d@example.com"}, Subject: "new", Body: "b"}, OriginAgent)
	if it.Status != store.StatusApproval {
		t.Fatalf("known-contacts should ask for a new thread: %+v", it)
	}
	// chat from an agent always asks unless confirm... always asks
	it, _ = a.Queue("work", "chat", store.Outgoing{Space: "spaces/portal-dev", Body: "on it"}, OriginAgent)
	if it.Status != store.StatusApproval {
		t.Fatalf("agent chat should ask: %+v", it)
	}
	_ = a.Approve(it.ID, nil)
	a.DeliverDue(ctx)
	last := g.Chat[len(g.Chat)-1]
	if last.Text != "on it" || last.Sender != "users/1" {
		t.Fatalf("chat sent: %+v", last)
	}

	// validation
	if _, err := a.Queue("work", "mail", store.Outgoing{To: []string{"not-an-address"}, Body: "b"}, OriginYou); err == nil {
		t.Fatal("bad address accepted")
	}
}

func TestSearchServer(t *testing.T) {
	a, _ := setup(t)
	ctx := context.Background()
	a.SyncAll(ctx)
	ts, err := a.SearchServer(ctx, "work", "from:github", 20)
	if err != nil || len(ts) != 1 || ts[0].ID != "t-gh" {
		t.Fatalf("search: %+v %v", ts, err)
	}
	if err := a.SaveSearch("GitHub", "from:github"); err != nil {
		t.Fatal(err)
	}
	a.SyncMail(ctx, "work")
	var gh config.Section
	for _, s := range a.Sections() {
		if s.Name == "GitHub" {
			gh = s
		}
	}
	got, _ := a.SectionThreads(gh, nil)
	if len(got) != 1 {
		t.Fatalf("saved search section: %+v", got)
	}
	if !a.RemoveSearch("GitHub") || len(a.SavedSearches()) != 0 {
		t.Fatal("remove search")
	}
}

func TestAttachments(t *testing.T) {
	a, g := setup(t)
	ctx := context.Background()
	if _, errs := a.SyncAll(ctx); len(errs) > 0 {
		t.Fatal(errs)
	}
	dl := t.TempDir()

	// mail: listed at sync, downloaded on demand, never overwriting
	msgs, _ := a.Store.Messages("work", "t-hr")
	if len(msgs) != 1 || len(msgs[0].Attachments) != 1 || msgs[0].Attachments[0].Name != "holidays-2027.csv" {
		t.Fatalf("mail attachments: %+v", msgs)
	}
	p1, err := a.SaveMailAttachment(ctx, "work", msgs[0].ID, 0, dl)
	if err != nil {
		t.Fatal(err)
	}
	p2, _ := a.SaveMailAttachment(ctx, "work", msgs[0].ID, 0, dl)
	if filepath.Base(p1) != "holidays-2027.csv" || filepath.Base(p2) != "holidays-2027 (2).csv" {
		t.Fatalf("saved as %s and %s", p1, p2)
	}
	if b, _ := os.ReadFile(p1); !strings.Contains(string(b), "Republic Day") {
		t.Fatalf("content: %q", b)
	}

	// chat: the file's reference is kept and downloads
	cm, _ := a.Store.ChatMessages("work", "spaces/portal-dev", 0)
	var withFile store.ChatMessage
	for _, m := range cm {
		if len(m.Files) > 0 {
			withFile = m
		}
	}
	if withFile.Files == nil || withFile.Files[0].Ref == "" {
		t.Fatalf("chat files: %+v", cm)
	}
	p3, err := a.SaveChatAttachment(ctx, "work", withFile.Name, 0, dl)
	if b, _ := os.ReadFile(p3); err != nil || string(b) != "412 passed, 0 failed\n" {
		t.Fatalf("chat download: %v %q", err, b)
	}

	// sending: private copies, multipart mail, uploaded chat file
	src := filepath.Join(t.TempDir(), "notes.txt")
	_ = os.WriteFile(src, []byte("hello file"), 0o644)
	files, err := a.AttachFiles([]string{src})
	if err != nil || len(files) != 1 || files[0].Path == src || files[0].Type != "text/plain; charset=utf-8" {
		t.Fatalf("attach: %+v %v", files, err)
	}
	if _, err := a.Queue("work", "mail", store.Outgoing{To: []string{"rohan.d@example.com"}, Subject: "file", Body: "see attached", Files: files}, OriginYou); err != nil {
		t.Fatal(err)
	}
	if done := a.DeliverDue(ctx); len(done) != 1 || done[0].Status != store.StatusSent {
		t.Fatalf("deliver: %+v", done)
	}
	last := g.Messages[len(g.Messages)-1]
	if len(last.Files) != 1 || string(last.Files[0].Data) != "hello file" || last.Body != "see attached" {
		t.Fatalf("sent mail: %+v", last)
	}
	if _, err := os.Stat(files[0].Path); !os.IsNotExist(err) {
		t.Fatal("private copy kept after sending")
	}
	files, _ = a.AttachFiles([]string{src})
	if _, err := a.Queue("work", "chat", store.Outgoing{Space: "spaces/portal-dev", Files: files}, OriginYou); err != nil {
		t.Fatal(err)
	}
	if done := a.DeliverDue(ctx); len(done) != 1 || done[0].Status != store.StatusSent {
		t.Fatalf("chat deliver: %+v", done)
	}
	if c := g.Chat[len(g.Chat)-1]; len(c.Files) != 1 || c.Files[0].Name != "notes.txt" {
		t.Fatalf("chat upload: %+v", c)
	}

	// an agent's files always ask, whatever the policy
	a.Config.SendPolicy = config.PolicyAllow
	files, _ = a.AttachFiles([]string{src})
	it, _ := a.Queue("work", "mail", store.Outgoing{To: []string{"rohan.d@example.com"}, Subject: "x", Body: "b", Files: files}, OriginAgent)
	if it.Status != store.StatusApproval {
		t.Fatalf("agent files went out without asking: %+v", it)
	}
	if _, err := a.AttachFiles([]string{"/nonexistent/x"}); err == nil {
		t.Fatal("missing file attached")
	}
	if _, err := a.AttachFiles([]string{t.TempDir()}); err == nil {
		t.Fatal("a folder attached")
	}
}

func TestChatUnreadSurvivesSync(t *testing.T) {
	a, _ := setup(t)
	ctx := context.Background()
	if _, errs := a.SyncAll(ctx); len(errs) > 0 {
		t.Fatal(errs)
	}
	unread := func() int {
		sps, _ := a.Store.Spaces(nil, nil)
		for _, s := range sps {
			if s.Name == "spaces/portal-dev" {
				return s.Unread
			}
		}
		return -1
	}
	if err := a.MarkSpaceRead(ctx, "work", "spaces/portal-dev"); err != nil || unread() != 0 {
		t.Fatalf("read: %v %d", err, unread())
	}
	// your own message last: unread still marks the others' latest
	if _, err := a.Queue("work", "chat", store.Outgoing{Space: "spaces/portal-dev", Body: "mine"}, OriginYou); err != nil {
		t.Fatal(err)
	}
	a.DeliverDue(ctx)
	if err := a.MarkSpaceUnread(ctx, "work", "spaces/portal-dev"); err != nil || unread() != 1 {
		t.Fatalf("unread: %v %d", err, unread())
	}
	if _, errs := a.SyncAll(ctx); len(errs) > 0 {
		t.Fatal(errs)
	}
	if unread() != 1 {
		t.Fatalf("sync undid mark unread: %d", unread())
	}
}

func TestBackfillAndKeep(t *testing.T) {
	a, g := setup(t)
	ctx := context.Background()
	// an old thread, outside the first sync's 30 days
	g.Messages = append(g.Messages, &fake.Message{ID: "m-old", ThreadID: "t-old", Labels: []string{"INBOX"}, From: "Old Friend <old@example.com>",
		To: "you@example.com", Subject: "From last year", Body: "hello from 2025", Date: time.Now().AddDate(-1, 0, 0), HistoryID: 50})
	a.Config.InitialDays = 30
	a.Config.Keep = "none"
	if _, errs := a.SyncAll(ctx); len(errs) > 0 {
		t.Fatal(errs)
	}
	if a.Store.HasThread("work", "t-old") {
		t.Fatal("the first sync should only cover 30 days")
	}
	a.Config.Keep = "all"
	if _, errs := a.SyncAll(ctx); len(errs) > 0 {
		t.Fatal(errs)
	}
	if !a.Store.HasThread("work", "t-old") {
		t.Fatal("the backfill should have brought the old thread in")
	}
	if b := a.Store.Backfill("work"); !b.Done {
		t.Fatalf("backfill state: %+v", b)
	}
	// keep = none: nothing older than the first sync
	for q, want := range map[string]string{"1y": "newer_than:1y", "6m": "newer_than:6m", "2w": "newer_than:14d", "all": ""} {
		if got, ok := KeepQuery(q); !ok || got != want {
			t.Errorf("KeepQuery(%q) = %q %v", q, got, ok)
		}
	}
	if _, ok := KeepQuery("none"); ok {
		t.Error("none should turn the backfill off")
	}
}

func TestLoadOlder(t *testing.T) {
	a, g := setup(t)
	ctx := context.Background()
	a.Config.Keep = "none"
	for i := 0; i < 60; i++ {
		id := fmt.Sprintf("old%02d", i)
		g.Messages = append(g.Messages, &fake.Message{ID: "m" + id, ThreadID: "t" + id, Labels: []string{"INBOX"}, From: "Old <old@example.com>",
			To: "you@example.com", Subject: "old " + id, Body: "b", Date: time.Now().AddDate(0, -2, -i), HistoryID: 50})
	}
	if _, errs := a.SyncAll(ctx); len(errs) > 0 {
		t.Fatal(errs)
	}
	inbox := config.Section{Name: "Inbox", Label: "INBOX"}
	oldest := func() int64 {
		ts, _ := a.SectionThreads(inbox, nil)
		return ts[len(ts)-1].LastDate
	}
	before, _ := a.SectionThreads(inbox, nil)
	n, err := a.LoadOlder(ctx, "work", inbox, oldest(), 10)
	if err != nil || n != 10 {
		t.Fatalf("first page: %d %v", n, err)
	}
	n, _ = a.LoadOlder(ctx, "work", inbox, oldest(), 40)
	if n != 40 {
		t.Fatalf("a bigger page: %d", n)
	}
	n, _ = a.LoadOlder(ctx, "work", inbox, oldest(), 25)
	after, _ := a.SectionThreads(inbox, nil)
	if n != 10 || len(after) != len(before)+60 {
		t.Fatalf("second page: %d, %d → %d threads", n, len(before), len(after))
	}
	if n, _ := a.LoadOlder(ctx, "work", inbox, oldest(), 10); n != 0 {
		t.Fatalf("nothing older left, got %d", n)
	}
}
