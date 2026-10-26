package app

import (
	"context"
	"net/http/httptest"
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
