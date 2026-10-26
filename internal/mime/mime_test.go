package mime

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestHTMLToText(t *testing.T) {
	got := HTMLToText(`<html><head><style>p{}</style></head><body><p>Hi&nbsp;there,</p><p>See <a href="https://x.example/pr/1">the PR</a><br>and <b>reply</b>.</p><ul><li>one</li><li>two</li></ul></body></html>`)
	got = tidy(got)
	for _, want := range []string{"Hi there,", "See the PR (https://x.example/pr/1)", "and reply.", "• one", "• two"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
	if strings.Contains(got, "p{}") {
		t.Errorf("style leaked: %q", got)
	}
}

func TestTextPrefersPlain(t *testing.T) {
	enc := func(s string) string { return base64.URLEncoding.EncodeToString([]byte(s)) }
	p := Part{MimeType: "multipart/mixed", Parts: []Part{
		{MimeType: "multipart/alternative", Parts: []Part{
			{MimeType: "text/plain", Body: struct {
				AttachmentID string `json:"attachmentId"`
				Size         int    `json:"size"`
				Data         string `json:"data"`
			}{Data: enc("plain body\r\n")}},
			{MimeType: "text/html", Body: struct {
				AttachmentID string `json:"attachmentId"`
				Size         int    `json:"size"`
				Data         string `json:"data"`
			}{Data: enc("<p>html body</p>")}},
		}},
		{MimeType: "application/pdf", Filename: "a.pdf", Body: struct {
			AttachmentID string `json:"attachmentId"`
			Size         int    `json:"size"`
			Data         string `json:"data"`
		}{AttachmentID: "x", Size: 2048}},
	}}
	body, atts := Text(p)
	if body != "plain body" || len(atts) != 1 || atts[0].Name != "a.pdf" {
		t.Fatalf("got %q %+v", body, atts)
	}
}

func TestBuild(t *testing.T) {
	raw, err := Build(Draft{From: "me@x.example", To: []string{"Priya <priya@x.example>"}, Subject: "Re: Thursday — ok?", Body: "Yes.\nThanks", InReplyTo: "<a@b>"})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := base64.URLEncoding.DecodeString(raw)
	s := string(b)
	for _, want := range []string{"To: Priya <priya@x.example>", "Subject: =?utf-8?q?Re:_Thursday_=E2=80=94_ok=3F?=", "In-Reply-To: <a@b>", "Content-Transfer-Encoding: base64"} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in\n%s", want, s)
		}
	}
	if _, err := Build(Draft{To: []string{"nope"}}); err == nil {
		t.Error("bad address accepted")
	}
	if _, err := Build(Draft{}); err == nil {
		t.Error("no recipients accepted")
	}
}

func TestAddresses(t *testing.T) {
	if n, a := ParseAddress(`"Kulkarni, Priya" <priya@x.example>`); n != "Kulkarni, Priya" || a != "priya@x.example" {
		t.Errorf("%q %q", n, a)
	}
	if got := ParseList("A <a@x.example>, b@y.example"); len(got) != 2 || got[1] != "b@y.example" {
		t.Errorf("%v", got)
	}
	if ReplySubject("Re: x") != "Re: x" || ReplySubject("x") != "Re: x" {
		t.Error("reply subject")
	}
}

func TestClean(t *testing.T) {
	in := "Invoice\x1b]52;c;ZXZpbA==\x07 due\x1b[2J‮nuf‬\ttab\nline"
	if got := Clean(in); got != "Invoice]52;c;ZXZpbA== due[2Jnuf\ttab\nline" {
		t.Errorf("%q", got)
	}
}
