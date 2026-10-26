package tui

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/Theyashsawarkar/sandesh/internal/cli"
	"github.com/Theyashsawarkar/sandesh/internal/config"
	"github.com/Theyashsawarkar/sandesh/internal/mime"
	"github.com/Theyashsawarkar/sandesh/internal/store"
)

// The search window (/): a floating panel over the screen. What's in the
// local copy shows as you type; when typing pauses, Gmail's own search
// runs too and adds what isn't here yet. Mail and chat both, with a
// preview of the highlighted result.

type finder struct {
	input      textinput.Model
	mail       []findMail
	chat       []store.ChatHit
	sel        int
	serverBusy bool
	serverQ    string // the query Gmail last answered
	serverErr  string
}

type findMail struct {
	t      store.Thread
	remote bool // found by Gmail's search, not in the local copy before
}

type findTickMsg struct{ q string }

type findServerMsg struct {
	q   string
	ts  []store.Thread
	err error
}

func (m *Model) openFinder() {
	in := newInput(m.styles)
	in.Placeholder = "search mail and chat · Gmail search works: from:priya has:attachment newer_than:7d"
	in.Focus()
	m.find = &finder{input: in}
	m.mode = modeFind
}

func (f *finder) query() string { return strings.TrimSpace(f.input.Value()) }

func (f *finder) count() int { return len(f.mail) + len(f.chat) }

// plain words of a query, without Gmail operators (from:, has:...)
func plainWords(q string) string {
	var w []string
	for _, x := range strings.Fields(q) {
		if !strings.Contains(x, ":") {
			w = append(w, x)
		}
	}
	return strings.Join(w, " ")
}

// findLocal searches the local copy (instant).
func (m *Model) findLocal() {
	f := m.find
	q := f.query()
	keep := map[string]bool{}
	for _, x := range f.mail {
		if x.remote && f.serverQ == q {
			keep[x.t.Account+"/"+x.t.ID] = true
		}
	}
	var mail []findMail
	if words := plainWords(q); words != "" {
		ts, _ := m.a.Store.Threads(store.ThreadQuery{Accounts: m.accountFilter(), Text: words, Limit: 40})
		for _, t := range ts {
			mail = append(mail, findMail{t: t})
		}
	}
	// Gmail's results for this same query stay
	if f.serverQ == q {
		seen := map[string]bool{}
		for _, x := range mail {
			seen[x.t.Account+"/"+x.t.ID] = true
		}
		for _, x := range f.mail {
			if x.remote && !seen[x.t.Account+"/"+x.t.ID] {
				mail = append(mail, x)
			}
		}
		sortMail(mail)
	}
	f.mail = mail
	f.chat = nil
	if words := plainWords(q); words != "" {
		f.chat, _ = m.a.Store.SearchChat(m.accountFilter(), words, 30)
	}
	f.sel = min(f.sel, max(0, f.count()-1))
}

func sortMail(x []findMail) {
	sort.SliceStable(x, func(i, j int) bool { return x[i].t.LastDate > x[j].t.LastDate })
}

func (m *Model) findServer(q string) tea.Cmd {
	f := m.find
	if f == nil || f.query() != q || len([]rune(q)) < 2 || f.serverQ == q {
		return nil
	}
	f.serverBusy, f.serverErr = true, ""
	a := m.a
	accs := m.accountFilter()
	if len(accs) == 0 {
		accs = m.accounts
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		var all []store.Thread
		for _, acc := range accs {
			ts, err := a.SearchServer(ctx, acc, q, 25)
			if err != nil {
				return findServerMsg{q, all, err}
			}
			all = append(all, ts...)
		}
		return findServerMsg{q, all, nil}
	}
}

func (m *Model) gotFindServer(msg findServerMsg) {
	f := m.find
	if f == nil || f.query() != msg.q {
		return
	}
	f.serverBusy, f.serverQ = false, msg.q
	if msg.err != nil {
		f.serverErr = msg.err.Error()
	}
	seen := map[string]bool{}
	for _, x := range f.mail {
		seen[x.t.Account+"/"+x.t.ID] = true
	}
	for _, t := range msg.ts {
		if !seen[t.Account+"/"+t.ID] {
			f.mail = append(f.mail, findMail{t: t, remote: true})
			seen[t.Account+"/"+t.ID] = true
		}
	}
	sortMail(f.mail)
}

