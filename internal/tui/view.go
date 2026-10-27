package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/Theyashsawarkar/sandesh/internal/app"
	"github.com/Theyashsawarkar/sandesh/internal/config"
	"github.com/Theyashsawarkar/sandesh/internal/store"
)

type styles struct {
	c                                             config.Colors
	text, sub, muted, accent, accent2, bold, warn lipgloss.Style
	green, blue, yellow, red, peach               lipgloss.Style
	tabOn, tabOff, sel, pill, keycap, banner      lipgloss.Style
	nerd                                          bool // round ends from Nerd Font half circles
}

// round renders a filled label with round ends (Nerd Font half circles
// in the fill colour) instead of square ones; plain when there's no
// Nerd Font.
func (s styles) round(st lipgloss.Style, text string) string {
	if !s.nerd {
		return st.Render(text)
	}
	cap := lipgloss.NewStyle().Foreground(st.GetBackground())
	return cap.Render("\ue0b6") + st.UnsetPadding().Render(text) + cap.Render("\ue0b4")
}

func newStyles(c config.Colors, nerd bool) styles {
	fg := func(col string) lipgloss.Style { return lipgloss.NewStyle().Foreground(lipgloss.Color(col)) }
	return styles{
		c: c, text: fg(c.Text), sub: fg(c.Subtle), muted: fg(c.Muted), accent: fg(c.Accent), accent2: fg(c.Accent2),
		bold: fg(c.Text).Bold(true), warn: fg(c.Peach), green: fg(c.Green), blue: fg(c.Blue), yellow: fg(c.Yellow), red: fg(c.Red), peach: fg(c.Peach),
		tabOn:  lipgloss.NewStyle().Foreground(lipgloss.Color(c.Base)).Background(lipgloss.Color(c.Accent)).Bold(true).Padding(0, 1),
		tabOff: fg(c.Muted).Padding(0, 1),
		sel:    lipgloss.NewStyle().Background(lipgloss.Color(c.Surface)),
		pill:   lipgloss.NewStyle().Foreground(lipgloss.Color(c.Base)).Background(lipgloss.Color(c.Accent)).Bold(true).Padding(0, 1),
		keycap: lipgloss.NewStyle().Foreground(lipgloss.Color(c.Text)).Background(lipgloss.Color(c.Surface)).Padding(0, 1),
		banner: lipgloss.NewStyle().Foreground(lipgloss.Color(c.Base)).Background(lipgloss.Color(c.Peach)).Bold(true).Padding(0, 1),
		nerd:   nerd,
	}
}

func (m Model) renamePrompt() string {
	sp, ok := m.curSpace()
	if !ok {
		return "name: "
	}
	switch sp.Type {
	case "SPACE":
		return "rename on Google Chat (everyone sees it): "
	case "GROUP_CHAT":
		return "name this group on Google Chat (everyone sees it; it becomes a space): "
	}
	return "name this DM (only here; empty: back to the default): "
}

