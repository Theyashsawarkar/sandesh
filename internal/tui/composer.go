package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"net/mail"
	"os"
	"path/filepath"
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

// The compose screen. A new mail takes the whole width; a reply keeps the
// message it answers on the left. Recipients are pills with autocomplete
// from your contacts and the Workspace directory, Cc/Bcc show when needed,
// your Gmail signature sits under the body, and the draft is saved on
// disk every few seconds so a closed popup loses nothing.

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
	showCC        bool
	chipSel       int // a selected pill in the focused field (-1: none)
	focus         int
	sugg          []store.Contact
	suggSel       int
	approveID     int64 // editing an agent's message
	armed         bool  // Esc (or a subject-less Ctrl+s) pressed once
	dirAsked      string
	sig           string
	sigOn         bool
	reply         bool     // the original is shown on the left
	orig          []string // ...rendered for it
	origScroll    int
	draftKey      string
	dirty         bool
	savedAt       time.Time
}

type dirMsg struct {
	q     string
	found []store.Contact
}

type dirTickMsg struct{ q string }

type sigMsg struct{ account, sig string }

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
	c := &composer{account: account, title: title, base: out, quoted: quoted, approveID: approveID, files: out.Files, chipSel: -1, sigOn: true}
	c.reply = out.ThreadID != "" && approveID == 0
	c.draftKey = "new"
	if out.ThreadID != "" {
		c.draftKey = "re-" + out.ThreadID
	}
	if strings.HasPrefix(strings.ToLower(out.Subject), "fwd:") {
		c.draftKey = "fwd-" + mime.SafeName(out.Subject)
	}
	for i := range c.inputs {
		c.inputs[i] = newInput(s)
	}
	c.inputs[fTo].Placeholder = "name or address"
	c.subject = newInput(s)
	c.subject.Placeholder = "what it's about"
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
	c.body = ta
	m.comp = c
	m.mode = modeCompose
	c.load(m, out)
	restored := false
	if approveID == 0 {
		restored = m.restoreDraft()
	}
	c.setAccount(m, c.account)
	c.showCC = c.showCC || len(c.rcpt[fCc])+len(c.rcpt[fBcc]) > 0
	// start where there's something to write
	switch {
	case restored:
		c.focus = fBody
	case len(c.rcpt[fTo]) == 0:
		c.focus = fTo
	case c.subject.Value() == "":
		c.focus = fSubject
	default:
		c.focus = fBody
	}
	c.applyFocus()
	m.layoutComposer()
	if restored {
		m.say("continuing your unsent draft · esc esc discards it")
	}
	a, acc := m.a, c.account
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return sigMsg{acc, a.Signature(ctx, acc, true)}
	}
}

// load fills the form from a message.
func (c *composer) load(m *Model, out store.Outgoing) {
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
}

func (c *composer) setAccount(m *Model, account string) {
	c.account = account
	c.from = account
	if acc, err := m.a.Store.Account(account); err == nil && acc.Email != "" {
		c.from = acc.Email
	}
	c.sig = m.a.Signature(context.Background(), account, false)
}

