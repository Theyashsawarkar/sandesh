package tui

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/Theyashsawarkar/sandesh/internal/app"
	"github.com/Theyashsawarkar/sandesh/internal/store"
)

// Starting a conversation (N in chat): pick people, then sandesh opens the
// DM you already have with them (earlier messages and all) or starts one;
// several people make a group chat.

type person struct{ Name, Email, ID string }

func (p person) key() string {
	if p.ID != "" {
		return p.ID
	}
	return strings.ToLower(p.Email)
}

func (p person) label() string {
	if p.Name != "" {
		return p.Name
	}
	return p.Email
}

// people finds who you could talk to: people from chat first (they have a
// chat id), then contacts from mail and the directory.
func (m Model) people(account, q string, limit int, skip map[string]bool) []person {
	var out []person
	seen := map[string]bool{}
	add := func(p person) {
		k := strings.ToLower(p.Name)
		if p.Name == "" {
			k = strings.ToLower(p.Email)
		}
		if seen[k] || seen[strings.ToLower(p.Email)] && p.Email != "" || skip[p.key()] || len(out) >= limit {
			return
		}
		seen[k] = true
		if p.Email != "" {
			seen[strings.ToLower(p.Email)] = true
		}
		out = append(out, p)
	}
	us, _ := m.a.Store.ChatUsers(account, q, limit)
	for _, u := range us {
		if !strings.HasPrefix(u.Name, "user ") {
			add(person{Name: u.Name, Email: u.Email, ID: u.ID})
		}
	}
	cs, _ := m.a.Store.SuggestContacts(account, q, limit, nil)
	for _, c := range cs {
		add(person{Name: c.Name, Email: c.Addr})
	}
	return out
}

type convoPicker struct {
	account string
	input   textinput.Model
	picked  []person
	sugg    []person
	sel     int
	busy    bool
	back    mode
	name    textinput.Model // a group's name (optional)
	naming  bool            // typing it
	people  []person        // who the last try was with
	signIn  bool            // Google wants a new sign-in first
	signURL string          // where the browser went, in case it didn't open
	named   bool            // the name step was offered
}

type (
	convoDirTick struct{ q string }
	convoDirMsg  struct{ q string }
	convoMsg     struct {
		sp     store.Space
		err    error
		people []person
	}
	signedInMsg struct{ err error }
	convoURLMsg struct{ url string }
)

func (m *Model) openConvo() {
	in := newInput(m.styles)
	in.Placeholder = "a name or an address"
	in.Focus()
	name := newInput(m.styles)
	name.Placeholder = "optional: a name makes it a space everyone sees"
	m.convo = &convoPicker{account: m.defaultAccount(), input: in, name: name, back: m.mode}
	m.mode = modeConvo
}

func (m *Model) convoSuggest() tea.Cmd {
	c := m.convo
	q := strings.TrimSpace(c.input.Value())
	if q == "" {
		c.sugg = nil
		return nil
	}
	skip := map[string]bool{}
	for _, p := range c.picked {
		skip[p.key()] = true
	}
	c.sugg = m.people(c.account, q, 7, skip)
	c.sel = min(c.sel, max(0, len(c.sugg)-1))
	if len([]rune(q)) < 2 || strings.Contains(q, "@") {
		return nil
	}
	return tea.Tick(350*time.Millisecond, func(time.Time) tea.Msg { return convoDirTick{q} })
}

// startConvo opens the conversation with everyone picked, in the background.
func (m *Model) startConvo(people []person) tea.Cmd {
	if len(people) == 0 {
		return nil
	}
	c := m.convo
	c.busy, c.people = true, people
	name := ""
	if len(people) > 1 {
		name = strings.TrimSpace(c.name.Value())
	}
	return m.conversationCmd(c.account, people, name)
}

// conversationCmd opens (or starts) the conversation in the background.
func (m *Model) conversationCmd(acc string, people []person, groupName ...string) tea.Cmd {
	gname := ""
	if len(groupName) > 0 {
		gname = groupName[0]
	}
	a := m.a
	var ids, names []string
	for _, p := range people {
		id := p.ID
		if id == "" {
			id = p.Email
		}
		ids = append(ids, id)
		names = append(names, p.label())
	}
	if len(names) > 1 {
		for i, n := range names {
			names[i] = firstWord(n)
		}
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		sp, err := a.OpenConversation(ctx, acc, ids, names, gname)
		return convoMsg{sp, err, people}
	}
}

