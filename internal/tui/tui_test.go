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
