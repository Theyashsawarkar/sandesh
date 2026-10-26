package tui

import (
	"context"
	"fmt"
	"net/mail"
	"regexp"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/cursor"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/Theyashsawarkar/sandesh/internal/app"
	"github.com/Theyashsawarkar/sandesh/internal/cli"
	"github.com/Theyashsawarkar/sandesh/internal/mime"
	"github.com/Theyashsawarkar/sandesh/internal/store"
)

// The compose screen: a form in the right panel. Recipients are chips with
// autocomplete from your contacts (and the Workspace directory), the body
// is edited in place, and a reply's quoted text stays folded underneath.

const (
	fTo = iota
	fCc
	fBcc
	fSubject
	fFiles
	fBody
	nFields
)

var fieldNames = [nFields]string{"To", "Cc", "Bcc", "Subject", "Files", ""}

type recip struct{ Name, Addr string }

func (r recip) String() string {
	if r.Name == "" {
		return r.Addr
	}
	return (&mail.Address{Name: r.Name, Address: r.Addr}).String()
}

func (r recip) label() string {
	if r.Name != "" {
		return r.Name
	}
	return r.Addr
}

type composer struct {
	account, from string
	title         string
	base          store.Outgoing // thread, references, draft id...
	rcpt          [3][]recip
	inputs        [3]textinput.Model
	subject       textinput.Model
	body          textarea.Model
	files         []store.File
	fileSel       int
	quoted        string
	showQuoted    bool
	focus         int
	sugg          []store.Contact
	suggSel       int
	approveID     int64 // editing an agent's message
	armed         bool  // Esc pressed once on a written message
	dirAsked      string
}

type dirMsg struct {
	q     string
	found []store.Contact
}

type dirTickMsg struct{ q string }

func newInput(s styles) textinput.Model {
	ti := textinput.New()
	ti.Prompt = ""
	ti.Cursor.SetMode(cursor.CursorStatic)
	ti.TextStyle = s.text
	ti.PlaceholderStyle = s.muted
	ti.Cursor.Style = lipgloss.NewStyle().Foreground(lipgloss.Color(s.c.Accent))
	return ti
}

// openComposer starts the compose screen. quoted is a reply's or forward's
// original, kept under the body.
func (m *Model) openComposer(account, title string, out store.Outgoing, quoted string, approveID int64) tea.Cmd {
	if m.a.Config.Compose == "editor" {
		out.Body = strings.TrimRight(out.Body, "\n")
		if quoted != "" {
			out.Body += "\n\n" + quoted
		}
		return m.editMail(account, out, approveID)
	}
	s := m.styles
	c := &composer{account: account, title: title, base: out, quoted: quoted, approveID: approveID, files: out.Files}
	if acc, err := m.a.Store.Account(account); err == nil {
		c.from = acc.Email
		if len(m.accounts) > 1 {
			c.from = account + " · " + acc.Email
		}
	}
	for i, list := range [][]string{out.To, out.Cc, out.Bcc} {
		for _, a := range list {
			c.rcpt[i] = append(c.rcpt[i], m.recipFor(account, a))
		}
		c.inputs[i] = newInput(s)
	}
	c.inputs[fTo].Placeholder = "name or address"
	c.subject = newInput(s)
	c.subject.Placeholder = "what it's about"
	c.subject.SetValue(out.Subject)
	ta := textarea.New()
	ta.ShowLineNumbers = false
	ta.Prompt = ""
	ta.CharLimit = 0
	ta.MaxHeight = 0
	ta.Placeholder = "Write your message…"
	ta.Cursor.SetMode(cursor.CursorStatic)
	ta.Cursor.Style = lipgloss.NewStyle().Foreground(lipgloss.Color(s.c.Accent))
	ta.EndOfBufferCharacter = ' '
	plain := lipgloss.NewStyle()
	ta.FocusedStyle = textarea.Style{Base: plain, Text: s.text, Placeholder: s.muted, CursorLine: plain, EndOfBuffer: s.muted, CursorLineNumber: plain, LineNumber: plain, Prompt: plain}
	ta.BlurredStyle = ta.FocusedStyle
	ta.SetValue(out.Body)
	c.body = ta
	// start where there's something to write
	switch {
	case len(c.rcpt[fTo]) == 0:
		c.focus = fTo
	case out.Subject == "":
		c.focus = fSubject
	default:
		c.focus = fBody
	}
	c.applyFocus()
	m.comp = c
	m.mode = modeCompose
	m.layoutComposer()
	if c.focus == fBody {
		c.body.CursorStart()
	}
	return nil
}