// recipFor parses an address, naming it from your contacts when it has no
// name of its own.
func (m Model) recipFor(account, a string) recip {
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

func (c *composer) visible(f int) bool {
	return c.showCC || (f != fCc && f != fBcc)
}

func (c *composer) move(step int) {
	for i := 0; i < nFields; i++ {
		c.focus = (c.focus + step + nFields) % nFields
		if c.visible(c.focus) {
			break
		}
	}
	c.applyFocus()
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
	c.sugg, c.chipSel = nil, -1
}

// the panes: a reply leaves the list's width for the original
func (m Model) composeWidth() int {
	if m.comp != nil && m.comp.reply {
		return m.w - m.listWidth()
	}
	return m.w
}

func (m *Model) layoutComposer() {
	c := m.comp
	if c == nil || m.w == 0 {
		return
	}
	w := m.composeWidth() - 6
	for i := range c.inputs {
		c.inputs[i].Width = max(10, w-14)
	}
	c.subject.Width = max(10, w-14)
	c.body.SetWidth(max(10, w-4))
	c.body.SetHeight(max(3, m.bodyHeight()-2-c.chromeRows()))
	if c.reply && len(c.orig) == 0 {
		// the thread, drawn once at the left pane's width
		keep, links := m.preview, m.links
		m.buildPreview(m.listWidth() - 4)
		c.orig = m.preview
		m.preview, m.links = keep, links
	}
}

// rows taken by everything but the body text
func (c *composer) chromeRows() int {
	rows := 2 + 4 // header box borders + From, To, Cc row (or its hint), Subject
	rows++        // Files
	if c.showCC {
		rows++
	}
	if len(c.sugg) > 0 {
		rows += len(c.sugg) + 3
	}
	rows += 2 // body box borders
	if c.quoted != "" {
		if c.showQuoted {
			rows += min(12, strings.Count(c.quoted, "\n")+2)
		} else {
			rows++
		}
	}
	if c.sig != "" && c.sigOn {
		rows += min(3, strings.Count(c.sig, "\n")+2)
	}
	return rows + 1 // the status line
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
	c.dirty = true
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

func (m *Model) gotSignature(msg sigMsg) {
	if c := m.comp; c != nil && c.account == msg.account && msg.sig != c.sig {
		c.sig = msg.sig
		m.layoutComposer()
	}
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
	if c.sigOn && c.sig != "" {
		out.Body += "\n\n-- \n" + c.sig
	}
	if c.quoted != "" {
		out.Body += "\n\n" + c.quoted
	}
	out.Files = c.files
	return out
}

func (c *composer) empty() bool {
	return strings.TrimSpace(c.body.Value()) == "" && len(c.files) == 0
}

func (c *composer) words() int { return len(strings.Fields(c.body.Value())) }

// --------------------------------------------------------- local drafts

type savedDraft struct {
	Account string         `json:"account"`
	Title   string         `json:"title"`
	Base    store.Outgoing `json:"base"`
	Rcpt    [3][]recip     `json:"rcpt"`
	Subject string         `json:"subject"`
	Body    string         `json:"body"`
	Files   []store.File   `json:"files,omitempty"`
	Quoted  string         `json:"quoted,omitempty"`
	CC      bool           `json:"cc,omitempty"`
}

func (m *Model) draftPath(key string) string {
	return filepath.Join(m.a.Paths.State, "drafts", mime.SafeName(key)+".json")
}

// saveDraft keeps the message on disk (0600) until it is sent or dropped.
func (m *Model) saveDraft() {
	c := m.comp
	if c == nil || c.approveID > 0 || !c.dirty {
		return
	}
	c.dirty = false
	p := m.draftPath(c.draftKey)
	if c.empty() && len(c.rcpt[fTo]) == 0 {
		os.Remove(p)
		return
	}
	d := savedDraft{Account: c.account, Title: c.title, Base: c.base, Rcpt: c.rcpt, Subject: c.subject.Value(), Body: c.body.Value(),
		Files: c.files, Quoted: c.quoted, CC: c.showCC}
	b, _ := json.Marshal(d)
	_ = os.MkdirAll(filepath.Dir(p), 0o700)
	if os.WriteFile(p, b, 0o600) == nil {
		c.savedAt = time.Now()
	}
}

func (m *Model) restoreDraft() bool {
	c := m.comp
	b, err := os.ReadFile(m.draftPath(c.draftKey))
	if err != nil {
		return false
	}
	var d savedDraft
	if json.Unmarshal(b, &d) != nil {
		return false
	}
	c.account, c.base, c.rcpt, c.files, c.showCC = d.Account, d.Base, d.Rcpt, d.Files, d.CC
	if d.Quoted != "" {
		c.quoted = d.Quoted
	}
	c.subject.SetValue(d.Subject)
	c.body.SetValue(d.Body)
	c.savedAt = time.Now()
	return true
}

func (m *Model) dropDraft() {
	if m.comp != nil {
		os.Remove(m.draftPath(m.comp.draftKey))
	}
}

// ------------------------------------------------------------------ keys

func (m Model) composerKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	c := m.comp
	key := k.String()
	if key != "esc" && key != "ctrl+s" {
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
		m.dropDraft()
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
	case "ctrl+b":
		c.showCC = !c.showCC
		if c.showCC {
			c.focus = fCc
		} else if c.focus == fCc || c.focus == fBcc {
			c.focus = fSubject
		}
		c.applyFocus()
		m.layoutComposer()
		return m, nil
	case "ctrl+g":
		if c.sig != "" {
			c.sigOn = !c.sigOn
			c.dirty = true
			m.say(map[bool]string{true: "signature on", false: "signature off"}[c.sigOn])
			m.layoutComposer()
		}
		return m, nil
	case "ctrl+f":
		if len(m.accounts) > 1 {
			i := 0
			for j, a := range m.accounts {
				if a == c.account {
					i = j
				}
			}
			c.setAccount(&m, m.accounts[(i+1)%len(m.accounts)])
			c.dirty = true
			a, acc := m.a, c.account
			return m, func() tea.Msg {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				return sigMsg{acc, a.Signature(ctx, acc, true)}
			}
		}
		return m, nil
	case "pgup", "alt+k", "pgdown", "alt+j":
		if c.reply {
			step := max(3, m.bodyHeight()/2)
			if key == "pgup" || key == "alt+k" {
				step = -step
			}
			c.origScroll = max(0, min(c.origScroll+step, len(c.orig)-3))
		}
		return m, nil
	case "esc", "ctrl+c":
		if len(c.sugg) > 0 {
			c.sugg = nil
			m.layoutComposer()
			return m, nil
		}
		if c.empty() || c.armed {
			m.dropDraft()
			approve := c.approveID > 0
			m.comp, m.mode = nil, modeNormal
			m.say("discarded")
			if approve {
				m.mode = modeApprove
			}
			return m, nil
		}
		c.armed = true
		c.dirty = true
		m.saveDraft()
		m.say("esc again discards · ctrl+d keeps it as a Gmail draft")
		return m, nil
	case "tab", "shift+tab":
		if key == "tab" && c.focus <= fBcc && strings.TrimSpace(c.inputs[c.focus].Value()) != "" && !c.accept() {
			m.say("not an address: pick a suggestion with ↑↓, or type name@domain")
			return m, nil
		}
		step := 1
		if key == "shift+tab" {
			step = -1
		}
		c.move(step)
		m.layoutComposer()
		return m, nil
	}
	c.dirty = true
	switch {
	case c.focus <= fBcc:
		in := &c.inputs[c.focus]
		list := &c.rcpt[c.focus]
		empty := in.Value() == ""
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
		case "left":
			if empty && len(*list) > 0 {
				if c.chipSel < 0 {
					c.chipSel = len(*list) - 1
				} else {
					c.chipSel = max(0, c.chipSel-1)
				}
				return m, nil
			}
		case "right":
			if c.chipSel >= 0 {
				c.chipSel++
				if c.chipSel >= len(*list) {
					c.chipSel = -1
				}
				return m, nil
			}
		case "enter", ",", ";":
			if strings.TrimSpace(in.Value()) == "" {
				if key == "enter" {
					c.move(1)
					m.layoutComposer()
				}
				return m, nil
			}
			if !c.accept() && key == "enter" {
				m.say("not an address: pick a suggestion with ↑↓, or type name@domain")
			}
			m.layoutComposer()
			return m, nil
		case "backspace", "delete":
			if empty && len(*list) > 0 {
				i := c.chipSel
				if i < 0 {
					i = len(*list) - 1
				}
				*list = append((*list)[:i], (*list)[i+1:]...)
				c.chipSel = -1
				return m, nil
			}
		}
		c.chipSel = -1
		var cmd tea.Cmd
		*in, cmd = in.Update(k)
		return m, tea.Batch(cmd, m.suggest())
	case c.focus == fSubject:
		if key == "enter" || key == "down" {
			c.move(1)
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
			c.move(1)
		case "up", "k":
			c.move(-1)
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
	if out.Subject == "" && c.base.ThreadID == "" && !c.armed {
		c.focus = fSubject
		c.applyFocus()
		c.armed = true
		m.say("no subject · ctrl+s again sends anyway")
		return m, nil
	}
	it, err := m.a.Queue(c.account, "mail", out, app.OriginYou)
	if err != nil {
		m.setErr(err.Error())
		return m, nil
	}
	m.dropDraft()
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
	c.load(m, out)
	c.showCC = c.showCC || len(c.rcpt[fCc])+len(c.rcpt[fBcc]) > 0
	c.dirty = true
	m.mode = modeCompose
	m.layoutComposer()
}

// ------------------------------------------------------------------ view

const labelW = 10

// composerPanes draws the compose screen in place of the list and preview.
func (m Model) composerPanes(bh int) string {
	c := m.comp
	s := m.styles
	title := c.title
	if !c.savedAt.IsZero() {
		title += " · draft saved " + c.savedAt.Format("15:04")
	}
	cw := m.composeWidth()
	right := m.box(cw, bh, title, m.composerView(cw-2, bh-2), true, s.c.Accent)
	if !c.reply {
		return right
	}
	lw := m.w - cw
	lines := c.orig
	if c.origScroll < len(lines) {
		lines = lines[c.origScroll:]
	}
	left := m.box(lw, bh, "replying to · pgup/pgdn scroll", strings.Join(lines, "\n"), false, s.c.Border)
	return lipgloss.JoinHorizontal(lipgloss.Top, left, right)
}

func (m Model) composerView(w, h int) string {
	c := m.comp
	s := m.styles
	iw := w - 2 // inside the header box
	field := func(f int, label, value string) string {
		lb := s.muted.Render(" " + fmt.Sprintf("%-*s", labelW-1, label))
		if f == c.focus {
			lb = s.accent.Bold(true).Render("▎" + fmt.Sprintf("%-*s", labelW-1, label))
		}
		return lb + ansi.Truncate(value, iw-labelW-1, "…")
	}
	var head []string
	from := s.text.Render(c.from)
	if len(m.accounts) > 1 {
		from += s.muted.Render("  ctrl+f switches account")
	}
	head = append(head, field(-1, "From", from))
	var firstTime []string
	for f := fTo; f <= fBcc; f++ {
		if !c.visible(f) {
			if f == fCc {
				head = append(head, field(-1, "", s.sub.Render("+ Cc  + Bcc")+s.muted.Render("  ·  ctrl+b shows them")))
			}
			continue
		}
		var pills []string
		for i, r := range c.rcpt[f] {
			p := m.avatar(r.label()) + " " + s.text.Render(r.label())
			if !m.a.Store.KnownContact(c.account, r.Addr) {
				p += s.peach.Render(" first time")
				firstTime = append(firstTime, r.label())
			}
			if f == c.focus && i == c.chipSel {
				p = s.accent.Bold(true).Render("[") + p + s.accent.Bold(true).Render("]")
			} else {
				p = s.muted.Render("‹") + p + s.muted.Render("›")
			}
			pills = append(pills, p)
		}
		line := strings.Join(pills, " ")
		if f == c.focus || c.inputs[f].Value() != "" {
			ti := c.inputs[f] // a copy, sized to what the pills leave
			ti.Width = max(6, iw-labelW-lipgloss.Width(line)-4)
			if len(pills) > 0 {
				ti.Placeholder = ""
			}
			line = strings.TrimSpace(line + " " + ti.View())
		}
		head = append(head, field(f, fieldNames[f], line))
		if f == c.focus && len(c.sugg) > 0 {
			head = append(head, m.suggestBox(iw-labelW-2)...)
		}
	}
	head = append(head, field(fSubject, "Subject", c.subject.View()))
	var fl []string
	for i, f := range c.files {
		name, size := f.Name, f.Size
		if name == "" {
			name = filepath.Base(f.Source)
			if st, err := os.Stat(app.ExpandPath(f.Source)); err == nil {
				size = st.Size()
			}
		}
		item := s.peach.Render("▤ ") + s.text.Render(name)
		if size > 0 {
			item += s.muted.Render(" " + app.HumanSize(size))
		}
		if c.focus == fFiles && i == c.fileSel {
			item = s.accent.Bold(true).Render("[") + item + s.accent.Bold(true).Render(" x]")
		} else {
			item = s.muted.Render("‹") + item + s.muted.Render("›")
		}
		fl = append(fl, item)
	}
	hint := s.muted.Render("ctrl+o adds")
	if c.focus == fFiles {
		hint = s.muted.Render("enter adds · ←→ pick · x removes")
	}
	head = append(head, field(fFiles, "Files", strings.TrimSpace(strings.Join(fl, " ")+"  "+hint)))

	var out []string
	out = append(out, frame(head, w, s.c.Border)...)

	// the body box: text, then the folded quote and the signature
	bl := strings.Split(c.body.View(), "\n")
	if c.quoted != "" {
		ql := strings.Split(strings.TrimRight(c.quoted, "\n"), "\n")
		if c.showQuoted {
			bl = append(bl, s.muted.Render(strings.Repeat("┄", iw-2)))
			for i, l := range ql {
				if i >= 10 {
					bl = append(bl, s.muted.Render(fmt.Sprintf("… %d more lines", len(ql)-10)))
					break
				}
				bl = append(bl, s.muted.Render(ansi.Truncate(l, iw-2, "…")))
			}
		} else {
			first := strings.TrimSpace(ql[0])
			bl = append(bl, s.muted.Render("▸ "+ansi.Truncate(first, max(10, iw-34), "…")+fmt.Sprintf(" · %d lines  ", len(ql)))+s.muted.Render("ctrl+q shows it"))
		}
	}
	if c.sig != "" && c.sigOn {
		note := ""
		if c.focus == fBody {
			note = "   ctrl+g leaves your signature out"
		}
		bl = append(bl, s.muted.Render("-- "+note))
		for i, l := range strings.Split(c.sig, "\n") {
			if i >= 2 {
				break
			}
			bl = append(bl, s.muted.Render(ansi.Truncate(l, iw-2, "…")))
		}
	}
	border := s.c.Border
	if c.focus == fBody {
		border = s.c.Accent
	}
	out = append(out, frame(bl, w, border)...)

	// status and buttons
	n := 0
	for _, l := range c.rcpt {
		n += len(l)
	}
	left := s.muted.Render(fmt.Sprintf(" %d recipient%s · %d file%s · %d words", n, plural(n), len(c.files), plural(len(c.files)), c.words()))
	if len(firstTime) > 0 {
		left += s.peach.Render("  ● first time writing to " + strings.Join(firstTime, ", "))
	}
	btn := func(k, label string, primary bool) string {
		if primary {
			return lipgloss.NewStyle().Background(lipgloss.Color(s.c.Accent)).Foreground(lipgloss.Color(s.c.Base)).Bold(true).Padding(0, 1).Render(k + " " + label)
		}
		return s.keycap.Render(k) + s.muted.Render(" "+label)
	}
	buttons := []string{btn("ctrl+s", "send", true), btn("ctrl+d", "draft", false), btn("ctrl+e", "nvim", false), btn("esc", "discard", false)}
	if c.approveID > 0 {
		buttons = []string{btn("ctrl+s", "keep changes", true), btn("ctrl+e", "nvim", false), btn("esc", "back", false)}
	}
	// narrow: fewer buttons (the footer still lists the keys)
	for len(buttons) > 1 && lipgloss.Width(left)+lipgloss.Width(strings.Join(buttons, "  "))+3 > w {
		buttons = buttons[:len(buttons)-1]
	}
	out = append(out, row(left, strings.Join(buttons, "  ")+" ", w))
	return strings.Join(out, "\n")
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// frame draws a rounded box of width w around lines.
func frame(lines []string, w int, color string) []string {
	b := fg(color)
	out := []string{b.Render("╭" + strings.Repeat("─", w-2) + "╮")}
	for _, l := range lines {
		out = append(out, b.Render("│")+fit(" "+l, w-2)+b.Render("│"))
	}
	return append(out, b.Render("╰"+strings.Repeat("─", w-2)+"╯"))
}

func (m Model) suggestBox(w int) []string {
	c := m.comp
	s := m.styles
	bw := min(w, 78)
	b := fg(s.c.Accent)
	q := strings.ToLower(strings.TrimSpace(c.inputs[c.focus].Value()))
	hl := func(text string, base lipgloss.Style) string {
		i := strings.Index(strings.ToLower(text), q)
		if q == "" || i < 0 || i+len(q) > len(text) {
			return base.Render(text)
		}
		return base.Render(text[:i]) + s.accent.Bold(true).Render(text[i:i+len(q)]) + base.Render(text[i+len(q):])
	}
	pad := strings.Repeat(" ", labelW)
	out := []string{pad + b.Render("╭"+strings.Repeat("─", bw-2)+"╮")}
	for i, ct := range c.sugg {
		name := ct.Name
		if name == "" {
			name = strings.Split(ct.Addr, "@")[0]
		}
		var meta string
		switch {
		case ct.Sent > 0:
			meta = fmt.Sprintf("wrote %d×", ct.Sent)
			if ct.Last > 0 {
				meta += " · " + ago(ct.Last)
			}
			meta = s.muted.Render(meta)
		case ct.Received > 0:
			meta = s.muted.Render(fmt.Sprintf("wrote you %d×", ct.Received))
		default:
			meta = fg(s.c.Blue).Render("directory")
		}
		mark := "  "
		nameStyle := s.text
		if i == c.suggSel {
			mark = s.accent.Bold(true).Render("▸ ")
			nameStyle = s.bold
		}
		left := mark + m.avatar(name) + " " + hl(name, nameStyle) + "  " + hl(ct.Addr, s.muted)
		out = append(out, pad+b.Render("│")+fit(" "+row(left, meta, bw-4)+" ", bw-2)+b.Render("│"))
	}
	out = append(out, pad+b.Render("│")+fit(s.muted.Render(" ↑↓ pick · tab or enter adds · esc closes"), bw-2)+b.Render("│"))
	return append(out, pad+b.Render("╰"+strings.Repeat("─", bw-2)+"╯"))
}

func ago(ms int64) string {
	d := time.Since(time.UnixMilli(ms))
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", max(1, int(d.Minutes())))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 60*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
	return time.UnixMilli(ms).Format("Jan 2006")
}