func clearInput(in textinput.Model) textinput.Model {
	in.SetValue("")
	return in
}

// signInAgain signs the account in again in the browser (for the
// permission it lacks), then tries the conversation again.
func (m *Model) signInAgain() tea.Cmd {
	c := m.convo
	c.busy = true
	a, acc := m.a, c.account
	urls := make(chan string, 1)
	signIn := func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		err := a.Reauthorize(ctx, acc, func(u string) {
			_ = exec.Command("xdg-open", u).Start()
			urls <- u
		})
		close(urls)
		return signedInMsg{err}
	}
	// the link, in case the browser didn't open
	link := func() tea.Msg {
		if u, ok := <-urls; ok {
			return convoURLMsg{u}
		}
		return nil
	}
	return tea.Batch(signIn, link)
}

// openSpace shows a conversation, ready to type in it.
func (m *Model) openSpace(sp store.Space) {
	m.showSpace(sp)
	m.mode, m.input = modeChatMsg, nil
}

func (m *Model) showSpace(sp store.Space) {
	m.tab = tabChat
	m.search, m.filter = nil, ""
	m.sec = 0 // All
	m.reload()
	m.selectKey(sp.Account + "/" + sp.Name)
	m.scroll = 1 << 30
	m.fixView()
}

func (m Model) gotSignedIn(msg signedInMsg) (tea.Model, tea.Cmd) {
	c := m.convo
	if c == nil {
		return m, nil
	}
	c.busy = false
	if msg.err != nil {
		m.setErr("sign-in: " + msg.err.Error())
		return m, nil
	}
	c.signIn, c.signURL = false, ""
	m.say("signed in again · opening the conversation")
	return m, m.startConvo(c.people)
}

func (m Model) gotConvo(msg convoMsg) (tea.Model, tea.Cmd) {
	if m.convo != nil {
		m.convo.busy = false
	}
	if msg.err != nil {
		if errors.Is(msg.err, app.ErrNeedsSignIn) {
			if m.convo == nil { // from search: ask in the window
				m.openConvo()
			}
			m.convo.signIn, m.convo.people = true, msg.people
			if len(m.convo.picked) == 0 {
				m.convo.picked = msg.people
			}
			return m, nil
		}
		m.setErr(msg.err.Error())
		return m, nil
	}
	m.convo = nil
	m.openSpace(msg.sp)
	m.say("type your message · enter sends · esc stops typing")
	return m, nil
}

func (m Model) convoKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	c := m.convo
	if c.busy {
		return m, nil
	}
	if c.signIn {
		switch k.String() {
		case "enter":
			return m, m.signInAgain()
		case "esc", "ctrl+c":
			c.signIn = false
		}
		return m, nil
	}
	if c.naming {
		switch k.String() {
		case "esc", "tab", "shift+tab", "up":
			c.naming = false
			c.name.Blur()
			c.input.Focus()
			return m, nil
		case "enter", "ctrl+s":
			return m, m.startConvo(c.picked)
		}
		var cmd tea.Cmd
		c.name, cmd = c.name.Update(k)
		return m, cmd
	}
	switch k.String() {
	case "esc", "ctrl+c":
		m.mode, m.convo = c.back, nil
		return m, nil
	case "up", "ctrl+p":
		c.sel = max(0, c.sel-1)
		return m, nil
	case "down", "ctrl+n":
		c.sel = min(max(0, len(c.sugg)-1), c.sel+1)
		return m, nil
	case "tab", ",":
		if len(c.sugg) > 0 {
			c.picked = append(c.picked, c.sugg[c.sel])
			c.input.SetValue("")
			c.sugg, c.sel = nil, 0
		}
		return m, nil
	case "backspace":
		if c.input.Value() == "" && len(c.picked) > 0 {
			c.picked = c.picked[:len(c.picked)-1]
			return m, nil
		}
	case "enter", "ctrl+s":
		people := c.picked
		if strings.TrimSpace(c.input.Value()) != "" {
			switch {
			case len(c.sugg) > 0:
				people = append(people, c.sugg[c.sel])
			case strings.Contains(c.input.Value(), "@"):
				people = append(people, person{Email: strings.TrimSpace(c.input.Value())})
			default:
				m.say("no one by that name here; type their address, or pick from the list")
				return m, nil
			}
			c.picked, c.input = people, clearInput(c.input)
			c.sugg = nil
		}
		// two or more: a chance to name the group first
		if len(people) > 1 && !c.named {
			c.named, c.naming = true, true
			c.input.Blur()
			c.name.Focus()
			return m, nil
		}
		return m, m.startConvo(people)
	}
	var cmd tea.Cmd
	before := c.input.Value()
	c.input, cmd = c.input.Update(k)
	if c.input.Value() != before {
		return m, tea.Batch(cmd, m.convoSuggest())
	}
	return m, cmd
}

