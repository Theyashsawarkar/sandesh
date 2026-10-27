package tui

import (
	"context"
	"flag"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

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

var lastFake *fake.Server

func model(t *testing.T) Model {
	g := fake.New()
	g.Seed()
	lastFake = g
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
		case "ctrl+t":
			msg = tea.KeyMsg{Type: tea.KeyCtrlT}
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
	// / opens the search window: it searches this section, in the background
	m = press(m, "/", "l", "u", "n", "c", "h")
	m = runSearch(t, m)
	if m.mode != modeFind || len(m.find.mail) != 1 || !strings.Contains(m.View(), "in ") {
		t.Fatalf("search window: %+v", m.find)
	}
	m = press(m, "enter")
	if th, ok := m.curThread(); !ok || th.ID != "t-rohan" || m.search == nil {
		t.Fatalf("opened: %+v %v", th, m.search)
	}
	m = press(m, "h", "tab")
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

// runSearch runs the search window's background work, as the screen would.
func runSearch(t *testing.T, m Model) Model {
	t.Helper()
	f := m.find
	for _, msg := range []tea.Msg{findLocalTick{f.seq}, findServerTick{f.seq}} {
		nm, cmd := m.Update(msg)
		m = nm.(Model)
		for cmd != nil {
			res := cmd()
			nm, cmd = m.Update(res)
			m = nm.(Model)
		}
	}
	return m
}

func TestSearchScopes(t *testing.T) {
	m := model(t)
	// Today: only today's mail; Tab widens to all mail
	m = press(m, "l") // Today
	m = press(m, "/")
	m = typeText(m, "lunch")
	m = runSearch(t, m)
	if len(m.find.mail) != 0 || !strings.Contains(m.View(), "Today") {
		t.Fatalf("today shouldn't hold the lunch thread: %+v", m.find.mail)
	}
	m = press(m, "tab")
	m = runSearch(t, m)
	if len(m.find.mail) != 1 || m.find.mail[0].t.ID != "t-rohan" {
		t.Fatalf("all mail: %+v", m.find.mail)
	}
	m = press(m, "esc")
	// a chat space: only its messages
	m = press(m, "tab", "j") // chat, portal-dev
	m = press(m, "/")
	m = typeText(m, "smoke")
	m = runSearch(t, m)
	if len(m.find.chat) != 1 || len(m.find.mail) != 0 || m.find.chat[0].Space != "spaces/portal-dev" {
		t.Fatalf("space search: %+v", m.find.chat)
	}
	m = typeText(m, "zzz")
	m = runSearch(t, m)
	if len(m.find.chat) != 0 {
		t.Fatal("no match expected")
	}
	// enter opens the chat at the message
	m = press(m, "backspace", "backspace", "backspace")
	m = runSearch(t, m)
	m = press(m, "enter")
	if sp, ok := m.curSpace(); !ok || sp.Name != "spaces/portal-dev" || m.flashMsg == "" {
		t.Fatalf("opened %+v %q", sp, m.flashMsg)
	}
}

func TestSchedule(t *testing.T) {
	m := model(t)
	m = press(m, "c")
	m = typeText(m, "rohan.d@example.com")
	m = press(m, "tab")
	m = typeText(m, "later")
	m = press(m, "tab", "tab")
	m = typeText(m, "see you")
	m = press(m, "ctrl+t")
	if m.mode != modeWhen || !strings.Contains(m.View(), "Tomorrow morning") {
		t.Fatal("ctrl+t should ask when")
	}
	m = typeText(m, "in 2h")
	if !strings.Contains(m.View(), "→ ") {
		t.Fatal("the typed time should be read back")
	}
	m = press(m, "enter")
	q, _ := m.a.Store.Outbox(store.StatusQueued)
	if len(q) != 1 || q[0].Out.ScheduledFor == 0 || time.Until(time.UnixMilli(q[0].SendAt)) < 119*time.Minute {
		t.Fatalf("scheduled: %+v", q)
	}
	// it's in the Scheduled tab (not Sent) with its time; t moves it
	for m.currentSection().Label != "SENT" {
		m = press(m, "l")
	}
	if strings.Contains(m.View(), "⏰") {
		t.Fatal("Sent shouldn't list scheduled mail")
	}
	m = press(m, "l")
	if m.currentSection().Label != "SCHEDULED" || !strings.Contains(m.View(), "Scheduled 1") {
		t.Fatalf("no Scheduled tab:\n%s", m.View())
	}
	if !strings.Contains(m.View(), "⏰ in 120 min") && !strings.Contains(m.View(), "⏰ today") && !strings.Contains(m.View(), "⏰ tomorrow") {
		t.Fatalf("Scheduled doesn't show the mail:\n%s", m.View())
	}
	m = press(m, "g", "t")
	m = typeText(m, "tomorrow 10am")
	m = press(m, "enter")
	it, _ := m.a.Store.OutboxItem(q[0].ID)
	if time.UnixMilli(it.SendAt).Hour() != 10 {
		t.Fatalf("moved: %v", time.UnixMilli(it.SendAt))
	}
}

func TestScheduledChat(t *testing.T) {
	m := model(t)
	m = press(m, "tab", "j", "i") // chat, portal-dev, type
	m = typeText(m, "standup notes")
	m = press(m, "ctrl+t")
	m = typeText(m, "tomorrow 9am")
	m = press(m, "enter")
	q, _ := m.a.Store.Outbox(store.StatusQueued)
	if len(q) != 1 || q[0].Kind != "chat" || q[0].Out.ScheduledFor == 0 {
		t.Fatalf("scheduled chat: %+v", q)
	}
	for chatSections[m.sec] != "Scheduled" {
		m = press(m, "l")
	}
	if len(m.spaces) != 1 || !strings.Contains(m.View(), "⏰ tomorrow 09:00") || !strings.Contains(m.View(), "Scheduled 1") {
		t.Fatalf("chat Scheduled tab:\n%s", m.View())
	}
	if _, ok := m.curSpace(); ok {
		t.Fatal("a scheduled row is not a space")
	}
	m = press(m, "z")
	if it, _ := m.a.Store.OutboxItem(q[0].ID); it.Status != store.StatusCancelled || len(m.spaces) != 0 {
		t.Fatalf("z should cancel it: %s, %d rows", it.Status, len(m.spaces))
	}
}

func TestNewConversation(t *testing.T) {
	m := model(t)
	m = press(m, "tab", "N")
	if m.mode != modeConvo || !strings.Contains(m.View(), "new conversation") {
		t.Fatal("N should open (and draw) the new conversation window")
	}
	m = typeText(m, "arj")
	if len(m.convo.sugg) == 0 || m.convo.sugg[0].label() != "Arjun Mehta" {
		t.Fatalf("people: %+v", m.convo.sugg)
	}
	nm, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = nm.(Model)
	if cmd == nil {
		t.Fatal("enter should open the conversation")
	}
	nm, _ = m.Update(cmd())
	m = nm.(Model)
	sp, ok := m.curSpace()
	if !ok || sp.Type != "DIRECT_MESSAGE" || m.mode != modeChatMsg {
		t.Fatalf("opened %+v ok=%v mode=%v err=%s", sp, ok, m.mode, m.err)
	}
	// search in chat finds people and conversations by name
	m = press(m, "esc")
	m.find = nil
	m.sec = 0
	m.reload()
	m = press(m, "/")
	m.find.scope = scopeAllChat
	m = typeText(m, "priya")
	m = runSearch(t, m)
	if len(m.find.convos) == 0 || len(m.find.people) == 0 {
		t.Fatalf("convos %d people %d", len(m.find.convos), len(m.find.people))
	}
	if !strings.Contains(m.View(), "PEOPLE") || !strings.Contains(m.View(), "CONVERSATIONS") {
		t.Fatalf("groups missing:\n%s", m.View())
	}
}

// runCmds runs a command and what follows from it, as the screen would.
func runCmds(m Model, cmd tea.Cmd) Model {
	for cmd != nil {
		msg := cmd()
		if b, ok := msg.(tea.BatchMsg); ok {
			for _, c := range b {
				m = runCmds(m, c)
			}
			return m
		}
		if msg == nil {
			return m
		}
		var nm tea.Model
		nm, cmd = m.Update(msg)
		m = nm.(Model)
	}
	return m
}

func TestGroupNameAndSignIn(t *testing.T) {
	m := model(t)
	g := lastFake
	g.NeedsScope = true
	m = press(m, "tab", "N")
	m = typeText(m, "arj")
	m = press(m, "tab")
	m = typeText(m, "neh")
	nm, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = nm.(Model)
	if !m.convo.naming || !strings.Contains(m.View(), "Name") {
		t.Fatal("two people: Enter should ask for a group name first")
	}
	m = typeText(m, "Release crew")
	nm, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = runCmds(nm.(Model), cmd)
	if m.convo == nil || !m.convo.signIn || !strings.Contains(m.View(), "one more permission") {
		t.Fatalf("the missing permission should ask to sign in again: %+v %s", m.convo, m.err)
	}
	// signing in again (the fake needs no browser), then it carries on
	g.NeedsScope = false
	nm, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = runCmds(nm.(Model), cmd)
	sp, ok := m.curSpace()
	if !ok || sp.Type != "SPACE" || sp.DisplayName != "Release crew" || m.convo != nil {
		t.Fatalf("named group: %+v ok=%v err=%s", sp, ok, m.err)
	}
}
