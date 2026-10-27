package tui

import (
	"fmt"
	"github.com/Theyashsawarkar/sandesh/internal/app"
	"regexp"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/Theyashsawarkar/sandesh/internal/cli"
	"github.com/Theyashsawarkar/sandesh/internal/mime"
	"github.com/Theyashsawarkar/sandesh/internal/render"
	"github.com/Theyashsawarkar/sandesh/internal/store"
)

// Card heights: a mail card is 5 rows, a chat card 4, plus a blank row.
const (
	mailCardRows = 6
	chatCardRows = 5
)

func (m Model) cardRows() int {
	if m.tab == tabMail {
		return mailCardRows
	}
	return chatCardRows
}

func fg(c string) lipgloss.Style { return lipgloss.NewStyle().Foreground(lipgloss.Color(c)) }

// fit pads or cuts a styled string to exactly w cells.
func fit(s string, w int) string {
	s = ansi.Truncate(s, w, "…")
	return s + strings.Repeat(" ", max(0, w-lipgloss.Width(s)))
}

// cardBox draws a card of inner width w around lines: rounded in the
// border colour, or heavy in the accent when selected. No background
// fills: they turn muddy on translucent terminals. dot marks it unread.
func (m Model) cardBox(lines []string, w int, border string, sel, dot bool) []string {
	s := m.styles
	b := fg(border)
	tl, tr, bl, br, hz, vt := "╭", "╮", "╰", "╯", "─", "│"
	if sel {
		b = b.Bold(true)
		vt = "┃" // rounded corners still; heavier sides mark the selection
	}
	out := []string{b.Render(tl + strings.Repeat(hz, w+2) + tr)}
	for i, l := range lines {
		left := b.Render(vt)
		if dot && i == 0 {
			left = s.accent.Bold(true).Render("●")
		}
		out = append(out, left+" "+fit(l, w)+" "+b.Render(vt))
	}
	return append(out, b.Render(bl+strings.Repeat(hz, w+2)+br))
}

func rgb(hex string) string {
	hex = strings.TrimPrefix(hex, "#")
	var r, g, bl int
	if len(hex) != 6 {
		return "49;50;68"
	}
	fmt.Sscanf(hex, "%02x%02x%02x", &r, &g, &bl)
	return fmt.Sprintf("%d;%d;%d", r, g, bl)
}

func row(left, right string, w int) string {
	gap := w - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		left = ansi.Truncate(left, max(1, w-lipgloss.Width(right)-1), "…")
		gap = max(1, w-lipgloss.Width(left)-lipgloss.Width(right))
	}
	return left + strings.Repeat(" ", gap) + right
}

// avatarColors are the theme's bright colours, picked per name.
func (s styles) avatarColors() []string {
	c := s.c
	return []string{c.Accent2, c.Peach, c.Green, c.Blue, c.Accent, c.Yellow, c.Red}
}

// avatar is two initials on a colour picked from the name.
// sentChip says whether a sent mail got an answer.
func (m Model) sentChip(st store.SentStatus) string {
	s := m.styles
	if st.ReplyFrom != "" {
		return m.chip("↩ "+firstWord(st.ReplyFrom)+" replied "+cli.When(st.ReplyAt), s.c.Green)
	}
	d := time.Since(time.UnixMilli(st.SentAt))
	wait := "today"
	if d >= 24*time.Hour {
		wait = fmt.Sprintf("%dd", int(d.Hours()/24))
	}
	return m.chip("no reply yet · "+wait, s.c.Yellow)
}

func (m Model) outboxStatus(it store.OutboxItem) (string, string) {
	s := m.styles
	switch it.Status {
	case store.StatusQueued:
		if it.Out.ScheduledFor > 0 {
			return "⏰ " + app.SendTime(time.UnixMilli(it.Out.ScheduledFor), time.Now()), s.c.Blue
		}
		return fmt.Sprintf("sending in %ds", max(0, int(time.Until(time.UnixMilli(it.SendAt)).Seconds()+0.5))), s.c.Yellow
	case store.StatusSending:
		return "sending…", s.c.Yellow
	case store.StatusApproval:
		return "waiting for your OK", s.c.Peach
	}
	return "failed", s.c.Red
}