// recipFor parses an address, naming it from your contacts when it has no
// name of its own.
func (m *Model) recipFor(account, a string) recip {
	name, addr := mime.ParseAddress(a)
	if name == "" {
		if cs, _ := m.a.Store.Contacts(account, addr, 1); len(cs) == 1 && strings.EqualFold(cs[0].Addr, addr) {
			name = cs[0].Name
		}
	}
	return recip{name, addr}
}

// splitQuoted splits a body written in the editor into what you wrote and
// the quoted original under it.
var quoteStart = regexp.MustCompile(`(?m)^(On .+ wrote:|---------- Forwarded message ---------)$`)

func splitQuoted(body string) (string, string) {
	if loc := quoteStart.FindStringIndex(body); loc != nil {
		return strings.TrimRight(body[:loc[0]], "\n"), body[loc[0]:]
	}
	return body, ""
}

func (c *composer) applyFocus() {
	for i := range c.inputs {
		if i == c.focus {
			c.inputs[i].Focus()
		} else {
			c.inputs[i].Blur()
		}
	}
	if c.focus == fSubject {
		c.subject.Focus()
	} else {
		c.subject.Blur()
	}
	if c.focus == fBody {
		c.body.Focus()
	} else {
		c.body.Blur()
	}
	c.sugg = nil
}

func (m *Model) layoutComposer() {
	c := m.comp
	if c == nil || m.w == 0 {
		return
	}
	w := m.w - m.listWidth() - 4
	for i := range c.inputs {
		c.inputs[i].Width = max(10, w-12)
	}
	c.subject.Width = max(10, w-12)
	c.body.SetWidth(max(10, w-2))
	c.body.SetHeight(max(3, m.bodyHeight()-2-c.headerRows()-c.quotedRows()))
}

func (c *composer) headerRows() int {
	return 8 + len(c.sugg) + map[bool]int{true: 2, false: 0}[len(c.sugg) > 0]
}

func (c *composer) quotedRows() int {
	if c.quoted == "" {
		return 0
	}
	if c.showQuoted {
		return min(12, strings.Count(c.quoted, "\n")+2)
	}
	return 2
}

// the address being typed in a recipient field, if it is one
func typedRecip(s string) (recip, bool) {
	s = strings.Trim(strings.TrimSpace(s), ",;")
	if s == "" {
		return recip{}, false
	}
	if a, err := mail.ParseAddress(s); err == nil {
		return recip{a.Name, strings.ToLower(a.Address)}, true
	}
	return recip{}, false
}

func (c *composer) added() map[string]bool {
	seen := map[string]bool{}
	for _, list := range c.rcpt {
		for _, r := range list {
			seen[strings.ToLower(r.Addr)] = true
		}
	}
	return seen
}

// accept adds the highlighted suggestion, or what was typed when it's an
// address. Reports whether something was added.
func (c *composer) accept() bool {
	f := c.focus
	if f > fBcc {
		return false
	}
	var r recip
	ok := false
	if len(c.sugg) > 0 {
		s := c.sugg[c.suggSel]
		r, ok = recip{s.Name, s.Addr}, true
	} else {
		r, ok = typedRecip(c.inputs[f].Value())
	}
	if !ok {
		return false
	}
	if !c.added()[strings.ToLower(r.Addr)] {
		c.rcpt[f] = append(c.rcpt[f], r)
	}
	c.inputs[f].SetValue("")
	c.sugg, c.suggSel = nil, 0
	return true
}

func (m *Model) suggest() tea.Cmd {
	c := m.comp
	if c.focus > fBcc {
		c.sugg = nil
		return nil
	}
	q := strings.TrimSpace(c.inputs[c.focus].Value())
	if q == "" {
		c.sugg = nil
		m.layoutComposer()
		return nil
	}
	c.sugg, _ = m.a.Store.SuggestContacts(c.account, q, 6, c.added())
	c.suggSel = min(c.suggSel, max(0, len(c.sugg)-1))
	m.layoutComposer()
	if len([]rune(q)) < 2 || strings.Contains(q, "@") {
		return nil
	}
	// the directory too, once typing pauses
	return tea.Tick(350*time.Millisecond, func(time.Time) tea.Msg { return dirTickMsg{q} })
}