func (m Model) finderKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	f := m.find
	switch k.String() {
	case "esc", "ctrl+c":
		m.find, m.mode = nil, modeNormal
		return m, nil
	case "up", "ctrl+p", "ctrl+k":
		f.sel = max(0, f.sel-1)
		return m, nil
	case "down", "ctrl+n", "ctrl+j":
		f.sel = min(max(0, f.count()-1), f.sel+1)
		return m, nil
	case "tab":
		// jump between the mail and chat groups
		if f.sel < len(f.mail) && len(f.chat) > 0 {
			f.sel = len(f.mail)
		} else if len(f.mail) > 0 {
			f.sel = 0
		}
		return m, nil
	case "ctrl+u":
		f.input.SetValue("")
		m.findLocal()
		return m, nil
	case "enter":
		return m.openFound()
	}
	var cmd tea.Cmd
	before := f.query()
	f.input, cmd = f.input.Update(k)
	if q := f.query(); q != before {
		f.sel = 0
		m.findLocal()
		return m, tea.Batch(cmd, tea.Tick(450*time.Millisecond, func(time.Time) tea.Msg { return findTickMsg{q} }))
	}
	return m, cmd
}

// openFound shows the chosen result: a mail thread in a search section
// holding every mail result, or the chat space of a message.
func (m Model) openFound() (tea.Model, tea.Cmd) {
	f := m.find
	q := f.query()
	if f.count() == 0 {
		if q != "" {
			m.find, m.mode = nil, modeNormal
			return m, m.searchCmd(q) // nothing local yet: Gmail decides
		}
		return m, nil
	}
	m.find, m.mode = nil, modeNormal
	if f.sel < len(f.mail) {
		chosen := f.mail[f.sel].t
		name := "search: " + q
		byAcc := map[string][]string{}
		for _, x := range f.mail {
			byAcc[x.t.Account] = append(byAcc[x.t.Account], x.t.ID)
		}
		for acc, ids := range byAcc {
			_ = m.a.Store.SetSectionThreads(acc, name, ids)
		}
		m.tab = tabMail
		m.search = &config.Section{Name: name, Query: q}
		m.filter = ""
		m.reload()
		m.selectKey(chosen.Account + "/" + chosen.ID)
		m.say(fmt.Sprintf("%d found · W saves it as a section · h/l leaves", len(f.mail)))
		return m, nil
	}
	hit := f.chat[f.sel-len(f.mail)]
	m.tab = tabChat
	m.search, m.filter = nil, ""
	m.sec = 0 // All
	m.reload()
	m.selectKey(hit.Account + "/" + hit.Space)
	return m, nil
}

func (m *Model) selectKey(key string) {
	for i := 0; i < m.count(); i++ {
		if m.keyAt(i) == key {
			m.sel = i
			break
		}
	}
	m.clamp()
	m.loadPreview(true)
	m.fixView()
}

// ------------------------------------------------------------------ view