// toNames shortens a recipient list: "Priya, Arjun, Neha +2", with names
// from your contacts where the addresses have none.
func (m Model) toNames(account, list string) string {
	var names []string
	for _, a := range mime.ParseList(list) {
		r := m.recipFor(account, a)
		name := r.Name
		if name == "" {
			name = strings.Split(r.Addr, "@")[0]
		}
		names = append(names, firstWord(name))
	}
	if len(names) > 3 {
		return strings.Join(names[:3], ", ") + fmt.Sprintf(" +%d", len(names)-3)
	}
	return strings.Join(names, ", ")
}

func firstWord(s string) string {
	if f := strings.Fields(s); len(f) > 0 {
		return strings.Trim(f[0], `"',`)
	}
	return s
}

func (m Model) avatar(name string) string {
	h := hash(name)
	var ini []rune
	for _, w := range strings.FieldsFunc(name, func(r rune) bool { return r == ' ' || r == '.' || r == '@' || r == '_' || r == ',' || r == '+' }) {
		ini = append(ini, []rune(strings.ToUpper(w))[0])
		if len(ini) == 2 {
			break
		}
	}
	if len(ini) == 1 { // one word: its first two letters
		if r := []rune(strings.ToUpper(strings.TrimSpace(name))); len(r) > 1 && r[1] != ' ' {
			ini = append(ini, r[1])
		}
	}
	for len(ini) < 2 {
		ini = append(ini, ' ')
	}
	ac := m.styles.avatarColors()
	col := lipgloss.Color(ac[h%len(ac)])
	face := lipgloss.NewStyle().Background(col).Foreground(lipgloss.Color(m.styles.c.Base)).Bold(true).Render(string(ini))
	if !m.a.Config.NerdFont {
		return face
	}
	// round ends (Nerd Font half circles) make it a pill-shaped circle
	cap := lipgloss.NewStyle().Foreground(col)
	return cap.Render("\ue0b6") + face + cap.Render("\ue0b4")
}

// indent lines up text under a name that follows an avatar.
func (m Model) indent() string { return strings.Repeat(" ", lipgloss.Width(m.avatar("x"))+1) }

func (m Model) chip(text, color string) string {
	return fg(color).Render("‹" + text + "›")
}

// ------------------------------------------------------------------- list