func (m *Model) dirLookup(q string) tea.Cmd {
	c := m.comp
	if c == nil || c.focus > fBcc || strings.TrimSpace(c.inputs[c.focus].Value()) != q || c.dirAsked == q {
		return nil
	}
	c.dirAsked = q
	a, acc := m.a, c.account
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		found, _ := a.SearchDirectory(ctx, acc, q)
		return dirMsg{q, found}
	}
}

func (m *Model) gotDirectory(msg dirMsg) {
	c := m.comp
	if c == nil || c.focus > fBcc || strings.TrimSpace(c.inputs[c.focus].Value()) != msg.q || len(msg.found) == 0 {
		return
	}
	// they're stored as contacts now: rank them with the rest
	c.sugg, _ = m.a.Store.SuggestContacts(c.account, msg.q, 6, c.added())
	m.layoutComposer()
}

// outgoing is the message as it stands.
func (c *composer) outgoing() store.Outgoing {
	out := c.base
	lists := [3][]string{}
	for i := range c.rcpt {
		for _, r := range c.rcpt[i] {
			lists[i] = append(lists[i], r.String())
		}
		if r, ok := typedRecip(c.inputs[i].Value()); ok {
			lists[i] = append(lists[i], r.String())
		}
	}
	out.To, out.Cc, out.Bcc = lists[0], lists[1], lists[2]
	out.Subject = strings.TrimSpace(c.subject.Value())
	out.Body = strings.TrimRight(c.body.Value(), "\n ")
	if c.quoted != "" {
		out.Body += "\n\n" + c.quoted
	}
	out.Files = c.files
	return out
}

func (c *composer) empty() bool {
	return strings.TrimSpace(c.body.Value()) == "" && len(c.files) == 0
}

func (m Model) composerKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	c := m.comp
	key := k.String()
	if key != "esc" {
		c.armed = false
	}
	switch key {
	case "ctrl+s":
		return m.composerSend()
	case "ctrl+d":
		if c.approveID > 0 {
			return m, nil
		}
		out, acc := c.outgoing(), c.account
		m.comp, m.mode = nil, modeNormal
		return m, m.act("saved as a Gmail draft", func(ctx context.Context) error {
			_, err := m.a.SaveDraft(ctx, acc, out)
			return err
		})
	case "ctrl+e":
		out := c.outgoing()
		out.Body = strings.TrimRight(c.body.Value(), "\n ")
		return m, m.editComposer(out)
	case "ctrl+o":
		return m, m.pickFiles("compose")
	case "ctrl+q":
		c.showQuoted = !c.showQuoted
		m.layoutComposer()
		return m, nil
	case "esc", "ctrl+c":
		if len(c.sugg) > 0 {
			c.sugg = nil
			m.layoutComposer()
			return m, nil
		}
		if c.empty() || c.armed {
			m.comp, m.mode = nil, modeNormal
			m.say("discarded")
			if c.approveID > 0 {
				m.mode = modeApprove
			}
			return m, nil
		}
		c.armed = true
		m.say("esc again discards · ctrl+d keeps it as a draft")
		return m, nil
	case "tab", "shift+tab":
		if key == "tab" && c.focus <= fBcc && strings.TrimSpace(c.inputs[c.focus].Value()) != "" {
			if c.accept() {
				m.layoutComposer()
				return m, nil
			}
		}
		if key == "tab" {
			c.focus = (c.focus + 1) % nFields
		} else {
			c.focus = (c.focus + nFields - 1) % nFields
		}
		c.applyFocus()
		m.layoutComposer()
		return m, nil
	}
	switch {
	case c.focus <= fBcc:
		in := &c.inputs[c.focus]
		switch key {
		case "up", "ctrl+p":
			if len(c.sugg) > 0 {
				c.suggSel = (c.suggSel + len(c.sugg) - 1) % len(c.sugg)
			}
			return m, nil
		case "down", "ctrl+n":
			if len(c.sugg) > 0 {
				c.suggSel = (c.suggSel + 1) % len(c.sugg)
			}
			return m, nil
		case "enter", ",", ";":
			if strings.TrimSpace(in.Value()) == "" {
				if key == "enter" {
					c.focus++
					c.applyFocus()
					m.layoutComposer()
				}
				return m, nil
			}
			if !c.accept() && key == "enter" {
				m.say("not an address: pick a suggestion with ↑↓, or type name@domain")
			}
			m.layoutComposer()
			return m, nil
		case "backspace":
			if in.Value() == "" && len(c.rcpt[c.focus]) > 0 {
				c.rcpt[c.focus] = c.rcpt[c.focus][:len(c.rcpt[c.focus])-1]
				return m, nil
			}
		}
		var cmd tea.Cmd
		*in, cmd = in.Update(k)
		return m, tea.Batch(cmd, m.suggest())
	case c.focus == fSubject:
		if key == "enter" || key == "down" {
			c.focus = fFiles
			c.applyFocus()
			return m, nil
		}
		var cmd tea.Cmd
		c.subject, cmd = c.subject.Update(k)
		return m, cmd
	case c.focus == fFiles:
		switch key {
		case "enter", "+", "a", "o":
			return m, m.pickFiles("compose")
		case "left", "h":
			c.fileSel = max(0, c.fileSel-1)
		case "right", "l":
			c.fileSel = min(max(0, len(c.files)-1), c.fileSel+1)
		case "backspace", "delete", "x", "d", "-":
			if len(c.files) > 0 {
				c.files = append(c.files[:c.fileSel], c.files[c.fileSel+1:]...)
				c.fileSel = min(c.fileSel, max(0, len(c.files)-1))
			}
		case "down", "j":
			c.focus = fBody
			c.applyFocus()
		case "up", "k":
			c.focus = fSubject
			c.applyFocus()
		}
		return m, nil
	default: // body
		var cmd tea.Cmd
		c.body, cmd = c.body.Update(k)
		return m, cmd
	}
}

