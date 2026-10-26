package cli

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Theyashsawarkar/sandesh/internal/app"
	"github.com/Theyashsawarkar/sandesh/internal/fake"
	"github.com/Theyashsawarkar/sandesh/internal/store"
)

func TestMailTemplateRoundTrip(t *testing.T) {
	base := store.Outgoing{To: []string{"a@x.example"}, Subject: "Re: hi", ThreadID: "t1", InReplyTo: "<m@x>"}
	text := MailTemplate(base, "On Mon, A wrote:\n> hello")
	// nothing written: only the quote is left
	if _, ok := ParseMailTemplate(text, base); ok {
		t.Fatal("an untouched template should not send")
	}
	text = strings.Replace(text, "Cc: ", "Cc: c@x.example, d@x.example", 1)
	text = strings.Replace(text, marker+"\n", marker+"\nSounds good.\n", 1)
	out, ok := ParseMailTemplate(text, base)
	if !ok || out.ThreadID != "t1" || len(out.Cc) != 2 || !strings.HasPrefix(out.Body, "Sounds good.") || !strings.Contains(out.Body, "> hello") {
		t.Fatalf("%+v %v", out, ok)
	}
}

func TestReplyTo(t *testing.T) {
	acc := store.Account{Email: "me@x.example"}
	th := store.Thread{ID: "t1", Subject: "Plan"}
	msgs := []store.Message{
		{FromAddr: "priya@x.example", FromName: "Priya", To: "me@x.example, arjun@x.example", Cc: "neha@x.example", MessageID: "<1@x>", Body: "Thoughts?"},
		{FromAddr: "me@x.example", To: "priya@x.example", MessageID: "<2@x>", References: "<1@x>", Body: "Looking"},
	}
	r := ReplyTo(acc, th, msgs, false)
	if r.Subject != "Re: Plan" || len(r.To) != 1 || r.To[0] != "priya@x.example" || r.InReplyTo != "<1@x>" || len(r.Cc) != 0 {
		t.Fatalf("reply: %+v", r)
	}
	all := ReplyTo(acc, th, msgs, true)
	if strings.Join(all.Cc, ",") != "arjun@x.example,neha@x.example" {
		t.Fatalf("reply all cc: %v", all.Cc)
	}
}

func TestChatTemplate(t *testing.T) {
	if _, ok := ParseChatTemplate(ChatTemplate("dev", "")); ok {
		t.Fatal("empty chat should not send")
	}
	if s, ok := ParseChatTemplate(ChatTemplate("dev", "") + "on it\n"); !ok || s != "on it" {
		t.Fatalf("%q", s)
	}
}

func TestForwardKeepsAttachments(t *testing.T) {
	g := fake.New()
	g.Seed()
	srv := httptest.NewServer(g)
	t.Cleanup(srv.Close)
	t.Setenv("SANDESH_GOOGLE_URL", srv.URL)
	t.Setenv("SANDESH_HOME", t.TempDir())
	a, err := app.Open()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	a.Config.UndoSeconds = 0
	_ = a.Store.AddAccount(store.Account{Name: "work"})
	if _, errs := a.SyncAll(context.Background()); len(errs) > 0 {
		t.Fatal(errs)
	}
	th, _ := a.Store.Thread("work", "t-hr")
	msgs, _ := a.Store.Messages("work", "t-hr")
	out := Forward(th, msgs)
	if len(out.Files) != 1 || out.Files[0].Ref == "" {
		t.Fatalf("forward files: %+v", out.Files)
	}
	// the editor round trip keeps it, unless removed from Attach:
	text := MailTemplate(out, "")
	if !strings.Contains(text, "Attach: original: holidays-2027.csv") {
		t.Fatalf("template: %s", text)
	}
	text = strings.Replace(text, "To: ", "To: rohan.d@example.com", 1)
	edited, ok := ParseMailTemplate(text, out)
	if !ok || len(edited.Files) != 1 || edited.Files[0].Ref == "" {
		t.Fatalf("parsed: %+v %v", edited.Files, ok)
	}
	if _, err := a.Queue("work", "mail", edited, app.OriginYou); err != nil {
		t.Fatal(err)
	}
	if done := a.DeliverDue(context.Background()); len(done) != 1 || done[0].Status != store.StatusSent {
		t.Fatalf("deliver: %+v", done)
	}
	last := g.Messages[len(g.Messages)-1]
	if len(last.Files) != 1 || !strings.Contains(string(last.Files[0].Data), "Republic Day") {
		t.Fatalf("forwarded mail: %+v", last.Files)
	}
	dropped, _ := ParseMailTemplate(strings.Replace(text, "Attach: original: holidays-2027.csv", "Attach: ", 1), out)
	if len(dropped.Files) != 0 {
		t.Fatal("removing it from Attach: should drop it")
	}
}