func (m *Model) listView(w, h int) string {
	s := m.styles
	if m.count() == 0 {
		msg := "Nothing here."
		switch {
		case m.tab == tabMail && m.currentSection().Label == "SCHEDULED":
			msg = "Nothing scheduled. Ctrl+t in a mail sends it later."
		case m.tab == tabChat && chatSections[m.sec%len(chatSections)] == "Scheduled":
			msg = "Nothing scheduled. Ctrl+t while typing a message (i) sends it later."
		}
		if m.filter != "" {
			msg = "Nothing matches /" + m.filter + " (Esc clears)"
		} else if m.syncing {
			msg = "Syncing…"
		}
		return "\n " + s.muted.Render(msg)
	}
	cw := w - 6 // card inner width: margins, borders and padding on both sides
	var out []string
	visible := max(1, (h-1)/m.cardRows())
	for i := m.offset; i < min(m.count(), m.offset+visible); i++ {
		sel := i == m.sel
		border := s.c.Border
		if sel && m.focus == 0 {
			border = s.c.Accent
		} else if sel {
			border = s.c.Muted
		}
		var lines []string
		dot := false
		if m.tab == tabMail {
			t := m.threads[i]
			picked := m.selected[t.Account+"/"+t.ID]
			if picked {
				border = s.c.Accent2
			}
			dot = t.Unread()
			who := s.sub.Render(t.Participants)
			subj := s.sub.Render(t.Subject)
			if t.Unread() {
				who, subj = s.bold.Render(t.Participants), s.text.Render(t.Subject)
			}
			if it, ok := m.outboxRows[t.ID]; ok {
				// still in the outbox: who it's for, and what's happening
				status, col := m.outboxStatus(it)
				if !sel {
					border = col
				}
				to := "→ " + m.toNames(it.Account, strings.Join(it.Out.To, ", "))
				if it.Kind == "chat" {
					to = "→ # " + it.Out.SpaceName
				}
				hint := map[string]string{store.StatusQueued: "z cancels", store.StatusFailed: "R sends it again · ⏎ edits",
					store.StatusApproval: "A answers it", store.StatusSending: "…"}[it.Status]
				if it.Status == store.StatusQueued && it.Out.ScheduledFor > 0 {
					hint = "t moves · ⏎ edits · z cancels"
				}
				lines = []string{row(s.text.Render(to), fg(col).Render(status), cw), s.text.Render(t.Subject),
					ansi.Truncate(fg(col).Render(hint)+s.muted.Render("  "+oneLine(it.Out.Body)), cw, "…")}
				for _, l := range m.cardBox(lines, cw, border, sel, false) {
					out = append(out, " "+l)
				}
				out = append(out, "")
				continue
			}
			sentSt, isSent := m.sentStat[t.Account+"/"+t.ID]
			if isSent {
				who = s.sub.Render("→ " + m.toNames(t.Account, sentSt.To))
			}
			if picked {
				who = s.accent2.Render("✓ ") + who
			}
			if t.Starred() {
				who += s.yellow.Render(" ★")
			}
			var chips []string
			if t.Count > 1 {
				chips = append(chips, m.chip(fmt.Sprintf("%d msgs", t.Count), s.c.Muted))
			}
			for _, l := range userLabels(t.Labels) {
				chips = append(chips, m.chip(l, s.c.Accent2))
			}
			if len(m.accounts) > 1 && m.acct < 0 {
				chips = append(chips, m.chip(t.Account, s.c.Blue))
			}
			if isSent {
				chips = append([]string{m.sentChip(sentSt)}, chips...)
			}
			chipStr := strings.Join(chips, " ")
			snipW := cw - lipgloss.Width(chipStr) - 1
			snip := ansi.Truncate(s.muted.Render(oneLine(t.Snippet)), max(4, snipW), "…")
			lines = []string{row(who, s.muted.Render(cli.When(t.LastDate)), cw), subj, row(snip, chipStr, cw)}
		} else {
			sp := m.spaces[i]
			if it, ok := m.outboxRows[sp.Name]; ok {
				// a scheduled message: where it goes, when, what
				status, col := m.outboxStatus(it)
				if !sel {
					border = col
				}
				lines = []string{row(m.avatar(sp.DisplayName)+" "+s.text.Render("→ "+sp.DisplayName), fg(col).Render(status), cw),
					m.indent() + ansi.Truncate(s.muted.Render(sp.LastText), cw-lipgloss.Width(m.indent()), "…")}
				for _, l := range m.cardBox(lines, cw, border, sel, false) {
					out = append(out, " "+l)
				}
				out = append(out, "")
				continue
			}
			dot = sp.Unread > 0
			name := cli.SpaceTitle(sp)
			mark := s.muted.Render("# ")
			if sp.Type == "DIRECT_MESSAGE" {
				mark = s.muted.Render("@ ")
			}
			title := s.sub.Render(name)
			if sp.Unread > 0 {
				title = s.bold.Render(name) + s.peach.Render(fmt.Sprintf(" %d", sp.Unread))
			}
			last := oneLine(sp.LastText)
			if sp.LastSender != "" {
				last = strings.SplitN(sp.LastSender, " ", 2)[0] + ": " + last
			}
			lines = []string{row(m.avatar(name)+" "+mark+title, s.muted.Render(cli.When(sp.LastActive)), cw), m.indent() + s.muted.Render(last)}
		}
		for _, l := range m.cardBox(lines, cw, border, sel, dot) {
			out = append(out, " "+l)
		}
		out = append(out, "")
	}
	// the end of a mail list: older mail comes from Gmail on demand
	if m.tab == tabMail && m.offset+visible >= m.count() && !m.currentSection().Today && m.currentSection().Label != "SCHEDULED" {
		sec := m.currentSection().Name
		var hint string
		switch {
		case m.loadingOlder:
			hint = s.accent.Render(fmt.Sprintf("  ⟳ loading %d older…", m.olderN))
		case m.noOlder[sec]:
			hint = s.muted.Render("  ✓ that's everything in " + sec)
		default:
			hint = s.muted.Render(fmt.Sprintf("  ↓ j at the end loads %d older", m.olderN)) + s.muted.Render(" · + / - for more or fewer")
		}
		if len(out) > 0 && out[len(out)-1] == "" {
			out = out[:len(out)-1]
		}
		out = append(out, "", ansi.Truncate(hint, w, "…"))
	}
	return strings.Join(out, "\n")
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

func userLabels(labels []string) []string {
	var out []string
	for _, l := range labels {
		switch {
		case l == "INBOX", l == "UNREAD", l == "STARRED", l == "SENT", l == "IMPORTANT", l == "DRAFT", strings.HasPrefix(l, "CATEGORY_"):
		default:
			out = append(out, strings.TrimPrefix(l, "Label_"))
		}
	}
	return out
}

// ---------------------------------------------------------------- preview

func (m Model) renderStyles() render.Styles {
	s := m.styles
	return render.Styles{
		Text: s.text, Bold: s.bold, Italic: s.text.Italic(true), Heading: fg(s.c.Accent).Bold(true),
		Link: fg(s.c.Blue).Underline(true), Quote: s.muted, Muted: s.muted, Button: fg(s.c.Accent).Bold(true),
		Code: fg(s.c.Peach), TableHead: fg(s.c.Accent).Bold(true), Border: fg(s.c.Border),
	}
}

// labelNames maps label ids to names for the chips.
func (m *Model) labelName(account, id string) string {
	if m.labelNames == nil {
		m.labelNames = map[string]string{}
	}
	key := account + "/" + id
	if n, ok := m.labelNames[key]; ok {
		return n
	}
	name := strings.TrimPrefix(id, "Label_")
	if ls, err := m.a.Store.Labels(account); err == nil {
		for _, l := range ls {
			m.labelNames[account+"/"+l.ID] = l.Name
			if l.ID == id {
				name = l.Name
			}
		}
	}
	m.labelNames[key] = name
	return name
}

// buildPreview renders the selected thread or space at width w into
// m.preview (and m.links), so View only slices it.
func (m *Model) buildPreview(w int) {
	m.preview, m.links = nil, nil
	if t, ok := m.curThread(); ok {
		m.preview = m.mailLines(t, w)
	} else if it, ok := m.curOutbox(); ok {
		note := map[string]string{store.StatusQueued: "in its undo window · z cancels", store.StatusSending: "sending now",
			store.StatusApproval: "an agent's mail waiting for your approval (A)", store.StatusFailed: "failed: " + it.Error + " · R sends it again"}[it.Status]
		if it.Status == store.StatusQueued && it.Out.ScheduledFor > 0 {
			note = "⏰ scheduled for " + time.UnixMilli(it.Out.ScheduledFor).Format("Monday 02 January, 15:04") + " · t moves it · enter edits · z cancels"
		}
		m.preview = strings.Split(m.outgoingView(it.Kind, it.Out, note, w), "\n")
	} else if _, ok := m.curSpace(); ok {
		m.preview = m.chatLines(w)
	}
}

func (m *Model) mailLines(t store.Thread, w int) []string {
	s := m.styles
	var out []string
	out = append(out, wrapStyled(fg(s.c.Text).Bold(true), t.Subject, w)...)
	var chips []string
	if t.Unread() {
		chips = append(chips, m.chip("unread", s.c.Accent))
	}
	if t.Has("INBOX") {
		chips = append(chips, m.chip("Inbox", s.c.Muted))
	}
	for _, l := range userLabels(t.Labels) {
		chips = append(chips, m.chip(m.labelName(t.Account, l), s.c.Accent2))
	}
	if t.Starred() {
		chips = append(chips, m.chip("★ starred", s.c.Yellow))
	}
	if st, ok := m.sentStat[t.Account+"/"+t.ID]; ok {
		chips = append(chips, m.sentChip(st))
	}
	chips = append(chips, m.chip(fmt.Sprintf("%d message%s", len(m.msgs), map[bool]string{true: "s"}[len(m.msgs) != 1]), s.c.Muted))
	out = append(out, strings.Join(chips, " "), "")

	inner := w - 4
	rs := m.renderStyles()
	for i, msg := range m.msgs {
		last := i == len(m.msgs)-1
		open := last || m.expanded[msg.ID] || m.expandAll || has(msg.Labels, "UNREAD")
		from := msg.FromName
		if from == "" {
			from = msg.FromAddr
		}
		date := time.UnixMilli(msg.Date).Format("Mon 02 Jan, 15:04")
		head := row(m.avatar(from)+" "+s.bold.Render(from)+s.muted.Render(" <"+msg.FromAddr+">"), s.muted.Render(date), inner)
		border := s.c.Border
		if !open {
			lines := []string{head, m.indent() + ansi.Truncate(s.muted.Render(oneLine(msg.Body)), inner-lipgloss.Width(m.indent()), "…")}
			out = append(out, m.cardBox(lines, inner, border, false, false)...)
			out = append(out, "")
			continue
		}
		var lines []string
		lines = append(lines, head)
		to := "to " + shortAddrs(msg.To)
		if msg.Cc != "" {
			to += " · cc " + shortAddrs(msg.Cc)
		}
		lines = append(lines, m.indent()+s.muted.Render(to))
		sep := len(lines)
		var res render.Result
		if strings.TrimSpace(msg.HTML) != "" {
			res = render.HTML(msg.HTML, inner, rs, m.showQuoted)
		} else {
			res = render.Text(msg.Body, inner, rs, m.showQuoted)
		}
		// renumber this message's links after the earlier messages' ones
		base := len(m.links)
		body := res.Lines
		if base > 0 && len(res.Links) > 0 {
			body = renumber(body, base)
		}
		lines = append(lines, body...)
		if len(msg.Attachments) > 0 {
			lines = append(lines, "")
			for _, at := range msg.Attachments {
				lines = append(lines, s.peach.Render("📎 "+at.Name)+s.muted.Render(" · "+sizeOf(at.Size)))
			}
			lines = append(lines, s.muted.Render("   v saves · V opens"))
		}
		if len(res.Links) > 0 {
			lines = append(lines, "")
			linkStart := len(lines)
			for j, l := range res.Links {
				lines = append(lines, s.muted.Render(fmt.Sprintf("[%d] ", base+j+1))+ansi.Truncate(fg(s.c.Blue).Render(l), inner-6, "…"))
			}
			m.links = append(m.links, res.Links...)
			_ = linkStart
		}
		box := m.cardBox(lines, inner, border, false, false)
		// a rule between the header and the body
		rule := fg(border).Render("├" + strings.Repeat("─", inner+2) + "┤")
		box = append(box[:sep+1], append([]string{rule}, box[sep+1:]...)...)
		out = append(out, box...)
		out = append(out, "")
	}
	return out
}

var reLinkNum = regexp.MustCompile(`\[(\d+)\]`)

// renumber shifts [n] link markers by base (several messages, one list).
func renumber(lines []string, base int) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = reLinkNum.ReplaceAllStringFunc(l, func(s string) string {
			var n int
			fmt.Sscanf(s, "[%d]", &n)
			return fmt.Sprintf("[%d]", n+base)
		})
	}
	return out
}