func thousands(n int) string {
	s := fmt.Sprint(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

func (m Model) listWidth() int { return max(30, m.w*42/100) }

func (m Model) bodyHeight() int {
	h := m.h - 2 // tabs + footer
	if len(m.waiting) > 0 && m.mode != modeApprove {
		h--
	}
	if m.err != "" {
		h--
	}
	if m.inputMode() {
		h--
	}
	return max(5, h)
}

func (m Model) previewHeight() int { return m.bodyHeight() - 2 }

func (m Model) inputMode() bool {
	switch m.mode {
	case modeFilter, modeAsk, modeSearch, modeSaveName, modeDraftHint, modeLink, modeRename:
		return true
	}
	return false
}

func (m Model) View() string {
	if m.w == 0 {
		return ""
	}
	var rows []string
	rows = append(rows, m.tabsView())
	lw := m.listWidth()
	pw := m.w - lw
	bh := m.bodyHeight()
	left := m.box(lw, bh, m.listTitle(), m.listView(lw-2, bh-2), m.focus == 0 && m.mode == modeNormal, m.styles.c.Accent)
	// under a popup, draw what it was opened over
	under := m.mode
	switch {
	case m.mode == modeWhen && m.when != nil:
		under = m.when.back
	case m.mode == modePick && m.pick != nil:
		under = m.pickMode
	case m.mode == modeConvo && m.convo != nil:
		under = m.convo.back
	}
	var right string
	switch under {
	case modeReview:
		right = m.box(pw, bh, "review: y send · e edit · + file · d draft · n discard", m.reviewView(pw-4, bh-2), true, m.styles.c.Accent2)
	case modeApprove:
		right = m.box(pw, bh, "an agent wants to send: y send · e edit · n decline · q later", m.approveView(pw-4, bh-2), true, m.styles.c.Peach)
	default:
		right = m.previewBox(pw, bh)
	}
	if under == modeCompose && m.comp != nil {
		rows = append(rows, m.composerPanes(bh))
	} else {
		rows = append(rows, lipgloss.JoinHorizontal(lipgloss.Top, left, right))
	}
	switch {
	case m.mode == modeFind && m.find != nil:
		rows[len(rows)-1] = overlay(rows[len(rows)-1], m.finderView(), m.w)
	case m.mode == modeHelp:
		box := m.helpPopup()
		rows[len(rows)-1] = overlayAt(rows[len(rows)-1], box, m.w, max(0, (bh-len(box))/2))
	case m.mode == modeReact:
		box := m.reactPopup()
		rows[len(rows)-1] = overlayAt(rows[len(rows)-1], box, m.w, max(0, bh/2-2))
	case m.mode == modeConvo && m.convo != nil:
		box := m.convoPopup()
		rows[len(rows)-1] = overlayAt(rows[len(rows)-1], box, m.w, max(0, (bh-len(box))/3))
	case m.mode == modeWhen && m.when != nil:
		box := m.whenPopup()
		rows[len(rows)-1] = overlayAt(rows[len(rows)-1], box, m.w, max(0, (bh-len(box))/2))
	case m.mode == modePick && m.pick != nil:
		box := m.pickerPopup()
		rows[len(rows)-1] = overlayAt(rows[len(rows)-1], box, m.w, max(0, (bh-len(box))/2))
	}
	if len(m.waiting) > 0 && m.mode != modeApprove {
		rows = append(rows, m.styles.round(m.styles.banner, fmt.Sprintf("󰀦 %d send(s) from an agent wait for your approval · press A", len(m.waiting))))
	}
	if m.err != "" {
		e := m.styles.red.Render("● " + m.err)
		rows = append(rows, " "+ansi.Truncate(e, m.w-14, "…")+m.styles.muted.Render("  esc hides"))
	}
	if m.inputMode() {
		rows = append(rows, m.inputView())
	}
	rows = append(rows, m.footerView())
	return strings.Join(rows, "\n")
}

func (m Model) tabsView() string {
	s := m.styles
	var parts []string
	if m.tab == tabMail {
		for i, sec := range m.sections {
			label := sec.Name
			if n := m.counts[sec.Name]; n > 0 {
				label += fmt.Sprintf(" %d", n)
			}
			if i == m.sec && m.search == nil {
				parts = append(parts, s.round(s.tabOn, label))
			} else {
				parts = append(parts, s.tabOff.Render(label))
			}
		}
		if m.search != nil {
			parts = append(parts, s.round(s.tabOn, m.search.Name))
		}
		chat := "Chat"
		if m.chatN > 0 {
			chat += fmt.Sprintf(" %d", m.chatN)
		}
		parts = append(parts, s.muted.Render("│"), s.tabOff.Render(chat))
	} else {
		mail := "Mail"
		if n := m.counts[m.sections[0].Name]; n > 0 {
			mail += fmt.Sprintf(" %d", n)
		}
		parts = append(parts, s.tabOff.Render(mail), s.muted.Render("│"))
		for i, name := range chatSections {
			label := name
			if name == "All" && m.chatN > 0 {
				label += fmt.Sprintf(" %d", m.chatN)
			}
			if name == "Scheduled" && m.chatScheduled > 0 {
				label += fmt.Sprintf(" %d", m.chatScheduled)
			}
			if i == m.sec {
				parts = append(parts, s.round(s.tabOn, label))
			} else {
				parts = append(parts, s.tabOff.Render(label))
			}
		}
	}
	left := " " + strings.Join(parts, " ")
	var right string
	acct := "all accounts"
	if f := m.accountFilter(); len(f) > 0 {
		acct = f[0]
	} else if len(m.accounts) == 1 {
		acct = m.accounts[0]
	}
	switch {
	case m.syncing:
		right = s.muted.Render("syncing… ") + s.green.Render("●") + " " + acct
	case m.err != "":
		right = s.red.Render("● ") + s.text.Render(acct)
	default:
		right = s.green.Render("●") + " " + s.sub.Render(acct)
	}
	if m.launch == "demo" {
		right = s.peach.Render("demo · example mail ") + right
	}
	if !m.older.Done && m.older.Total > m.older.Have {
		const n = 10
		filled := min(n, m.older.Have*n/max(1, m.older.Total))
		bar := s.accent.Render(strings.Repeat("▰", filled)) + s.muted.Render(strings.Repeat("▱", n-filled))
		right = s.muted.Render("older mail ") + bar + s.muted.Render(fmt.Sprintf(" %s / %s   ", thousands(m.older.Have), thousands(m.older.Total))) + right
	}
	gap := m.w - lipgloss.Width(left) - lipgloss.Width(right) - 1
	if gap < 1 {
		return ansi.Truncate(left, m.w, "…")
	}
	return left + strings.Repeat(" ", gap) + right + " "
}

// box draws a rounded border with a title in it.
func (m Model) box(w, h int, title, content string, focus bool, color string) string {
	s := m.styles
	bc := s.c.Border
	if focus {
		bc = color
	}
	b := lipgloss.NewStyle().Foreground(lipgloss.Color(bc))
	tstyle := s.muted
	if focus {
		tstyle = lipgloss.NewStyle().Foreground(lipgloss.Color(color)).Bold(true)
	}
	inner := w - 2
	t := ansi.Truncate(" "+title+" ", max(0, inner-2), "…")
	top := b.Render("╭─") + tstyle.Render(t) + b.Render(strings.Repeat("─", max(0, inner-1-lipgloss.Width(t)))+"╮")
	lines := strings.Split(content, "\n")
	var out []string
	out = append(out, top)
	for i := 0; i < h-2; i++ {
		l := ""
		if i < len(lines) {
			l = lines[i]
		}
		l = ansi.Truncate(l, inner, "…")
		out = append(out, b.Render("│")+l+strings.Repeat(" ", max(0, inner-lipgloss.Width(l)))+b.Render("│"))
	}
	out = append(out, b.Render("╰"+strings.Repeat("─", inner)+"╯"))
	return strings.Join(out, "\n")
}

func (m Model) listTitle() string {
	if m.tab == tabMail {
		n := len(m.threads)
		t := fmt.Sprintf("%s · %d", m.currentSection().Name, n)
		if m.filter != "" {
			t += " · /" + m.filter
		}
		if len(m.selected) > 0 {
			t += fmt.Sprintf(" · %d selected", len(m.selected))
		}
		return t
	}
	t := fmt.Sprintf("%s · %d", chatSections[m.sec%len(chatSections)], len(m.spaces))
	if m.filter != "" {
		t += " · /" + m.filter
	}
	return t
}

func (m Model) aiLines(w int) []string {
	if !m.aiBusy && m.ai == "" {
		return nil
	}
	s := m.styles
	text := m.ai
	if m.aiBusy {
		text = s.muted.Render("thinking…")
	}
	head := s.accent2.Bold(true).Render(m.aiTitle)
	lines := []string{s.accent2.Render(strings.Repeat("┄", min(w, 60))), head}
	body := wrap(text, w-2)
	if len(body) > 14 {
		body = append(body[:14], s.muted.Render("… (Esc closes)"))
	}
	for _, l := range body {
		lines = append(lines, s.text.Render(l))
	}
	return lines
}

// helpView lists every key by group, in two columns when one is too tall.

func stripANSI(s string) string { return ansi.Strip(s) }

func (m Model) reviewView(w, h int) string {
	r := m.review
	if r == nil {
		return ""
	}
	return m.outgoingView(r.kind, r.out, "", w)
}

func (m Model) approveView(w, h int) string {
	if len(m.waiting) == 0 {
		return ""
	}
	it := m.waiting[0]
	head := ""
	if len(m.waiting) > 1 {
		head = fmt.Sprintf("1 of %d · ", len(m.waiting))
	}
	return m.outgoingView(it.Kind, it.Out, head+"asked because "+it.Reason, w)
}

func (m Model) outgoingView(kind string, out store.Outgoing, note string, w int) string {
	s := m.styles
	var lines []string
	if note != "" {
		lines = append(lines, s.muted.Render(note), "")
	}
	field := func(k, v string) {
		if v != "" {
			lines = append(lines, s.muted.Render(fmt.Sprintf("%-8s", k))+s.bold.Render(v))
		}
	}
	if kind == "chat" {
		field("To", firstNonEmpty(out.SpaceName, out.Space))
		if out.Thread != "" {
			field("Thread", "replying in the latest thread")
		}
	} else {
		field("To", strings.Join(out.To, ", "))
		field("Cc", strings.Join(out.Cc, ", "))
		field("Bcc", strings.Join(out.Bcc, ", "))
		field("Subject", out.Subject)
	}
	for _, f := range out.Files {
		name, size := f.Name, f.Size
		if name == "" { // not copied yet: what's on disk
			name = filepath.Base(f.Source)
			if st, err := os.Stat(app.ExpandPath(f.Source)); err == nil {
				size = st.Size()
			} else {
				name += " (not found)"
			}
		}
		info := ""
		if size > 0 {
			info = "  " + app.HumanSize(size)
		}
		if dir := filepath.Dir(f.Source); dir != "." {
			info += "  in " + tilde(dir)
		}
		lines = append(lines, ansi.Truncate(s.muted.Render(fmt.Sprintf("%-8s", "File"))+s.peach.Render("📎 "+name)+s.muted.Render(info), w, "…"))
	}
	lines = append(lines, s.muted.Render(strings.Repeat("─", min(w, 60))))
	for _, l := range wrap(out.Body, w) {
		if strings.HasPrefix(l, ">") {
			lines = append(lines, s.muted.Render(l))
		} else {
			lines = append(lines, s.text.Render(l))
		}
	}
	return strings.Join(lines, "\n")
}

func firstNonEmpty(xs ...string) string {
	for _, x := range xs {
		if x != "" {
			return x
		}
	}
	return ""
}

func (m Model) inputView() string {
	s := m.styles
	prompt := map[mode]string{modeFilter: "/", modeAsk: "@claude ", modeSearch: "search gmail: ", modeSaveName: "section name (Enter: the search itself): ",
		modeDraftHint: "draft a reply that says (Enter for Claude's pick): ", modeLink: fmt.Sprintf("open link (1-%d): ", len(m.links)), modeRename: m.renamePrompt()}[m.mode]
	return " " + s.accent2.Bold(true).Render(prompt) + s.text.Render(string(m.input)) + s.accent.Render("▏")
}

func (m Model) footerView() string {
	s := m.styles
	modeName := map[mode]string{modeNormal: "normal", modeFilter: "filter", modeAsk: "ask", modeSearch: "search", modeSaveName: "save",
		modeDraftHint: "draft", modeReview: "review", modeApprove: "approve", modeHelp: "keys", modeChatMsg: "message", modeLink: "link", modeCompose: "compose", modeFind: "search", modeRename: "rename", modePick: "files", modeWhen: "later", modeConvo: "new chat", modeReact: "react"}[m.mode]
	var keys [][2]string
	switch m.mode {
	case modeReview:
		keys = [][2]string{{"y", "send"}, {"t", "later"}, {"e", "edit"}, {"+", "file"}, {"-", "drop file"}, {"d", "save draft"}, {"n", "discard"}}
		if m.review != nil && m.review.kind == "chat" {
			keys = [][2]string{{"y", "send"}, {"t", "later"}, {"e", "edit"}, {"+", "file"}, {"-", "drop file"}, {"n", "discard"}}
		}
	case modeApprove:
		keys = [][2]string{{"y", "send"}, {"e", "edit"}, {"n", "decline"}, {"q", "later"}}
	case modeConvo:
		keys = [][2]string{{"↑↓", "pick"}, {"tab", "add person"}, {"enter", "open"}, {"esc", "cancel"}}
	case modeWhen:
		keys = [][2]string{{"↑↓", "pick"}, {"enter", "schedule"}, {"esc", "cancel"}}
	case modePick:
		keys = [][2]string{{"↑↓", "move"}, {"tab", "pick several"}, {"enter", "add"}, {"esc", "close"}}
	case modeFind:
		keys = [][2]string{{"↑↓", "move"}, {"enter", "open"}, {"tab", "mail/chat"}, {"esc", "close"}}
	case modeCompose:
		keys = [][2]string{{"tab", "next"}, {"↑↓", "pick"}, {"ctrl+o", "attach"}, {"ctrl+b", "cc/bcc"}, {"ctrl+q", "quote"}, {"ctrl+g", "signature"}}
		if m.comp != nil && m.comp.reply {
			keys = append(keys, [2]string{"pgup/dn", "original"})
		}
		if m.comp != nil && m.comp.approveID > 0 {
			keys = [][2]string{{"tab", "next"}, {"ctrl+s", "keep changes"}, {"ctrl+e", "editor"}, {"esc", "back"}}
		}
	case modeFilter, modeAsk, modeSearch, modeSaveName, modeDraftHint, modeLink, modeRename:
		keys = [][2]string{{"enter", "done"}, {"esc", "cancel"}, {"ctrl+u", "clear"}}
	case modeChatMsg:
		keys = [][2]string{{"enter", "send"}, {"ctrl+o", "file"}, {"ctrl+t", "send later"}, {"esc", "cancel"}, {"ctrl+u", "clear"}}
		if m.editingMsg != "" {
			keys = [][2]string{{"enter", "save the edit"}, {"esc", "cancel"}}
		}
	case modeHelp:
		keys = [][2]string{{"?", "close"}}
	default:
		if m.tab == tabMail {
			keys = [][2]string{{"j/k", "move"}, {"h/l", "section"}, {"tab", "chat"}, {"⏎", "open"}, {"m/M", "read/unread"}, {"c", "compose"}, {"r", "reply"},
				{"e", "archive"}, {"Q", "quoted"}, {"o", "link"}, {"@", "ask"}, {"?", "keys"}}
		} else if m.pickingMsg() {
			keys = [][2]string{{"j/k", "pick"}, {"⏎", "to the original"}, {"⌫", "back"}, {"r", "reply"}, {"e", "react"}, {"E", "edit"}, {"d", "delete"}, {"y", "copy"}, {"[ ]", "chats"}, {"1", "list"}}
		} else {
			keys = [][2]string{{"j/k", "move"}, {"h/l", "section"}, {"tab", "mail"}, {"⏎", "open"}, {"i", "type"}, {"r", "reply"}, {"N", "new chat"},
				{"m/M", "read/unread"}, {"@", "ask"}, {"?", "keys"}}
		}
	}
	left := s.round(s.pill, modeName)
	for _, k := range keys {
		left += " " + s.round(s.keycap, k[0]) + s.muted.Render(" "+k[1])
	}
	right := ""
	if m.toast != "" {
		right = s.green.Render(m.toast)
	}
	rw := lipgloss.Width(right)
	if rw > m.w/2 {
		right = ansi.Truncate(right, m.w/2, "…")
		rw = lipgloss.Width(right)
	}
	left = ansi.Truncate(left, max(10, m.w-rw-2), "")
	gap := max(1, m.w-lipgloss.Width(left)-rw-1)
	return left + strings.Repeat(" ", gap) + right
}