func (m Model) convoPopup() []string {
	c := m.convo
	s := m.styles
	w := min(m.w-4, 84)
	iw := w - 4
	var pills []string
	for _, p := range c.picked {
		pills = append(pills, s.muted.Render("‹")+m.avatar(p.label())+" "+s.text.Render(p.label())+s.muted.Render("›"))
	}
	in := c.input
	head := strings.Join(pills, " ")
	in.Width = max(10, iw-lipgloss.Width(head)-6)
	lines := []string{s.accent.Bold(true).Render(" ") + strings.TrimSpace(head+" "+in.View())}
	if len(c.picked) > 1 || c.naming {
		label := s.muted.Render(" Name  ")
		if c.naming {
			label = s.accent.Bold(true).Render("▎Name  ")
		}
		nm := c.name
		nm.Width = iw - 9
		lines = append(lines, label+nm.View())
	}
	lines = append(lines, s.muted.Render(strings.Repeat("─", iw)))
	switch {
	case c.signIn && c.busy:
		lines = append(lines, "", s.accent.Render(" ⟳ finish signing in in your browser…"))
		if c.signURL != "" {
			lines = append(lines, s.muted.Render(" (didn't open? "+ansi.Truncate(c.signURL, iw-18, "…")+")"))
		}
		lines = append(lines, "")
	case c.signIn:
		lines = append(lines, "", s.peach.Render(" Google wants one more permission before sandesh can start chats"),
			s.muted.Render(" (your sign-in is from before sandesh asked for it)."), "",
			s.text.Render(" Enter signs in again in your browser; then this chat opens."), "")
	case c.busy:
		lines = append(lines, "", s.accent.Render(" ⟳ opening the conversation…"), "")
	case c.naming:
		lines = append(lines, "", s.text.Render(fmt.Sprintf(" A group with %d people.", len(c.picked)+1)),
			s.muted.Render(" With a name it's a space (you can rename it later with n);"),
			s.muted.Render(" without one, a group chat. Enter starts it."), "")
	case len(c.sugg) == 0 && strings.TrimSpace(c.input.Value()) == "":
		lines = append(lines, "", s.muted.Render(" Who do you want to talk to? Your conversation with them opens,"),
			s.muted.Render(" with what you said before; a new one starts if there is none."), "")
	case len(c.sugg) == 0:
		lines = append(lines, "", s.muted.Render(" no one by that name; an address works too"), "")
	default:
		q := strings.ToLower(strings.TrimSpace(c.input.Value()))
		for i, p := range c.sugg {
			mark := "  "
			nameS := s.text
			if i == c.sel {
				mark, nameS = s.accent.Bold(true).Render("▌ "), s.bold
			}
			where := s.muted.Render(p.Email)
			if p.ID != "" && p.Email == "" {
				where = s.muted.Render("from chat")
			}
			lines = append(lines, ansi.Truncate(mark+m.avatar(p.label())+" "+highlight(p.label(), []string{q}, nameS, s.accent.Bold(true))+"  "+where, iw, "…"))
		}
	}
	keys := " ↑↓ pick · tab adds another person · enter opens the conversation · esc cancels"
	switch {
	case c.signIn:
		keys = " enter signs in again · esc goes back"
	case c.naming:
		keys = " type a name (or none) · enter starts the group · tab back to people"
	case len(c.picked)+min(1, len(c.sugg)) > 1:
		keys = " ↑↓ pick · tab adds another person · enter: name the group, then start it · esc cancels"
	}
	lines = append(lines, s.muted.Render(strings.Repeat("─", iw)), s.muted.Render(keys))
	return m.popupFrame("new conversation", lines, w)
}
