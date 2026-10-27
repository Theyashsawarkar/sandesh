package tui

import (
	"context"
	"fmt"
	"maps"
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

// The search window (/): a floating panel that searches where you are.
// In a mail section it searches that section (Tab: all mail); in chat, the
// space you're on (Tab: every chat). Nothing runs on the screen's thread:
// the local copy is searched after a short pause in typing, Gmail after a
// longer one, previews are drawn in the background, and an answer to an
// older query is dropped.

type findScope int

const (
	scopeSection findScope = iota // this mail section
	scopeAllMail
	scopeSpace // this chat
	scopeAllChat
)

type finder struct {
	input      textinput.Model
	scope      findScope
	section    config.Section // the section you were in
	space      store.Space    // the chat you were in
	seq        int            // bumped on every change; stale answers are dropped
	mail       []findMail
	convos     []store.Space // chats whose name matches
	people     []person      // people to open a conversation with
	chat       []store.ChatHit
	sel        int
	searching  bool
	serverBusy bool
	serverQ    string // the query (and scope) Gmail last answered
	serverErr  string
	cancel     context.CancelFunc // the Gmail search in flight
	previews   map[string][]string
	wantPrev   string
}

type findMail struct {
	t      store.Thread
	remote bool // found by Gmail's search, not in the local copy before
}

type (
	findLocalTick  struct{ seq int }
	findServerTick struct{ seq int }
	findLocalMsg   struct {
		seq    int
		mail   []findMail
		convos []store.Space
		people []person
		chat   []store.ChatHit
	}
	findServerMsg struct {
		seq int
		q   string
		ts  []store.Thread
		err error
	}
	findPreviewMsg struct {
		key   string
		lines []string
	}
)

func (m *Model) openFinder() {
	in := newInput(m.styles)
	in.Focus()
	f := &finder{input: in, previews: map[string][]string{}}
	if m.tab == tabMail {
		f.scope, f.section = scopeSection, m.currentSection()
	} else if sp, ok := m.curSpace(); ok {
		f.scope, f.space = scopeSpace, sp
	} else {
		f.scope = scopeAllChat
	}
	m.find = f
	m.mode = modeFind
	f.placeholder()
}

func (f *finder) placeholder() {
	if f.mailScope() {
		f.input.Placeholder = "search mail · Gmail search works: from:priya has:attachment newer_than:7d"
	} else {
		f.input.Placeholder = "search messages · a word, or a name"
	}
}

func (f *finder) mailScope() bool { return f.scope == scopeSection || f.scope == scopeAllMail }

func (f *finder) query() string { return strings.TrimSpace(f.input.Value()) }

func (f *finder) count() int { return len(f.mail) + len(f.convos) + len(f.people) + len(f.chat) }

// at says what the i-th result is: mail, convo, person or chat, and its
// index in that list.
func (f *finder) at(i int) (string, int) {
	for _, g := range []struct {
		kind string
		n    int
	}{{"mail", len(f.mail)}, {"convo", len(f.convos)}, {"person", len(f.people)}, {"chat", len(f.chat)}} {
		if i < g.n {
			return g.kind, i
		}
		i -= g.n
	}
	return "", -1
}

func (f *finder) scopeName() string {
	switch f.scope {
	case scopeSection:
		return f.section.Name
	case scopeAllMail:
		return "all mail"
	case scopeSpace:
		return cli.SpaceTitle(f.space)
	}
	return "every chat"
}

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

func searchable(q string) bool {
	return len([]rune(plainWords(q))) >= 2 || strings.Contains(q, ":")
}

// findChanged starts over for a new query or scope: the local copy after
// 120 ms of quiet, Gmail after 600 ms.
func (m *Model) findChanged() tea.Cmd {
	f := m.find
	f.seq++
	f.sel = 0
	if f.cancel != nil {
		f.cancel()
		f.cancel = nil
	}
	f.serverBusy, f.serverErr = false, ""
	if !searchable(f.query()) {
		f.mail, f.convos, f.people, f.chat, f.searching = nil, nil, nil, nil, false
		return nil
	}
	f.searching = true
	seq := f.seq
	cmds := []tea.Cmd{tea.Tick(120*time.Millisecond, func(time.Time) tea.Msg { return findLocalTick{seq} })}
	if f.mailScope() {
		cmds = append(cmds, tea.Tick(600*time.Millisecond, func(time.Time) tea.Msg { return findServerTick{seq} }))
	}
	return tea.Batch(cmds...)
}

// findLocal searches the local copy, in the background.
func (m *Model) findLocal(seq int) tea.Cmd {
	f := m.find
	if f == nil || seq != f.seq {
		return nil
	}
	a, q, scope, sec, space := m.a, f.query(), f.scope, f.section, f.space
	accs := m.accountFilter()
	mm := *m
	return func() tea.Msg {
		words := plainWords(q)
		var mail []findMail
		var chat []store.ChatHit
		var convos []store.Space
		var people []person
		switch scope {
		case scopeSection, scopeAllMail:
			if words == "" {
				break // only Gmail operators: Gmail answers
			}
			tq := store.ThreadQuery{Accounts: accs, Text: words, Limit: 60}
			if scope == scopeSection {
				tq.Label = sec.Label
				if sec.Query != "" {
					tq.Section = sec.Name
				}
				if sec.Today {
					y, mo, d := time.Now().Date()
					tq.Since = time.Date(y, mo, d, 0, 0, 0, 0, time.Local).UnixMilli()
				}
			}
			ts, _ := a.Store.Threads(tq)
			for _, t := range ts {
				if !t.Has("TRASH") && !t.Has("SPAM") || sec.Label == "TRASH" || sec.Label == "SPAM" {
					mail = append(mail, findMail{t: t})
				}
			}
		case scopeSpace:
			chat, _ = a.Store.SearchChat(nil, words, 80, space.Name)
		default:
			chat, _ = a.Store.SearchChat(accs, words, 80, "")
			// conversations by name, and people to talk to
			lw := strings.ToLower(words)
			sps, _ := a.Store.Spaces(accs, nil)
			for _, sp := range sps {
				if len(convos) < 6 && strings.Contains(strings.ToLower(cli.SpaceTitle(sp)), lw) {
					convos = append(convos, sp)
				}
			}
			acc := mm.defaultAccount()
			for _, p := range mm.people(acc, words, 5, nil) {
				people = append(people, p)
			}
		}
		return findLocalMsg{seq: seq, mail: mail, convos: convos, people: people, chat: chat}
	}
}

func (m *Model) gotFindLocal(msg findLocalMsg) tea.Cmd {
	f := m.find
	if f == nil || msg.seq != f.seq {
		return nil
	}
	f.searching = false
	// Gmail's answers to this same query stay
	seen := map[string]bool{}
	for _, x := range msg.mail {
		seen[x.t.Account+"/"+x.t.ID] = true
	}
	for _, x := range f.mail {
		if x.remote && !seen[x.t.Account+"/"+x.t.ID] {
			msg.mail = append(msg.mail, x)
		}
	}
	sortMail(msg.mail)
	f.mail, f.convos, f.people, f.chat = msg.mail, msg.convos, msg.people, msg.chat
	f.sel = min(f.sel, max(0, f.count()-1))
	return m.wantPreview()
}

func sortMail(x []findMail) {
	sort.SliceStable(x, func(i, j int) bool { return x[i].t.LastDate > x[j].t.LastDate })
}

// gmailScope turns the section into a Gmail search and labels.
func gmailScope(sec config.Section, q string) (string, []string) {
	switch {
	case sec.Today:
		q += " after:" + time.Now().Format("2006/01/02")
	case sec.Query != "":
		q = "(" + sec.Query + ") " + q
	}
	if sec.Label != "" {
		return q, []string{sec.Label}
	}
	return q, nil
}

func (m *Model) findServer(seq int) tea.Cmd {
	f := m.find
	if f == nil || seq != f.seq || !f.mailScope() {
		return nil
	}
	q := f.query()
	key := fmt.Sprint(f.scope) + "\x00" + q
	if f.serverQ == key {
		return nil
	}
	gq, labels := q, []string(nil)
	if f.scope == scopeSection {
		gq, labels = gmailScope(f.section, q)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	f.cancel = cancel
	f.serverBusy, f.serverErr = true, ""
	a := m.a
	accs := m.accountFilter()
	if len(accs) == 0 {
		accs = m.accounts
	}
	return func() tea.Msg {
		defer cancel()
		var all []store.Thread
		for _, acc := range accs {
			ts, err := a.SearchServer(ctx, acc, gq, 20, labels...)
			if err != nil {
				return findServerMsg{seq, key, all, err}
			}
			all = append(all, ts...)
		}
		return findServerMsg{seq, key, all, nil}
	}
}

func (m *Model) gotFindServer(msg findServerMsg) tea.Cmd {
	f := m.find
	if f == nil || msg.seq != f.seq {
		return nil
	}
	f.serverBusy, f.cancel = false, nil
	if msg.err != nil {
		if msg.err != context.Canceled {
			f.serverErr = msg.err.Error()
		}
	} else {
		f.serverQ = msg.q
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
	return m.wantPreview()
}

// wantPreview draws the highlighted result's preview in the background
// (once: they're kept while the window is open).
func (m *Model) wantPreview() tea.Cmd {
	f := m.find
	if f == nil || f.count() == 0 {
		return nil
	}
	w := m.findPrevWidth()
	if f.sel < len(f.mail) {
		t := f.mail[f.sel].t
		key := "m/" + t.Account + "/" + t.ID
		f.wantPrev = key
		if _, ok := f.previews[key]; ok {
			return nil
		}
		mm := *m // a copy, drawn away from the screen
		mm.links, mm.expanded, mm.expandAll, mm.showQuoted = nil, map[string]bool{}, false, false
		mm.labelNames = maps.Clone(m.labelNames) // its own: it caches into it
		return func() tea.Msg {
			mm.msgs, _ = mm.a.Store.Messages(t.Account, t.ID)
			return findPreviewMsg{key, mm.mailLines(t, w)}
		}
	}
	kind, i := f.at(f.sel)
	mm := *m
	s := m.styles
	switch kind {
	case "convo":
		sp := f.convos[i]
		key := "s/" + sp.Account + "/" + sp.Name
		f.wantPrev = key
		if _, ok := f.previews[key]; ok {
			return nil
		}
		return func() tea.Msg {
			msgs, _ := mm.a.Store.ChatMessages(sp.Account, sp.Name, 8)
			out := []string{s.bold.Render(cli.SpaceTitle(sp)), s.muted.Render("the latest messages"), ""}
			for j := len(msgs) - 1; j >= 0; j-- {
				c := msgs[j]
				name := c.SenderName
				if c.Mine {
					name = "You"
				}
				out = append(out, s.muted.Render("│ ")+s.bold.Render(name)+s.muted.Render(" "+cli.When(c.Created)))
				for _, l := range wrap(mime.Clean(c.Text), w-3) {
					out = append(out, s.muted.Render("│ ")+s.sub.Render(l))
				}
			}
			return findPreviewMsg{key, append(out, "", s.muted.Render("enter opens it"))}
		}
	case "person":
		p := f.people[i]
		key := "p/" + p.key()
		f.wantPrev = key
		out := []string{m.avatar(p.label()) + " " + s.bold.Render(p.label())}
		if p.Email != "" {
			out = append(out, s.muted.Render("   "+p.Email))
		}
		out = append(out, "", s.text.Render("Enter opens your conversation with "+firstWord(p.label())+","),
			s.text.Render("with what you said before, or starts one."))
		f.previews[key] = out
		return nil
	}
	hit := f.chat[i]
	key := "c/" + hit.Name
	f.wantPrev = key
	if _, ok := f.previews[key]; ok {
		return nil
	}
	words := strings.Fields(strings.ToLower(plainWords(f.query())))
	return func() tea.Msg { return findPreviewMsg{key, mm.chatContext(hit, w, words)} }
}

// chatContext shows a found message with a few around it.
func (m Model) chatContext(hit store.ChatHit, w int, words []string) []string {
	s := m.styles
	msgs, _ := m.a.Store.ChatMessages(hit.Account, hit.Space, 400)
	at := -1
	for i, c := range msgs {
		if c.Name == hit.Name {
			at = i
		}
	}
	out := []string{s.bold.Render(hit.SpaceTitle), s.muted.Render(time.UnixMilli(hit.Created).Format("Mon 02 Jan 2006, 15:04")), ""}
	if at < 0 {
		return out
	}
	from, to := max(0, at-4), min(len(msgs), at+5)
	for i := from; i < to; i++ {
		c := msgs[i]
		name := c.SenderName
		if c.Mine {
			name = "You"
		}
		bar := s.muted.Render("│")
		textS := s.sub
		if i == at {
			bar, textS = s.accent.Bold(true).Render("┃"), s.text
		}
		out = append(out, bar+" "+s.bold.Render(name)+s.muted.Render(" "+time.UnixMilli(c.Created).Format("15:04")))
		for _, l := range wrap(mime.Clean(c.Text), w-3) {
			out = append(out, bar+" "+highlight(l, words, textS, s.accent.Bold(true)))
		}
	}
	return append(out, "", s.muted.Render("enter opens the chat at this message"))
}

func (m *Model) gotFindPreview(msg findPreviewMsg) {
	if f := m.find; f != nil {
		f.previews[msg.key] = msg.lines
	}
}

func (m Model) finderKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	f := m.find
	switch k.String() {
	case "esc", "ctrl+c":
		if f.cancel != nil {
			f.cancel()
		}
		m.find, m.mode = nil, modeNormal
		return m, nil
	case "up", "ctrl+p", "ctrl+k":
		f.sel = max(0, f.sel-1)
		return m, m.wantPreview()
	case "down", "ctrl+n", "ctrl+j":
		f.sel = min(max(0, f.count()-1), f.sel+1)
		return m, m.wantPreview()
	case "tab":
		// wider or narrower: this section ↔ all mail, this chat ↔ every chat
		switch f.scope {
		case scopeSection:
			f.scope = scopeAllMail
		case scopeAllMail:
			f.scope = scopeSection
		case scopeSpace:
			f.scope = scopeAllChat
		case scopeAllChat:
			if f.space.Name != "" {
				f.scope = scopeSpace
			}
		}
		f.mail, f.chat = nil, nil
		return m, m.findChanged()
	case "ctrl+u":
		f.input.SetValue("")
		return m, m.findChanged()
	case "enter":
		return m.openFound()
	}
	var cmd tea.Cmd
	before := f.query()
	f.input, cmd = f.input.Update(k)
	if f.query() != before {
		return m, tea.Batch(cmd, m.findChanged())
	}
	return m, cmd
}

// openFound shows the chosen result: a mail thread in a search section
// holding every mail result, or the chat at that message.
func (m Model) openFound() (tea.Model, tea.Cmd) {
	f := m.find
	q := f.query()
	if f.count() == 0 {
		return m, nil
	}
	if f.cancel != nil {
		f.cancel()
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
		m.say(fmt.Sprintf("%d found in %s · W saves it as a section · h/l leaves", len(f.mail), f.scopeName()))
		return m, nil
	}
	kind, i := f.at(f.sel)
	switch kind {
	case "convo":
		m.showSpace(f.convos[i])
		return m, nil
	case "person":
		m.say("opening the conversation…")
		return m, m.conversationCmd(m.defaultAccount(), []person{f.people[i]})
	}
	hit := f.chat[i]
	m.tab = tabChat
	m.search, m.filter = nil, ""
	m.sec = 0 // All
	m.reload()
	m.flashMsg = hit.Name
	m.selectKey(hit.Account + "/" + hit.Space)
	// scroll to the message (marked in the conversation)
	if line, ok := m.chatMsgLine[hit.Name]; ok {
		m.scroll = max(0, line-3)
		m.fixView()
	}
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

func (m Model) findPopupWidth() int { return min(m.w-4, 156) }

func (m Model) findPrevWidth() int {
	iw := m.findPopupWidth() - 4
	return iw - max(40, iw*46/100) - 3
}

func (m Model) finderView() []string {
	f := m.find
	s := m.styles
	w := m.findPopupWidth()
	h := min(m.bodyHeight()-2, 44)
	iw := w - 4
	q := f.query()

	var status string
	switch {
	case f.searching:
		status = s.accent.Render("⟳ searching…")
	case f.serverBusy:
		status = s.accent.Render("⟳ asking Gmail…")
	case f.serverErr != "":
		status = s.red.Render("Gmail: " + ansi.Truncate(f.serverErr, 30, "…"))
	case f.mailScope() && f.serverQ == fmt.Sprint(f.scope)+"\x00"+q && q != "":
		status = s.green.Render(fmt.Sprintf("✓ %d · here + Gmail", f.count()))
	case searchable(q):
		status = s.muted.Render(fmt.Sprintf("%d found", f.count()))
	}
	in := f.input
	in.Width = iw - lipgloss.Width(status) - 6
	var lines []string
	lines = append(lines, row(s.accent.Bold(true).Render(" ")+in.View(), status, iw))
	scopeLine := s.muted.Render(" in ") + s.round(s.pill, f.scopeName())
	switch f.scope {
	case scopeSection:
		scopeLine += s.muted.Render("   tab: all mail")
	case scopeAllMail:
		scopeLine += s.muted.Render("   tab: only " + f.section.Name)
	case scopeSpace:
		scopeLine += s.muted.Render("   tab: every chat")
	case scopeAllChat:
		if f.space.Name != "" {
			scopeLine += s.muted.Render("   tab: only " + cli.SpaceTitle(f.space))
		}
	}
	lines = append(lines, scopeLine, s.muted.Render(strings.Repeat("─", iw)))

	listW := max(40, iw*46/100)
	prevW := iw - listW - 3
	bodyH := h - 2 - len(lines) - 2
	list := m.findList(listW, q)
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
	var prev []string
	if f.count() > 0 {
		prev = f.previews[f.wantPrev]
		if prev == nil {
			prev = []string{s.muted.Render("…")}
		}
	}
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
	lines = append(lines, s.muted.Render(" ↑↓ move · enter opens · tab looks wider or narrower · esc closes"))
	return m.popupFrame("search", lines, w)
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
	if !searchable(q) {
		if f.mailScope() {
			return []string{"", s.muted.Render(" A name, a subject word, or Gmail search:"), "",
				s.text.Render("   from:priya") + s.muted.Render("        mail from Priya"),
				s.text.Render("   has:attachment") + s.muted.Render("    with files"),
				s.text.Render("   newer_than:7d") + s.muted.Render("     this week"),
				s.text.Render("   subject:invoice") + s.muted.Render("   in the subject")}
		}
		return []string{"", s.muted.Render(" A word from a message, or someone's name")}
	}
	if f.count() == 0 {
		msg := "Nothing matches"
		switch {
		case f.searching:
			msg = "Looking…"
		case f.serverBusy:
			msg = "Nothing here yet; Gmail is looking…"
		case f.scope == scopeSection || f.scope == scopeSpace:
			msg = "Nothing matches here. Tab looks wider."
		}
		return []string{"", s.muted.Render(" " + msg)}
	}
	var out []string
	for i, x := range f.mail {
		t := x.t
		sel := i == f.sel
		mark := "  "
		if sel {
			mark = s.accent.Bold(true).Render("▌ ")
		}
		whoS := s.sub
		if t.Unread() {
			whoS = s.bold
		}
		right := s.muted.Render(cli.When(t.LastDate))
		if x.remote {
			right = s.blue.Render("☁ ") + right
		}
		l1 := mark + m.avatar(t.Participants) + " " + hl(ansi.Truncate(t.Participants, w/2, "…"), whoS)
		l2 := "  " + m.indent() + hl(t.Subject, s.text) + s.muted.Render(" — ") + hl(oneLine(t.Snippet), s.muted)
		if sel {
			l1 += "\x00sel"
			l2 = s.accent.Bold(true).Render("▌") + l2[1:]
		}
		out = append(out, row(l1, right, w), ansi.Truncate(l2, w, "…"))
	}
	base := len(f.mail)
	group := func(title string, n int) {
		if n > 0 {
			if len(out) > 0 {
				out = append(out, "")
			}
			out = append(out, s.muted.Bold(true).Render(fmt.Sprintf(" %s · %d", title, n)))
		}
	}
	group("CONVERSATIONS", len(f.convos))
	for i, sp := range f.convos {
		sel := base+i == f.sel
		mark := "  "
		if sel {
			mark = s.accent.Bold(true).Render("▌ ")
		}
		pre := "# "
		if sp.Type == "DIRECT_MESSAGE" {
			pre = "@ "
		}
		l1 := mark + m.avatar(cli.SpaceTitle(sp)) + " " + s.muted.Render(pre) + hl(cli.SpaceTitle(sp), s.text)
		if sel {
			l1 += "\x00sel"
		}
		out = append(out, row(l1, s.muted.Render(cli.When(sp.LastActive)), w))
	}
	base += len(f.convos)
	group("PEOPLE", len(f.people))
	for i, p := range f.people {
		sel := base+i == f.sel
		mark := "  "
		if sel {
			mark = s.accent.Bold(true).Render("▌ ")
		}
		l1 := ansi.Truncate(mark+m.avatar(p.label())+" "+hl(p.label(), s.text)+"  "+s.muted.Render(p.Email), w, "…")
		if sel {
			l1 += "\x00sel"
		}
		out = append(out, l1)
	}
	base += len(f.people)
	if len(f.chat) > 0 && (len(f.convos) > 0 || len(f.people) > 0) {
		group("MESSAGES", len(f.chat))
	}
	for i, c := range f.chat {
		sel := base+i == f.sel
		mark := "  "
		if sel {
			mark = s.accent.Bold(true).Render("▌ ")
		}
		name := c.SenderName
		if c.Mine {
			name = "You"
		}
		l1 := mark + m.avatar(name) + " " + hl(name, s.sub)
		if f.scope == scopeAllChat {
			where := c.SpaceTitle
			if where == "" {
				where = "a chat"
			}
			l1 += s.muted.Render(" in ") + s.sub.Render(ansi.Truncate(where, w/3, "…"))
		}
		l2 := "  " + m.indent() + hl(oneLine(c.Text), s.text)
		if sel {
			l1 += "\x00sel"
			l2 = s.accent.Bold(true).Render("▌") + l2[1:]
		}
		out = append(out, row(l1, s.muted.Render(cli.When(c.Created)), w), ansi.Truncate(l2, w, "…"))
	}
	return out
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

// overlay draws box over base, near the top.
func overlay(base string, box []string, width int) string { return overlayAt(base, box, width, 1) }