func (m Model) finderView() []string {
	f := m.find
	s := m.styles
	w := min(m.w-6, 150)
	h := min(m.h-4, 40)
	iw := w - 4
	q := f.query()

	status := s.muted.Render("local")
	switch {
	case f.serverBusy:
		status = s.accent.Render("⟳ asking Gmail…")
	case f.serverErr != "":
		status = s.red.Render("Gmail: " + ansi.Truncate(f.serverErr, 30, "…"))
	case f.serverQ != "" && f.serverQ == q:
		status = s.green.Render("✓ local + Gmail")
	}
	in := f.input
	in.Width = iw - lipgloss.Width(status) - 6
	var lines []string
	lines = append(lines, row(s.accent.Bold(true).Render(" ")+in.View(), status, iw))
	lines = append(lines, s.muted.Render(strings.Repeat("─", iw)))

	listW := max(40, iw*46/100)
	prevW := iw - listW - 3
	bodyH := h - 2 - len(lines) - 2
	list := m.findList(listW, q)
	// keep the highlighted result in view
	top := 0
	for i, l := range list {
		if strings.Contains(l, "\x00sel") {
			list[i] = strings.ReplaceAll(l, "\x00sel", "")
			if i+3 > bodyH {
				top = i + 3 - bodyH
			}
		}
	}
	if top < len(list) {
		list = list[top:]
	}
	prev := m.findPreview(prevW)
	for i := 0; i < bodyH; i++ {
		left, right := "", ""
		if i < len(list) {
			left = list[i]
		}
		if i < len(prev) {
			right = prev[i]
		}
		lines = append(lines, fit(left, listW)+s.muted.Render(" │ ")+fit(right, prevW))
	}
	lines = append(lines, s.muted.Render(strings.Repeat("─", iw)))
	keys := s.muted.Render("↑↓ move · enter opens · tab mail/chat · ctrl+u clears · esc closes")
	if q == "" {
		keys = s.muted.Render("type to search · results from this machine at once, Gmail's after a moment")
	}
	lines = append(lines, " "+keys)

	b := fg(s.c.Accent)
	title := " search "
	top2 := b.Render("╭─") + b.Bold(true).Render(title) + b.Render(strings.Repeat("─", max(0, w-3-lipgloss.Width(title)))+"╮")
	out := []string{top2}
	for _, l := range lines {
		out = append(out, b.Render("│")+" "+fit(l, iw)+" "+b.Render("│"))
	}
	return append(out, b.Render("╰"+strings.Repeat("─", w-2)+"╯"))
}

// findList renders the results; the highlighted one's first line carries
// a marker for scrolling.
func (m Model) findList(w int, q string) []string {
	f := m.find
	s := m.styles
	words := strings.Fields(strings.ToLower(plainWords(q)))
	hl := func(text string, base lipgloss.Style) string {
		return highlight(text, words, base, s.accent.Bold(true))
	}
	var out []string
	if q == "" {
		return []string{"", s.muted.Render(" Try a name, a subject word, or Gmail search:"), "",
			s.text.Render("   from:priya") + s.muted.Render("        mail from Priya"),
			s.text.Render("   has:attachment") + s.muted.Render("    with files"),
			s.text.Render("   newer_than:7d") + s.muted.Render("     this week"),
			s.text.Render("   subject:invoice") + s.muted.Render("   in the subject"),
			s.text.Render("   in:sent") + s.muted.Render("           what you sent")}
	}
	if f.count() == 0 {
		msg := "Nothing here matches yet"
		if f.serverBusy {
			msg += "; Gmail is looking…"
		} else if f.serverQ == q {
			msg = "Nothing matches, here or on Gmail."
		}
		return []string{"", s.muted.Render(" " + msg)}
	}
	if len(f.mail) > 0 {
		out = append(out, s.muted.Bold(true).Render(fmt.Sprintf(" MAIL · %d", len(f.mail))))
	}
	for i, x := range f.mail {
		t := x.t
		sel := i == f.sel
		mark := "  "
		if sel {
			mark = s.accent.Bold(true).Render("▌ ")
		}
		who := t.Participants
		whoS := s.sub
		if t.Unread() {
			whoS = s.bold
		}
		right := s.muted.Render(cli.When(t.LastDate))
		if x.remote {
			right = s.blue.Render("☁ ") + right
		}
		l1 := mark + m.avatar(who) + " " + hl(ansi.Truncate(who, w/2, "…"), whoS)
		l2 := "     " + hl(t.Subject, s.text) + s.muted.Render(" — ") + hl(oneLine(t.Snippet), s.muted)
		if sel {
			l1 += "\x00sel"
			l2 = s.accent.Bold(true).Render("▌") + l2[1:]
		}
		out = append(out, row(l1, right, w), ansi.Truncate(l2, w, "…"))
	}
	if len(f.chat) > 0 {
		if len(out) > 0 {
			out = append(out, "")
		}
		out = append(out, s.muted.Bold(true).Render(fmt.Sprintf(" CHAT · %d", len(f.chat))))
	}
	for i, c := range f.chat {
		sel := len(f.mail)+i == f.sel
		mark := "  "
		if sel {
			mark = s.accent.Bold(true).Render("▌ ")
		}
		where := c.SpaceTitle
		if where == "" {
			where = "a chat"
		}
		pre := "# "
		if c.SpaceType == "DIRECT_MESSAGE" {
			pre = "@ "
		}
		name := c.SenderName
		if c.Mine {
			name = "You"
		}
		l1 := mark + m.avatar(name) + " " + hl(name, s.sub) + s.muted.Render(" in "+pre) + s.sub.Render(ansi.Truncate(where, w/3, "…"))
		l2 := "     " + hl(oneLine(c.Text), s.text)
		if sel {
			l1 += "\x00sel"
			l2 = s.accent.Bold(true).Render("▌") + l2[1:]
		}
		out = append(out, row(l1, s.muted.Render(cli.When(c.Created)), w), ansi.Truncate(l2, w, "…"))
	}
	return out
}

