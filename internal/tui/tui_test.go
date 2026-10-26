package tui

import (
	"context"
	"flag"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Theyashsawarkar/sandesh/internal/app"
	"github.com/Theyashsawarkar/sandesh/internal/fake"
	"github.com/Theyashsawarkar/sandesh/internal/store"
)

var update = flag.Bool("update", false, "rewrite scripts/keybindings")

// scripts/keybindings is what Vayu's keybinding search reads.
func TestKeybindingsFile(t *testing.T) {
	path := "../../scripts/keybindings"
	if *update {
		if err := os.WriteFile(path, []byte(KeybindLines()), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	b, err := os.ReadFile(path)
	if err != nil || string(b) != KeybindLines() {
		t.Fatal("scripts/keybindings is out of date: go test ./internal/tui -update")
	}
}

// The README documents every key.
func TestReadmeHasEveryKey(t *testing.T) {
	b, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range Keys {
		if !strings.Contains(string(b), k.Desc) {
			t.Errorf("README lacks %q (%s)", k.Desc, k.Keys)
		}
	}
}

func model(t *testing.T) Model {
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
	_ = a.Store.AddAccount(store.Account{Name: "work", Chat: true})
	a.SyncAll(context.Background())
	m := New(a, "")
	nm, _ := m.Update(tea.WindowSizeMsg{Width: 200, Height: 30})
	return nm.(Model)
}

func press(m Model, keys ...string) Model {
	for _, k := range keys {
		var msg tea.KeyMsg
		switch k {
		case "tab":
			msg = tea.KeyMsg{Type: tea.KeyTab}
		case "enter":
			msg = tea.KeyMsg{Type: tea.KeyEnter}
		case "esc":
			msg = tea.KeyMsg{Type: tea.KeyEsc}
		case "ctrl+s":
			msg = tea.KeyMsg{Type: tea.KeyCtrlS}
		case "down":
			msg = tea.KeyMsg{Type: tea.KeyDown}
		case "shift+tab":
			msg = tea.KeyMsg{Type: tea.KeyShiftTab}
		case "backspace":
			msg = tea.KeyMsg{Type: tea.KeyBackspace}
		default:
			msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
		}
		nm, _ := m.Update(msg)
		m = nm.(Model)
	}
	return m
}

func TestScreen(t *testing.T) {
	m := model(t)
	v := m.View()
	for _, want := range []string{"Inbox 3", "Release cut on Thursday?", "QA signed off", "Chat 2"} {
		if !strings.Contains(v, want) {
			t.Fatalf("first screen lacks %q:\n%s", want, v)
		}
	}
	m = press(m, "j", "j")
	if th, _ := m.curThread(); th.ID != "t-arjun" {
		t.Fatalf("j j selected %s", th.ID)
	}
	m = press(m, "/", "l", "u", "n", "c", "h", "enter")
	if len(m.threads) != 1 || m.threads[0].ID != "t-rohan" {
		t.Fatalf("filter: %+v", m.threads)
	}
	m = press(m, "esc", "tab")
	if !strings.Contains(m.View(), "portal-dev") {
		t.Fatal("chat tab")
	}
	m = press(m, "?")
	if !strings.Contains(m.View(), "Claude's summary") {
		t.Fatal("help sheet")
	}
	m = press(m, "esc", "l")
	if chatSections[m.sec] != "Unread" || len(m.spaces) != 2 {
		t.Fatalf("chat Unread section: %d", len(m.spaces))
	}
}

func typeText(m Model, s string) Model {
	nm, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)})
	return nm.(Model)
}

func TestComposer(t *testing.T) {
	m := model(t)
	m.a.Config.UndoSeconds = 60
	// the Gmail signature (fetched in the background when composing)
	if sig := m.a.Signature(context.Background(), "work", true); !strings.Contains(sig, "Example Corp") {
		t.Fatalf("signature: %q", sig)
	}
	m = press(m, "c")
	if m.mode != modeCompose || !strings.Contains(m.View(), "new mail") {
		t.Fatal("c should open the compose screen")
	}
	// autocomplete from contacts: Tab takes the suggestion
	m = typeText(m, "pri")
	if len(m.comp.sugg) == 0 || m.comp.sugg[0].Addr != "priya.k@example.com" || !strings.Contains(m.View(), "Priya Kulkarni") {
		t.Fatalf("suggestions: %+v", m.comp.sugg)
	}
	m = press(m, "tab", "shift+tab")
	// the directory: found after typing pauses, picked with ↓ Enter
	m = typeText(m, "kav")
	nm, cmd := m.Update(dirTickMsg{"kav"})
	m = nm.(Model)
	if cmd == nil {
		t.Fatal("no directory lookup")
	}
	nm, _ = m.Update(cmd())
	m = nm.(Model)
	if len(m.comp.sugg) == 0 || m.comp.sugg[0].Addr != "kavya.i@example.com" {
		t.Fatalf("directory suggestions: %+v", m.comp.sugg)
	}
	m = press(m, "enter")
	// a typed address, then Backspace on an empty field drops the last chip
	m = typeText(m, "x@y.example")
	m = press(m, ",", "backspace")
	if got := len(m.comp.rcpt[fTo]); got != 2 {
		t.Fatalf("recipients: %+v", m.comp.rcpt[fTo])
	}
	// Cc and Bcc stay hidden until wanted: To → Subject → Files → body
	m = press(m, "tab")
	m = typeText(m, "Release plan")
	m = press(m, "tab", "tab")
	m = typeText(m, "Thursday works.")
	m = press(m, "ctrl+s")
	if m.mode != modeNormal {
		t.Fatalf("not sent: %s", m.err)
	}
	q, _ := m.a.Store.Outbox(store.StatusQueued)
	if len(q) != 1 {
		t.Fatalf("outbox: %+v", q)
	}
	out := q[0].Out
	if strings.Join(out.To, "|") != `"Priya Kulkarni" <priya.k@example.com>|"Kavya Iyer" <kavya.i@example.com>` ||
		out.Subject != "Release plan" || !strings.HasPrefix(out.Body, "Thursday works.\n\n-- \n") {
		t.Fatalf("queued: %+v", out)
	}

	// a reply: recipients and thread filled in, the quote kept under
	m = press(m, "g", "r")
	if m.mode != modeCompose || m.comp.focus != fBody || !strings.Contains(m.View(), "ctrl+q shows it") || !strings.Contains(m.View(), "replying to") {
		t.Fatalf("reply composer: focus %d", m.comp.focus)
	}
	m = typeText(m, "Yes.")
	m = press(m, "ctrl+s")
	q, _ = m.a.Store.Outbox(store.StatusQueued)
	r := q[0].Out
	if r.ThreadID != "t-priya" || !strings.HasPrefix(r.Body, "Yes.\n\n-- \n") || !strings.Contains(r.Body, "\n\nOn ") || !strings.Contains(r.Body, "> ") {
		t.Fatalf("reply: %+v", r)
	}

	// Esc: once warns when something is written, twice discards
	m = press(m, "c", "tab", "tab", "tab")
	m = typeText(m, "unsent")
	m = press(m, "esc")
	if m.mode != modeCompose {
		t.Fatal("one Esc discarded a written message")
	}
	m = press(m, "esc")
	if m.mode != modeNormal {
		t.Fatal("second Esc should discard")
	}
}