func shortAddrs(list string) string {
	var out []string
	for _, p := range strings.Split(list, ",") {
		p = strings.TrimSpace(p)
		if i := strings.Index(p, "<"); i > 0 {
			p = strings.Trim(strings.TrimSpace(p[:i]), `"`)
		}
		if p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, ", ")
}

func has(l []string, x string) bool {
	for _, y := range l {
		if y == x {
			return true
		}
	}
	return false
}

func sizeOf(n int) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%d KB", n>>10)
	}
	return fmt.Sprintf("%d B", n)
}

func wrapStyled(st lipgloss.Style, text string, w int) []string {
	var out []string
	for _, l := range wrap(text, w) {
		out = append(out, st.Render(l))
	}
	return out
}

func wrap(text string, w int) []string {
	if w < 10 {
		w = 10
	}
	var out []string
	for _, l := range strings.Split(text, "\n") {
		if l == "" {
			out = append(out, "")
			continue
		}
		out = append(out, strings.Split(ansi.Wrap(l, w, ""), "\n")...)
	}
	return out
}

// ------------------------------------------------------------------- chat

var reMention = regexp.MustCompile(`@[\p{L}\p{N}_.-]+`)

// chatLines draws a conversation: a day line, then groups of messages by
// sender (name and time once, then each message behind a coloured bar);
// yours sit on the right with a green bar.
func (m *Model) chatLines(w int) []string {
	m.chatMsgLine = map[string]int{}
	threaded := false
	if sp, ok := m.curSpace(); ok {
		threaded = sp.Threaded
	}
	byName := map[string]store.ChatMessage{}
	for _, c := range m.chatMsgs {
		byName[c.Name] = c
	}
	picking := m.pickingMsg()
	s := m.styles
	if len(m.chatMsgs) == 0 {
		return []string{s.muted.Render("No recent messages synced.")}
	}
	threadCount := map[string]int{}
	first := map[string]string{}
	for _, c := range m.chatMsgs {
		threadCount[c.Thread]++
		if _, ok := first[c.Thread]; !ok {
			first[c.Thread] = c.Name
		}
	}
	mentionStyle := lipgloss.NewStyle().Background(lipgloss.Color(s.c.Accent)).Foreground(lipgloss.Color(s.c.Base))
	mention := func(t string) string { return s.round(mentionStyle, t) }
	var out []string
	var lastDay string
	bw := max(20, w*4/5)
	for i := 0; i < len(m.chatMsgs); {
		c := m.chatMsgs[i]
		t := time.UnixMilli(c.Created)
		if day := dayName(t); day != lastDay {
			lastDay = day
			label := " " + day + " "
			side := max(2, (w-lipgloss.Width(label))/2)
			if len(out) > 0 {
				out = append(out, "")
			}
			out = append(out, s.muted.Render(strings.Repeat("─", side)+label+strings.Repeat("─", max(0, w-side-lipgloss.Width(label)))))
		}
		// a group: same sender, same day, each within 10 minutes of the last
		j := i + 1
		for j < len(m.chatMsgs) && m.chatMsgs[j].Sender == c.Sender && m.chatMsgs[j].Created-m.chatMsgs[j-1].Created < 10*60*1000 &&
			dayName(time.UnixMilli(m.chatMsgs[j].Created)) == lastDay {
			j++
		}
		name := c.SenderName
		if c.Mine {
			name = "You"
		}
		ac := s.avatarColors()
		color := ac[hash(name)%len(ac)]
		if c.Mine {
			color = s.c.Green
		}
		bar := fg(color).Bold(true).Render("┃")
		out = append(out, "")
		head := m.avatar(name) + " " + fg(color).Bold(true).Render(name) + " " + s.muted.Render(t.Format("15:04"))
		if c.Mine {
			head = s.muted.Render(t.Format("15:04")) + " " + fg(color).Bold(true).Render(name) + " " + m.avatar(name)
			out = append(out, padLeft(head, w))
		} else {
			out = append(out, head)
		}
		for k, g := range m.chatMsgs[i:j] {
			m.chatMsgLine[g.Name] = len(out)
			var lines []string
			if threaded && first[g.Thread] != g.Name {
				lines = append(lines, s.muted.Render("↳ reply in a thread"))
			}
			if g.Quoted != "" {
				// a reply quoting another message, as Chat shows it
				who, what := "", "a message"
				if q, ok := byName[g.Quoted]; ok {
					who, what = q.SenderName, oneLine(q.Text)
					if q.Mine {
						who = "You"
					}
				}
				lines = append(lines, s.muted.Render("┊ ❝ ")+s.sub.Bold(true).Render(who)+" "+s.muted.Render(ansi.Truncate(what, bw-lipgloss.Width(who)-6, "…")))
			}
			for _, l := range wrap(mime.Clean(g.Text), bw-3) {
				if strings.HasPrefix(l, "📎 ") {
					lines = append(lines, s.peach.Render(l))
					continue
				}
				lines = append(lines, styleMentions(l, s.text, mention))
			}
			if len(g.Files) > 0 {
				lines = append(lines, s.muted.Render("v saves · V opens"))
			}
			if threaded && first[g.Thread] == g.Name && threadCount[g.Thread] > 1 {
				lines = append(lines, s.muted.Render(fmt.Sprintf("↳ %d %s in this thread", threadCount[g.Thread]-1,
					map[bool]string{true: "reply", false: "replies"}[threadCount[g.Thread] == 2])))
			}
			gbar := bar
			if g.Name == m.flashMsg {
				gbar = s.accent.Bold(true).Render("▶") // the one search opened
			}
			if picking && i+k == m.chatSel {
				gbar = s.accent.Bold(true).Render("▌") // the picked one: r replies to it
				for x := range lines {
					lines[x] = s.accent.Bold(true).Render(stripANSI(lines[x]))
				}
			}
			for _, l := range lines {
				if c.Mine {
					out = append(out, padLeft(l+" "+gbar, w))
				} else {
					out = append(out, m.indent()+gbar+" "+l)
				}
			}
		}
		i = j
	}
	return out
}