func (m Model) findPreview(w int) []string {
	f := m.find
	s := m.styles
	if f.count() == 0 {
		return nil
	}
	if f.sel < len(f.mail) {
		t := f.mail[f.sel].t
		mm := m // a copy: the preview of this thread, not the list's
		mm.msgs, _ = m.a.Store.Messages(t.Account, t.ID)
		mm.expanded, mm.links = map[string]bool{}, nil
		return mm.mailLines(t, w)
	}
	c := f.chat[f.sel-len(f.mail)]
	words := strings.Fields(strings.ToLower(plainWords(f.query())))
	out := []string{s.bold.Render(c.SpaceTitle), s.muted.Render(time.UnixMilli(c.Created).Format("Mon 02 Jan 15:04")), ""}
	out = append(out, m.avatar(c.SenderName)+" "+s.bold.Render(c.SenderName))
	for _, l := range wrap(mime.Clean(c.Text), w-2) {
		out = append(out, "  "+highlight(l, words, s.text, s.accent.Bold(true)))
	}
	return append(out, "", s.muted.Render("enter opens this chat"))
}

// highlight marks each word of the search in text.
func highlight(text string, words []string, base, mark lipgloss.Style) string {
	lower := strings.ToLower(text)
	if len(words) == 0 || len(lower) != len(text) {
		return base.Render(text)
	}
	var b strings.Builder
	i := 0
	for i < len(text) {
		hit := 0
		for _, w := range words {
			if w != "" && strings.HasPrefix(lower[i:], w) && len(w) > hit {
				hit = len(w)
			}
		}
		if hit > 0 {
			b.WriteString(mark.Render(text[i : i+hit]))
			i += hit
			continue
		}
		_, n := utf8.DecodeRuneInString(text[i:])
		j := i + n
		for j < len(text) && !startsAny(lower[j:], words) {
			_, n := utf8.DecodeRuneInString(text[j:])
			j += n
		}
		b.WriteString(base.Render(text[i:j]))
		i = j
	}
	return b.String()
}

func startsAny(s string, words []string) bool {
	for _, w := range words {
		if w != "" && strings.HasPrefix(s, w) {
			return true
		}
	}
	return false
}

// overlay draws box over base, centred near the top.
func overlay(base string, box []string, width int) string {
	lines := strings.Split(base, "\n")
	bw := 0
	for _, l := range box {
		bw = max(bw, lipgloss.Width(l))
	}
	x := max(0, (width-bw)/2)
	y := 2
	for i, l := range box {
		r := y + i
		if r >= len(lines) {
			break
		}
		left := ansi.Cut(lines[r], 0, x)
		left += strings.Repeat(" ", max(0, x-lipgloss.Width(left)))
		lines[r] = left + l + ansi.Cut(lines[r], x+lipgloss.Width(l), width)
	}
	return strings.Join(lines, "\n")
}