func (m Model) composerSend() (tea.Model, tea.Cmd) {
	c := m.comp
	out := c.outgoing()
	if c.approveID > 0 {
		// an agent's message, edited: back to the approval with it
		files, err := m.a.ResolveFiles(out.Files)
		if err != nil {
			m.setErr(err.Error())
			return m, nil
		}
		out.Files = files
		_ = m.a.Store.UpdateOutgoing(c.approveID, out)
		for i := range m.waiting {
			if m.waiting[i].ID == c.approveID {
				m.waiting[i].Out = out
			}
		}
		m.comp, m.mode = nil, modeApprove
		m.say("changes kept · y sends it")
		return m, nil
	}
	if len(out.To)+len(out.Cc)+len(out.Bcc) == 0 {
		c.focus = fTo
		c.applyFocus()
		m.setErr("add someone to send it to")
		return m, nil
	}
	if out.Subject == "" && c.base.ThreadID == "" {
		c.focus = fSubject
		c.applyFocus()
		m.say("no subject · ctrl+s again sends anyway")
		c.base.Subject = " " // second ctrl+s goes through
		return m, nil
	}
	it, err := m.a.Queue(c.account, "mail", out, app.OriginYou)
	if err != nil {
		m.setErr(err.Error())
		return m, nil
	}
	m.comp, m.mode = nil, modeNormal
	return m, m.sent(it)
}

// editComposer hands the message to $EDITOR and brings it back.
func (m *Model) editComposer(out store.Outgoing) tea.Cmd {
	path, err := cli.TempFile(m.a, "eml", cli.MailTemplate(out, ""))
	if err != nil {
		m.setErr(err.Error())
		return nil
	}
	base := out
	base.Body = ""
	return execEditor(m, path, func(err error) tea.Msg {
		return editedMsg{kind: "mail", account: m.comp.account, path: path, base: base, toComposer: true, err: err}
	})
}

// back from $EDITOR: the fields and body as written there
func (m *Model) composerFromEditor(out store.Outgoing) {
	c := m.comp
	if c == nil {
		return
	}
	for i, list := range [][]string{out.To, out.Cc, out.Bcc} {
		c.rcpt[i] = nil
		for _, a := range list {
			c.rcpt[i] = append(c.rcpt[i], m.recipFor(c.account, a))
		}
		c.inputs[i].SetValue("")
	}
	c.subject.SetValue(out.Subject)
	c.body.SetValue(out.Body)
	c.files = out.Files
	m.mode = modeCompose
	m.layoutComposer()
}

// ------------------------------------------------------------------ view