func hash(s string) int {
	h := 0
	for _, r := range s {
		h = h*31 + int(r)
	}
	if h < 0 {
		h = -h
	}
	return h
}

// styleMentions colours @names in a plain line.
func styleMentions(l string, text lipgloss.Style, mention func(string) string) string {
	var b strings.Builder
	last := 0
	for _, ix := range reMention.FindAllStringIndex(l, -1) {
		b.WriteString(text.Render(l[last:ix[0]]))
		b.WriteString(mention(l[ix[0]:ix[1]]))
		last = ix[1]
	}
	b.WriteString(text.Render(l[last:]))
	return b.String()
}

func padLeft(s string, w int) string {
	return strings.Repeat(" ", max(0, w-lipgloss.Width(s))) + s
}

func dayName(t time.Time) string {
	now := time.Now()
	y1, m1, d1 := t.Date()
	y2, m2, d2 := now.Date()
	switch {
	case y1 == y2 && m1 == m2 && d1 == d2:
		return "Today"
	case y1 == y2 && m1 == m2 && d1 == d2-1:
		return "Yesterday"
	case now.Sub(t) < 6*24*time.Hour:
		return t.Format("Monday")
	}
	return t.Format("Mon 02 Jan")
}

// previewBox draws the preview pane: the pre-rendered lines, scrolled,
// with the Claude box and (in chat) the message line under them.
func (m Model) previewBox(w, h int) string {
	s := m.styles
	inner := w - 4
	title := "preview"
	if _, ok := m.curOutbox(); ok {
		title = "outbox"
	}
	if t, ok := m.curThread(); ok {
		title = "thread · " + t.Account
	} else if sp, ok := m.curSpace(); ok {
		title = map[bool]string{true: "@ ", false: "# "}[sp.Type == "DIRECT_MESSAGE"] + cli.SpaceTitle(sp)
	}
	aiLines := m.aiLines(inner)
	var bottom []string
	if _, ok := m.curSpace(); ok {
		if m.mode == modeChatMsg {
			bottom = []string{s.muted.Render(strings.Repeat("─", inner)), s.accent.Render("› ") + s.text.Render(string(m.input)) + s.accent.Render("▏")}
			if r := m.replyTo; r != nil {
				who := r.SenderName
				if r.Mine {
					who = "your message"
				}
				line := s.accent.Render("↩ replying to ") + s.bold.Render(who) + s.muted.Render(": "+oneLine(r.Text))
				bottom = append([]string{bottom[0], ansi.Truncate(line, inner-12, "…") + s.muted.Render("  esc drops")}, bottom[1:]...)
			}
		} else {
			bottom = []string{s.muted.Render(strings.Repeat("─", inner)), s.accent.Render("› ") + s.muted.Render("i types a message here · c writes it in your editor · r replies in the thread")}
			if m.a.ChatAppMissing.Load() {
				bottom[1] = s.yellow.Render("! ") + s.muted.Render("read and unread stay on this machine until Chat API → Configuration is filled in (sandesh doctor)")
			}
		}
	}
	avail := h - 2 - len(aiLines) - len(bottom)
	if avail < 3 {
		aiLines = aiLines[:max(0, len(aiLines)-(3-avail))]
		avail = 3
	}
	lines := m.preview
	scroll := max(0, min(m.scroll, len(lines)-avail))
	end := min(len(lines), scroll+avail)
	view := append([]string{}, lines[scroll:end]...)
	for len(view) < avail {
		view = append(view, "")
	}
	if max := len(lines) - avail; max > 0 {
		title += fmt.Sprintf(" · %d%%", scroll*100/max)
	}
	for i := range view {
		view[i] = " " + view[i]
	}
	for _, l := range aiLines {
		view = append(view, " "+l)
	}
	for _, l := range bottom {
		view = append(view, " "+l)
	}
	return m.box(w, h, title, strings.Join(view, "\n"), m.focus == 1 && m.mode == modeNormal, s.c.Accent)
}
