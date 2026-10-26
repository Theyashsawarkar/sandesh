package cli

import (
	"strings"
	"testing"

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