func (m Model) composerView(w, h int) string {
	c := m.comp
	s := m.styles
	labelW := 9
	label := func(f int) string {
		name := fieldNames[f]
		if f == c.focus {
			return s.accent.Bold(true).Render("▎" + fmt.Sprintf("%-*s", labelW-1, name))
		}
		return s.muted.Render(" " + fmt.Sprintf("%-*s", labelW-1, name))
	}
	var lines []string
	lines = append(lines, s.muted.Render(" "+fmt.Sprintf("%-*s", labelW-1, "From"))+s.sub.Render(c.from))
	for f := fTo; f <= fBcc; f++ {
		var parts []string
		for _, r := range c.rcpt[f] {
			col := s.c.Accent
			if !m.a.Store.KnownContact(c.account, r.Addr) {
				col = s.c.Peach // never written to: worth a second look
			}
			parts = append(parts, m.chip(r.label(), col))
		}
		chips := strings.Join(parts, " ")
		in := ""
		if f == c.focus || c.inputs[f].Value() != "" {
			ti := c.inputs[f] // a copy: sized to what the chips leave
			ti.Width = max(6, w-labelW-lipgloss.Width(chips)-3)
			if len(parts) > 0 {
				ti.Placeholder = ""
			}
			in = ti.View()
		}
		row := strings.TrimSpace(chips + " " + in)
		lines = append(lines, label(f)+ansi.Truncate(row, w-labelW, "…"))
		if f == c.focus && len(c.sugg) > 0 {
			lines = append(lines, m.suggestBox(w-labelW)...)
		}
	}
	lines = append(lines, label(fSubject)+c.subject.View())
	var fl []string
	for i, f := range c.files {
		name := f.Name
		if name == "" {
			name = lastPart(f.Source)
		}
		chip := "📎 " + name
		if c.focus == fFiles && i == c.fileSel {
			fl = append(fl, s.accent.Bold(true).Render("["+chip+"]"))
		} else {
			fl = append(fl, s.peach.Render(chip))
		}
	}
	hint := s.muted.Render("ctrl+o adds")
	if c.focus == fFiles {
		hint = s.muted.Render("enter adds · x removes")
	}
	lines = append(lines, label(fFiles)+ansi.Truncate(strings.TrimSpace(strings.Join(fl, "  ")+"  "+hint), w-labelW, "…"))
	lines = append(lines, s.muted.Render(strings.Repeat("─", w)))
	for _, l := range strings.Split(c.body.View(), "\n") {
		lines = append(lines, " "+l)
	}
	if c.quoted != "" {
		ql := strings.Split(strings.TrimRight(c.quoted, "\n"), "\n")
		if c.showQuoted {
			lines = append(lines, s.muted.Render(strings.Repeat("┄", w)))
			for i, l := range ql {
				if i >= 10 {
					lines = append(lines, s.muted.Render(fmt.Sprintf(" … %d more lines", len(ql)-10)))
					break
				}
				lines = append(lines, s.muted.Render(" "+ansi.Truncate(l, w-2, "…")))
			}
		} else {
			lines = append(lines, "", s.muted.Render(fmt.Sprintf(" ▸ %d quoted lines (ctrl+q shows them)", len(ql))))
		}
	}
	return strings.Join(lines, "\n")
}

func lastPart(p string) string {
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}

func (m Model) suggestBox(w int) []string {
	c := m.comp
	s := m.styles
	bw := min(w, 64)
	b := s.muted
	out := []string{strings.Repeat(" ", 9) + b.Render("╭"+strings.Repeat("─", bw-2)+"╮")}
	for i, ct := range c.sugg {
		name := ct.Name
		if name == "" {
			name = ct.Addr
		}
		meta := ""
		if ct.Sent > 0 {
			meta = fmt.Sprintf("wrote %d×", ct.Sent)
		}
		left := name + "  " + s.muted.Render(ct.Addr)
		if i == c.suggSel {
			left = s.accent.Bold(true).Render("▸ "+name) + "  " + s.sub.Render(ct.Addr)
		} else {
			left = "  " + s.text.Render(name) + "  " + s.muted.Render(ct.Addr)
		}
		line := row(left, s.muted.Render(meta), bw-4)
		out = append(out, strings.Repeat(" ", 9)+b.Render("│")+" "+line+" "+b.Render("│"))
	}
	return append(out, strings.Repeat(" ", 9)+b.Render("╰"+strings.Repeat("─", bw-2)+"╯"))
}
