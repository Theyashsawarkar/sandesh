package tui

import (
	"fmt"
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

// cardBox draws a rounded card of inner width w around lines. sel fills it
// with the surface colour and thickens the left edge; dot marks it unread.
func (m Model) cardBox(lines []string, w int, border string, sel, dot bool) []string {
	s := m.styles
	b := fg(border)
	bg := ""
	if sel {
		bg = "\x1b[48;2;" + rgb(s.c.Surface) + "m"
	}
	out := []string{b.Render("╭" + strings.Repeat("─", w+2) + "╮")}
	for i, l := range lines {
		left := b.Render("│")
		if sel {
			left = fg(s.c.Accent).Bold(true).Render("┃")
		}
		if dot && i == 0 {
			left = s.accent.Render("●")
		}
		body := " " + fit(l, w) + " "
		if sel {
			body = bg + strings.ReplaceAll(body, "\x1b[0m", "\x1b[0m"+bg) + "\x1b[0m"
		}
		out = append(out, left+body+b.Render("│"))
	}
	return append(out, b.Render("╰"+strings.Repeat("─", w+2)+"╯"))
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

var avatarColors = []string{"#f5c2e7", "#fab387", "#a6e3a1", "#89b4fa", "#cba6f7", "#94e2d5", "#f9e2af", "#f2cdcd"}

// avatar is two initials on a colour picked from the name.
func (m Model) avatar(name string) string {
	h := hash(name)
	var ini []rune
	for _, w := range strings.FieldsFunc(name, func(r rune) bool { return r == ' ' || r == '.' || r == '@' || r == '_' || r == ',' || r == '+' }) {
		ini = append(ini, []rune(strings.ToUpper(w))[0])
		if len(ini) == 2 {
			break
		}
	}
	for len(ini) < 2 {
		ini = append(ini, ' ')
	}
	return lipgloss.NewStyle().Background(lipgloss.Color(avatarColors[h%len(avatarColors)])).
		Foreground(lipgloss.Color(m.styles.c.Base)).Bold(true).Render(string(ini))
}

func (m Model) chip(text, color string) string {
	return fg(color).Render("‹" + text + "›")
}

// ------------------------------------------------------------------- list

func (m *Model) listView(w, h int) string {
	s := m.styles
	if m.count() == 0 {
		msg := "Nothing here."
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
			chipStr := strings.Join(chips, " ")
			snipW := cw - lipgloss.Width(chipStr) - 1
			snip := ansi.Truncate(s.muted.Render(oneLine(t.Snippet)), max(4, snipW), "…")
			lines = []string{row(who, s.muted.Render(cli.When(t.LastDate)), cw), subj, row(snip, chipStr, cw)}
		} else {
			sp := m.spaces[i]
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
			lines = []string{row(m.avatar(name)+" "+mark+title, s.muted.Render(cli.When(sp.LastActive)), cw), "   " + s.muted.Render(last)}
		}
		for _, l := range m.cardBox(lines, cw, border, sel, dot) {
			out = append(out, " "+l)
		}
		out = append(out, "")
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
		Code: fg(s.c.Peach), TableHead: fg(s.c.Accent).Bold(true), Border: fg(s.c.Surface),
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
			lines := []string{head, "   " + ansi.Truncate(s.muted.Render(oneLine(msg.Body)), inner-3, "…")}
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
		lines = append(lines, "   "+s.muted.Render(to))
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
		for _, at := range msg.Attachments {
			lines = append(lines, "", s.peach.Render("📎 "+at.Name)+s.muted.Render(" · "+sizeOf(at.Size)))
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
	mention := lipgloss.NewStyle().Background(lipgloss.Color(s.c.Accent)).Foreground(lipgloss.Color(s.c.Base))
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
		color := avatarColors[hash(name)%len(avatarColors)]
		if c.Mine {
			color = s.c.Green
		}
		bar := fg(color).Render("▎")
		out = append(out, "")
		head := m.avatar(name) + " " + fg(color).Bold(true).Render(name) + " " + s.muted.Render(t.Format("15:04"))
		if c.Mine {
			head = s.muted.Render(t.Format("15:04")) + " " + fg(color).Bold(true).Render(name) + " " + m.avatar(name)
			out = append(out, padLeft(head, w))
		} else {
			out = append(out, head)
		}
		for _, g := range m.chatMsgs[i:j] {
			var lines []string
			if first[g.Thread] != g.Name {
				lines = append(lines, s.muted.Render("↳ reply in a thread"))
			}
			for _, l := range wrap(mime.Clean(g.Text), bw-3) {
				lines = append(lines, styleMentions(l, s.text, mention))
			}
			if first[g.Thread] == g.Name && threadCount[g.Thread] > 1 {
				lines = append(lines, s.muted.Render(fmt.Sprintf("↳ %d %s in this thread", threadCount[g.Thread]-1,
					map[bool]string{true: "reply", false: "replies"}[threadCount[g.Thread] == 2])))
			}
			for _, l := range lines {
				if c.Mine {
					out = append(out, padLeft(l+" "+bar, w))
				} else {
					out = append(out, "   "+bar+" "+l)
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
func styleMentions(l string, text, mention lipgloss.Style) string {
	var b strings.Builder
	last := 0
	for _, ix := range reMention.FindAllStringIndex(l, -1) {
		b.WriteString(text.Render(l[last:ix[0]]))
		b.WriteString(mention.Render(l[ix[0]:ix[1]]))
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
		} else {
			bottom = []string{s.muted.Render(strings.Repeat("─", inner)), s.accent.Render("› ") + s.muted.Render("i types a message here · c writes it in your editor · r replies in the thread")}
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
