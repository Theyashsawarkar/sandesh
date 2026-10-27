// Package tui is the sandesh screen: sections on top, threads or spaces
// on the left, the selected one on the right, keys only.
package tui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Theyashsawarkar/sandesh/internal/app"
	"github.com/Theyashsawarkar/sandesh/internal/cli"
	"github.com/Theyashsawarkar/sandesh/internal/config"
	"github.com/Theyashsawarkar/sandesh/internal/store"
)

type mode int

const (
	modeNormal mode = iota
	modeFilter
	modeAsk
	modeSearch
	modeSaveName
	modeDraftHint
	modeReview
	modeApprove
	modeHelp
	modeChatMsg // typing a chat message in the preview
	modeLink    // typing a link number after o
	modeRename  // typing a chat's new name
	modePick    // the file picker
	modeWhen    // picking when to send
	modeConvo   // picking people for a new conversation
	modeCompose // the compose screen
	modeFind    // the search window
)

const (
	tabMail = iota
	tabChat
)

var chatSections = []string{"All", "Unread", "DMs", "Spaces", "Scheduled"}

type undo struct {
	what     string // archive, trash, read, unread, star, unstar, send
	account  string
	ids      []string
	outboxID int64
}

type review struct {
	kind      string // mail | chat
	account   string
	out       store.Outgoing
	replaceID int64 // editing a waiting send: it makes way on send
}

type Model struct {
	a      *app.App
	launch string // "", popup, demo
	styles styles
	w, h   int

	tab      int
	sections []config.Section
	sec      int
	search   *config.Section // a server search shown as a temporary section
	accounts []string
	acct     int // -1: all

	threads  []store.Thread
	spaces   []store.Space
	counts   map[string]int // unread per mail section
	chatN    int
	sel      int
	offset   int
	focus    int // 0 list, 1 preview
	scroll   int
	selected map[string]bool

	// preview of the current item
	curKey     string
	msgs       []store.Message
	chatMsgs   []store.ChatMessage
	preview    []string // rendered lines (buildPreview)
	links      []string // the preview's numbered links
	expanded   map[string]bool
	expandAll  bool
	showQuoted bool
	labelNames map[string]string

	mode          mode
	input         []rune
	filter        string
	ai            string
	aiTitle       string
	aiBusy        bool
	toast         string
	toastAt       time.Time
	undo          *undo
	review        *review
	older         store.Backfill // the download of older mail, all accounts
	olderN        int            // how many "load older" fetches
	loadingOlder  bool
	noOlder       map[string]bool             // sections with nothing older on Gmail
	dbStamp       string                      // when the local copy last changed
	chatScheduled int                         // scheduled chat messages, for the tab
	replaceNext   int64                       // the waiting send the editor is changing
	sentStat      map[string]store.SentStatus // Sent: did they answer?
	outboxRows    map[string]store.OutboxItem // Sent: rows still in the outbox
	comp          *composer
	find          *finder
	flashMsg      string         // a chat message opened from search: marked
	chatMsgLine   map[string]int // where each chat message starts in the preview
	pick          *picker
	when          *whenPicker
	convo         *convoPicker
	chatSel       int                // the message picked in a chat (when the conversation has focus)
	replyTo       *store.ChatMessage // the message the one being typed replies to
	pickMode      mode               // where the picker goes back to
	helpQ         string             // filtering the keys popup
	helpTop       int
	waiting       []store.OutboxItem
	syncing       bool
	synced        time.Time
	err           string
	errAt         time.Time
	atBottom      bool // the preview is scrolled to its end
	themeStamp    string
	lastSync      time.Time
}

func New(a *app.App, launch string) Model {
	m := Model{a: a, launch: launch, styles: newStyles(a.Config.Colors, a.Config.NerdFont), acct: -1, selected: map[string]bool{}, counts: map[string]int{}, noOlder: map[string]bool{}, olderN: a.Config.OlderPage,
		expanded: map[string]bool{}}
	m.sections = a.Sections()
	m.accounts = a.AccountNames()
	m.reload()
	return m
}

// Run opens the screen.
// Run opens the screen, or, with no account yet, the setup screen that
// takes the credentials and signs one in first.
func Run(a *app.App, launch string) error {
	var first tea.Model = New(a, launch)
	if len(a.AccountNames()) == 0 {
		first = newSetup(a, launch)
	}
	_, err := tea.NewProgram(first, tea.WithAltScreen()).Run()
	if err == nil && demoWanted {
		demoWanted = false
		return app.ErrDemo
	}
	return err
}

// ------------------------------------------------------------------ data

func (m *Model) accountFilter() []string {
	if m.acct < 0 || m.acct >= len(m.accounts) {
		return nil
	}
	return []string{m.accounts[m.acct]}
}

func (m *Model) currentSection() config.Section {
	if m.search != nil {
		return *m.search
	}
	if m.sec >= len(m.sections) {
		m.sec = 0
	}
	return m.sections[m.sec]
}

// reload reads the list from the local copy, keeping the selection on the
// same item when it is still there.
func (m *Model) reload() {
	keep := m.curKey
	accs := m.accountFilter()
	if m.search == nil {
		for _, s := range m.sections {
			ts, _ := m.a.SectionThreads(s, accs)
			n := 0
			for _, t := range ts {
				if t.Unread() {
					n++
				}
			}
			m.counts[s.Name] = n
			if s.Label == "SCHEDULED" {
				m.counts[s.Name] = len(m.outboxItems(accs, "mail", true))
			}
		}
	}
	sps, _ := m.a.Store.Spaces(accs, nil)
	m.chatN = 0
	for _, s := range sps {
		if s.Unread > 0 {
			m.chatN++
		}
	}
	m.chatScheduled = len(m.outboxItems(accs, "chat", true))
	if m.tab == tabMail {
		ts, err := m.a.SectionThreads(m.currentSection(), accs)
		if err != nil {
			m.err = err.Error()
		}
		if m.filter != "" {
			ts = filterThreads(ts, m.filter)
		}
		m.sentStat, m.outboxRows = nil, nil
		if m.search == nil && m.currentSection().Label == "SCHEDULED" {
			ts = m.outboxThreads(m.outboxItems(accs, "mail", true))
		}
		if m.search == nil && m.currentSection().Label == "SENT" {
			ts = append(m.outboxThreads(m.outboxItems(accs, "mail", false)), ts...)
			m.sentStat = map[string]store.SentStatus{}
			byAcc := map[string][]string{}
			for _, t := range ts {
				byAcc[t.Account] = append(byAcc[t.Account], t.ID)
			}
			for acc, ids := range byAcc {
				for id, st := range m.a.Store.SentStatuses(acc, ids) {
					m.sentStat[acc+"/"+id] = st
				}
			}
		}
		m.threads = ts
	} else if chatSections[m.sec%len(chatSections)] == "Scheduled" {
		m.outboxRows = map[string]store.OutboxItem{}
		types := map[string]string{}
		for _, s := range sps {
			types[s.Account+"/"+s.Name] = s.Type
		}
		var out []store.Space
		for _, it := range m.outboxItems(accs, "chat", true) {
			id := fmt.Sprintf("%s%d", outboxPrefix, it.ID)
			m.outboxRows[id] = it
			out = append(out, store.Space{Account: it.Account, Name: id, DisplayName: it.Out.SpaceName, Type: types[it.Account+"/"+it.Out.Space],
				LastActive: it.Out.ScheduledFor, LastText: oneLine(it.Out.Body), LastSender: "You"})
		}
		m.spaces = out
	} else {
		m.outboxRows = nil
		var out []store.Space
		for _, s := range sps {
			switch chatSections[m.sec%len(chatSections)] {
			case "Unread":
				if s.Unread == 0 {
					continue
				}
			case "DMs":
				if s.Type != "DIRECT_MESSAGE" {
					continue
				}
			case "Spaces":
				if s.Type == "DIRECT_MESSAGE" {
					continue
				}
			}
			if m.filter != "" && !strings.Contains(strings.ToLower(cli.SpaceTitle(s)+" "+s.LastText), strings.ToLower(m.filter)) {
				continue
			}
			out = append(out, s)
		}
		m.spaces = out
	}
	m.waiting, _ = m.a.Store.Outbox(store.StatusApproval)
	// how far the download of older mail has come
	m.older = store.Backfill{Done: true}
	if _, on := app.KeepQuery(m.a.Config.Keep); on {
		for _, acc := range m.a.AccountNames() {
			if b := m.a.Store.Backfill(acc); !b.Done {
				m.older.Done = false
				m.older.Have += b.Have
				m.older.Total += b.Total
			}
		}
	}
	// keep the selection
	for i := 0; i < m.count(); i++ {
		if m.keyAt(i) == keep {
			m.sel = i
			break
		}
	}
	m.clamp()
	follow := m.tab == tabChat && m.atBottom
	m.loadPreview(m.curKey != keep)
	if follow {
		m.scroll = 1 << 30 // new messages: stay at the bottom, like a chat app
	}
	m.fixView()
}

func filterThreads(ts []store.Thread, f string) []store.Thread {
	f = strings.ToLower(f)
	var out []store.Thread
	for _, t := range ts {
		if strings.Contains(strings.ToLower(t.Subject+" "+t.Participants+" "+t.LastFrom+" "+t.Snippet), f) {
			out = append(out, t)
		}
	}
	return out
}

func (m *Model) count() int {
	if m.tab == tabMail {
		return len(m.threads)
	}
	return len(m.spaces)
}

func (m *Model) keyAt(i int) string {
	if m.tab == tabMail {
		if i < len(m.threads) {
			return m.threads[i].Account + "/" + m.threads[i].ID
		}
		return ""
	}
	if i < len(m.spaces) {
		return m.spaces[i].Account + "/" + m.spaces[i].Name
	}
	return ""
}

func (m *Model) clamp() {
	if m.sel >= m.count() {
		m.sel = m.count() - 1
	}
	if m.sel < 0 {
		m.sel = 0
	}
}

func (m *Model) curThread() (store.Thread, bool) {
	if m.tab != tabMail || m.sel >= len(m.threads) || strings.HasPrefix(m.threads[m.sel].ID, outboxPrefix) {
		return store.Thread{}, false
	}
	return m.threads[m.sel], true
}

type olderMsg struct {
	section string
	n       int
	err     error
}

// loadOlderCmd fetches the next older batch of the section from Gmail.
func (m *Model) loadOlderCmd() tea.Cmd {
	sec := m.currentSection()
	if m.loadingOlder || m.noOlder[sec.Name] || sec.Today || sec.Label == "SCHEDULED" {
		return nil
	}
	oldest := map[string]int64{}
	for _, t := range m.threads {
		if strings.HasPrefix(t.ID, outboxPrefix) {
			continue
		}
		if o, ok := oldest[t.Account]; !ok || t.LastDate < o {
			oldest[t.Account] = t.LastDate
		}
	}
	accs := m.accountFilter()
	if len(accs) == 0 {
		accs = m.accounts
	}
	m.loadingOlder = true
	a, n := m.a, m.olderN
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		total := 0
		for _, acc := range accs {
			got, err := a.LoadOlder(ctx, acc, sec, oldest[acc], n)
			if err != nil {
				return olderMsg{sec.Name, total, err}
			}
			total += got
		}
		return olderMsg{sec.Name, total, nil}
	}
}

// Sent shows mail still in the outbox (sending, failed) above what went out.
const outboxPrefix = "outbox:"

func (m *Model) curOutbox() (store.OutboxItem, bool) {
	key := ""
	switch {
	case m.tab == tabMail && m.sel < len(m.threads):
		key = m.threads[m.sel].ID
	case m.tab == tabChat && m.sel < len(m.spaces):
		key = m.spaces[m.sel].Name
	}
	it, ok := m.outboxRows[key]
	return it, ok
}

// outboxItems: what's still in the outbox, of one kind; scheduled ones,
// or the rest (sending, in the undo window, waiting for approval, failed
// this week), soonest first.
func (m *Model) outboxItems(accs []string, kind string, scheduled bool) []store.OutboxItem {
	items, _ := m.a.Store.Outbox(store.StatusApproval, store.StatusQueued, store.StatusSending, store.StatusFailed)
	var out []store.OutboxItem
	for _, it := range items {
		isSched := it.Status == store.StatusQueued && it.Out.ScheduledFor > 0
		if it.Kind != kind || isSched != scheduled || (len(accs) > 0 && !slices.Contains(accs, it.Account)) ||
			it.Status == store.StatusFailed && time.Since(time.UnixMilli(it.Created)) > 7*24*time.Hour {
			continue
		}
		out = append(out, it)
	}
	if scheduled {
		sort.SliceStable(out, func(i, j int) bool { return out[i].SendAt < out[j].SendAt })
	}
	return out
}

func (m *Model) outboxThreads(items []store.OutboxItem) []store.Thread {
	m.outboxRows = map[string]store.OutboxItem{}
	var out []store.Thread
	for _, it := range items {
		id := fmt.Sprintf("%s%d", outboxPrefix, it.ID)
		m.outboxRows[id] = it
		subject := it.Out.Subject
		if it.Kind == "chat" {
			subject = "chat message"
		}
		out = append(out, store.Thread{Account: it.Account, ID: id, Subject: subject, Participants: strings.Join(it.Out.To, ", "),
			Snippet: oneLine(it.Out.Body), LastDate: max(it.SendAt, it.Created)})
	}
	return out
}

func (m *Model) curSpace() (store.Space, bool) {
	if m.tab != tabChat || m.sel >= len(m.spaces) || strings.HasPrefix(m.spaces[m.sel].Name, outboxPrefix) {
		return store.Space{}, false
	}
	return m.spaces[m.sel], true
}

// loadPreview reads the selected item's messages; reset scrolls to the
// top (mail) or the bottom (chat).
func (m *Model) loadPreview(reset bool) {
	key := m.keyAt(m.sel)
	if key != m.curKey {
		reset = true
	}
	m.curKey = key
	m.msgs, m.chatMsgs = nil, nil
	if t, ok := m.curThread(); ok {
		m.msgs, _ = m.a.Store.Messages(t.Account, t.ID)
	}
	if s, ok := m.curSpace(); ok {
		m.chatMsgs, _ = m.a.Store.ChatMessages(s.Account, s.Name, 200)
	}
	if reset {
		m.expandAll, m.showQuoted = false, false
		m.scroll = 0
		if m.tab == tabChat {
			m.scroll = 1 << 30 // clamped to the bottom when drawn
		}
	}
}

// targets: the selected threads, or the current one.
func (m *Model) targets() []store.Thread {
	var out []store.Thread
	for _, t := range m.threads {
		if m.selected[t.Account+"/"+t.ID] {
			out = append(out, t)
		}
	}
	if len(out) == 0 {
		if t, ok := m.curThread(); ok {
			out = append(out, t)
		}
	}
	return out
}

// pickingMsg: the conversation has focus, so j/k pick messages.
func (m *Model) pickingMsg() bool {
	_, ok := m.curSpace()
	return ok && m.focus == 1 && len(m.chatMsgs) > 0
}

// moveChatSel picks another message and keeps it in view.
func (m *Model) moveChatSel(step int) {
	m.chatSel = max(0, min(len(m.chatMsgs)-1, m.chatSel+step))
	m.fixView()
	if line, ok := m.chatMsgLine[m.chatMsgs[m.chatSel].Name]; ok {
		avail := m.bodyHeight() - 6
		if line < m.scroll+1 || line > m.scroll+avail-3 {
			m.scroll = max(0, line-avail/2)
			m.fixView()
		}
	}
}

// replyTarget: the picked message, or else the latest one from someone else.
func (m *Model) replyTarget() *store.ChatMessage {
	if m.pickingMsg() && m.chatSel < len(m.chatMsgs) {
		c := m.chatMsgs[m.chatSel]
		return &c
	}
	for i := len(m.chatMsgs) - 1; i >= 0; i-- {
		if !m.chatMsgs[i].Mine {
			c := m.chatMsgs[i]
			return &c
		}
	}
	return nil
}

// quoteInto makes the message a reply to replyTo by quoting it, the way
// Chat's own reply does: it stays in the conversation, not in a thread
// (Google reports every space as threaded, so that can't decide it).
func (m *Model) quoteInto(out *store.Outgoing, sp store.Space) {
	if r := m.replyTo; r != nil && r.Space == sp.Name {
		out.Quote = r.Name
	}
}

// recolour picks up a theme change (varna's palette, the config or its
// include) without a restart.
func (m *Model) recolour() {
	stamp := config.ThemeStamp(m.a.Paths, m.a.Config)
	if stamp == m.themeStamp {
		return
	}
	first := m.themeStamp == ""
	m.themeStamp = stamp
	if first {
		return
	}
	if c, err := config.Load(m.a.Paths); err == nil {
		m.a.Config.Colors = c.Colors
		m.a.Config.Include = c.Include
		m.styles = newStyles(c.Colors, m.a.Config.NerdFont)
		m.fixView()
	}
}

// sent follows up a queued send: the undo window, or straight out (chat,
// by default), jumping to the end of the chat either way.
func (m *Model) sent(it store.OutboxItem) tea.Cmd {
	if it.Kind == "chat" {
		m.scroll = 1 << 30
		m.fixView()
	}
	if it.Status != store.StatusQueued {
		m.say("waiting for approval")
		return nil
	}
	if it.Out.ScheduledFor > 0 {
		m.say("scheduled for " + app.SendTime(time.UnixMilli(it.Out.ScheduledFor), time.Now()) + " · in Scheduled: t moves it, z cancels")
		if !m.a.DaemonRunning() {
			m.setErr("the background service isn't running, and it's what sends scheduled mail: sandesh service install")
		}
		return nil
	}
	if w := m.a.UndoWindow(it.Kind); w > 0 {
		m.undo = &undo{what: "send", outboxID: it.ID}
		m.say(fmt.Sprintf("sending in %ds · z cancels", int(w.Seconds())))
		return nil
	}
	m.say("sending…")
	a := m.a
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		for _, d := range a.DeliverDue(ctx) {
			if d.ID == it.ID && d.Status == store.StatusFailed {
				return doneMsg{err: fmt.Errorf("not sent: %s", d.Error)}
			}
		}
		return doneMsg{msg: "sent", sync: true}
	}
}

// setErr shows an error on its own line for a while (Esc clears it).
func (m *Model) setErr(e string) {
	m.err, m.errAt = e, time.Now()
}

func (m *Model) say(s string) {
	m.toast = s
	m.toastAt = time.Now()
}

// ------------------------------------------------------------- messages

type tickMsg time.Time
type syncedMsg struct{ errs map[string]error }
type doneMsg struct {
	msg  string
	err  error
	sync bool // sync soon after (a send went out)
}
type aiMsg struct {
	title, text string
	err         error
	draftFor    *review // D: open the editor on this reply with the AI's text
}
type editedMsg struct {
	kind, account, path string
	base                store.Outgoing
	approveID           int64
	toComposer          bool // back to the compose screen, not review
	err                 error
}
type searchMsg struct {
	q       string
	threads []store.Thread
	err     error
}

func tick() tea.Cmd { return tea.Tick(time.Second, func(t time.Time) tea.Msg { return tickMsg(t) }) }

// Init shows the local copy at once. The background service keeps it
// fresh; only when it isn't running does the screen sync by itself.
func (m Model) Init() tea.Cmd {
	if m.a.DaemonRunning() {
		return tick()
	}
	return tea.Batch(tick(), m.syncCmd())
}

type syncSoonMsg struct{}

// syncSoon asks for a quick sync shortly after a change (a send, an
// archive...), so the copy shows what Google now has. Gmail's change feed
// makes it cheap; the per-account lock keeps it from doubling the
// daemon's work.
func (m *Model) syncSoon() tea.Cmd {
	return tea.Tick(2*time.Second, func(time.Time) tea.Msg { return syncSoonMsg{} })
}

func (m *Model) syncCmd() tea.Cmd {
	if m.syncing {
		return nil
	}
	m.syncing = true
	a := m.a
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		_, errs := a.SyncAll(ctx)
		return syncedMsg{errs}
	}
}

func (m *Model) act(okMsg string, f func(ctx context.Context) error) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		return doneMsg{msg: okMsg, err: f(ctx)}
	}
}

// --------------------------------------------------------------- update

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		m.fixView()
		m.layoutComposer()
		return m, nil
	case tickMsg:
		var cmds []tea.Cmd
		cmds = append(cmds, tick())
		a := m.a
		cmds = append(cmds, func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			for _, it := range a.DeliverDue(ctx) {
				if it.Status == store.StatusFailed {
					return doneMsg{err: fmt.Errorf("send #%d failed: %s", it.ID, it.Error)}
				}
				return doneMsg{msg: "sent", sync: true}
			}
			return nil
		})
		if time.Since(m.toastAt) > 4*time.Second {
			m.toast = ""
		}
		// pick up the daemon's syncs and the agents' approvals
		if time.Now().Unix()%3 == 0 && m.mode != modeReview && m.mode != modeCompose {
			// only when another process (the daemon, an agent) wrote
			// something; Sent also counts down its outbox rows
			if st := m.a.Store.Stamp(); st != m.dbStamp || len(m.outboxRows) > 0 {
				m.dbStamp = st
				m.reload()
			}
		}
		if !m.syncing && time.Since(m.lastSync) > m.a.Config.MailInterval.Duration {
			if m.a.DaemonRunning() {
				m.lastSync = time.Now() // the background service has it
			} else {
				cmds = append(cmds, m.syncCmd())
			}
		}
		if time.Now().Unix()%2 == 0 {
			m.recolour()
		}
		if m.mode == modeCompose && time.Now().Unix()%3 == 0 {
			m.saveDraft()
		}
		if m.err != "" && time.Since(m.errAt) > 12*time.Second {
			m.err = ""
		}
		return m, tea.Batch(cmds...)
	case olderMsg:
		m.loadingOlder = false
		if msg.err != nil {
			m.setErr("older mail: " + msg.err.Error())
			return m, nil
		}
		if msg.n == 0 {
			m.noOlder[msg.section] = true
			m.say("that's everything in " + msg.section)
			return m, nil
		}
		m.reload()
		m.say(fmt.Sprintf("loaded %d older", msg.n))
		return m, nil
	case pickedFilesMsg:
		return m.afterPick(msg)
	case syncSoonMsg:
		return m, m.syncCmd()
	case syncedMsg:
		m.syncing = false
		m.lastSync = time.Now()
		for acc, err := range msg.errs {
			m.setErr(acc + ": " + err.Error())
		}
		m.accounts = m.a.AccountNames()
		m.reload()
		return m, nil
	case doneMsg:
		if msg.err != nil {
			m.setErr(msg.err.Error())
		} else if msg.msg != "" {
			m.say(msg.msg)
		}
		m.reload()
		if msg.sync && m.tab == tabChat {
			m.scroll = 1 << 30 // your message: show the end of the chat
			m.fixView()
		}
		if msg.err == nil && (msg.sync || msg.msg != "") {
			return m, m.syncSoon()
		}
		return m, nil
	case searchMsg:
		if msg.err != nil {
			m.err = "search: " + msg.err.Error()
			return m, nil
		}
		m.search = &config.Section{Name: "search: " + msg.q, Query: msg.q}
		m.filter = ""
		m.threads = msg.threads
		m.sel, m.offset = 0, 0
		m.loadPreview(true)
		m.say(fmt.Sprintf("%d found · W saves it as a section · h/l leaves", len(msg.threads)))
		return m, nil
	case aiMsg:
		m.aiBusy = false
		if msg.err != nil {
			m.ai, m.aiTitle = msg.err.Error(), "claude: error"
			return m, nil
		}
		if msg.draftFor != nil {
			r := *msg.draftFor
			quoted := r.out.Body
			r.out.Body = strings.TrimSpace(msg.text)
			m.ai = ""
			return m, m.openComposer(r.account, "reply · drafted by claude", r.out, quoted, 0)
		}
		m.ai, m.aiTitle = strings.TrimSpace(msg.text), msg.title
		return m, nil
	case editedMsg:
		return m.afterEdit(msg)
	case convoDirTick:
		if c := m.convo; c != nil && strings.TrimSpace(c.input.Value()) == msg.q {
			a, acc := m.a, c.account
			return m, func() tea.Msg {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				a.SearchDirectory(ctx, acc, msg.q)
				return convoDirMsg{msg.q}
			}
		}
		return m, nil
	case convoDirMsg:
		if c := m.convo; c != nil && strings.TrimSpace(c.input.Value()) == msg.q {
			skip := map[string]bool{}
			for _, p := range c.picked {
				skip[p.key()] = true
			}
			c.sugg = m.people(c.account, msg.q, 7, skip)
		}
		return m, nil
	case convoMsg:
		return m.gotConvo(msg)
	case signedInMsg:
		return m.gotSignedIn(msg)
	case convoURLMsg:
		if m.convo != nil {
			m.convo.signURL = msg.url
		}
		return m, nil
	case pickerFilesMsg:
		m.gotFiles(msg)
		return m, nil
	case findLocalTick:
		return m, m.findLocal(msg.seq)
	case findServerTick:
		return m, m.findServer(msg.seq)
	case findLocalMsg:
		return m, m.gotFindLocal(msg)
	case findServerMsg:
		return m, m.gotFindServer(msg)
	case findPreviewMsg:
		m.gotFindPreview(msg)
		return m, nil
	case dirTickMsg:
		return m, m.dirLookup(msg.q)
	case dirMsg:
		m.gotDirectory(msg)
		return m, nil
	case sigMsg:
		m.gotSignature(msg)
		return m, nil
	case tea.KeyMsg:
		nm, cmd := m.key(msg)
		if mm, ok := nm.(Model); ok {
			mm.fixView()
			return mm, cmd
		}
		return nm, cmd
	}
	return m, nil
}

// fixView keeps the list offset and the preview scroll in range and
// re-renders the preview, so what View draws and what the keys move agree.
func (m *Model) fixView() {
	visible := max(1, (m.bodyHeight()-3)/m.cardRows())
	if m.sel < m.offset {
		m.offset = m.sel
	}
	if m.sel >= m.offset+visible {
		m.offset = m.sel - visible + 1
	}
	if m.w == 0 {
		return
	}
	inner := m.w - m.listWidth() - 4
	m.buildPreview(inner)
	avail := m.bodyHeight() - 2 - len(m.aiLines(inner))
	if _, ok := m.curSpace(); ok {
		avail -= 2
	}
	bottom := max(0, len(m.preview)-max(3, avail))
	m.scroll = max(0, min(m.scroll, bottom))
	m.atBottom = m.scroll >= bottom
}

func (m Model) key(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	s := k.String()
	if m.mode == modeCompose && m.comp != nil {
		return m.composerKey(k)
	}
	if m.mode == modeFind && m.find != nil {
		return m.finderKey(k)
	}
	if m.mode == modePick && m.pick != nil {
		return m.pickerKey(k)
	}
	if m.mode == modeWhen && m.when != nil {
		return m.whenKey(k)
	}
	if m.mode == modeConvo && m.convo != nil {
		return m.convoKey(k)
	}
	// Ctrl+t while typing a chat message: send it later
	if m.mode == modeChatMsg && s == "ctrl+t" {
		if strings.TrimSpace(string(m.input)) == "" {
			return m, nil
		}
		text := string(m.input)
		m.openWhen("chat")
		m.when.text, m.when.back = text, modeNormal
		return m, nil
	}
	if m.mode == modeHelp {
		return m.helpKey(k)
	}
	if s == "ctrl+c" {
		return m, tea.Quit
	}
	switch m.mode {
	case modeHelp:
		if s == "?" || s == "q" || s == "esc" {
			m.mode = modeNormal
		}
		return m, nil
	case modeFilter, modeAsk, modeSearch, modeSaveName, modeDraftHint, modeChatMsg, modeLink, modeRename:
		return m.inputKey(k)
	case modeReview:
		return m.reviewKey(s)
	case modeApprove:
		return m.approveKey(s)
	}
	return m.normalKey(s)
}

func (m Model) inputKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.Type {
	case tea.KeyEsc:
		m.replyTo = nil
		if m.mode == modeFilter {
			m.filter = ""
			m.reload()
		}
		m.mode, m.input = modeNormal, nil
		return m, nil
	case tea.KeyEnter:
		text := strings.TrimSpace(string(m.input))
		md := m.mode
		m.mode, m.input = modeNormal, nil
		switch md {
		case modeFilter:
			m.filter = text
			m.reload()
		case modeAsk:
			if text != "" {
				return m, m.ask(text)
			}
		case modeSearch:
			if text != "" {
				return m, m.searchCmd(text)
			}
		case modeSaveName:
			if m.search != nil && text == "" {
				text = m.search.Query
			}
			if m.search != nil && text != "" {
				if err := m.a.SaveSearch(text, m.search.Query); err != nil {
					m.err = err.Error()
					return m, nil
				}
				m.sections = m.a.Sections()
				m.sec = len(m.sections) - 1
				m.search = nil
				m.say("saved as " + text)
				m.reload()
				return m, m.syncCmd()
			}
		case modeDraftHint:
			return m, m.aiDraft(text)
		case modeChatMsg:
			if sp, ok := m.curSpace(); ok && text != "" {
				out := store.Outgoing{Space: sp.Name, SpaceName: cli.SpaceTitle(sp), Body: text}
				m.quoteInto(&out, sp)
				it, err := m.a.Queue(sp.Account, "chat", out, app.OriginYou)
				if err != nil {
					m.setErr(err.Error())
				} else {
					m.replyTo = nil
					return m, m.sent(it)
				}
			}
		case modeRename:
			if sp, ok := m.curSpace(); ok {
				a := m.a
				return m, func() tea.Msg {
					ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
					defer cancel()
					onGoogle, err := a.RenameSpace(ctx, sp.Account, sp.Name, text)
					switch {
					case err != nil:
						return doneMsg{err: err}
					case text == "":
						return doneMsg{msg: "back to its own name"}
					case onGoogle:
						return doneMsg{msg: "renamed on Google Chat"}
					}
					return doneMsg{msg: "named here (Google Chat doesn't name DMs and groups)"}
				}
			}
		case modeLink:
			m.openLink(text)
		}
		return m, nil
	case tea.KeyBackspace:
		if len(m.input) > 0 {
			m.input = m.input[:len(m.input)-1]
		}
	case tea.KeyCtrlU:
		m.input = nil
	case tea.KeyCtrlW:
		t := strings.TrimRight(string(m.input), " ")
		if i := strings.LastIndex(t, " "); i >= 0 {
			m.input = []rune(t[:i+1])
		} else {
			m.input = nil
		}
	case tea.KeySpace:
		m.input = append(m.input, ' ')
	case tea.KeyRunes:
		m.input = append(m.input, k.Runes...)
	}
	if m.mode == modeFilter {
		m.filter = string(m.input)
		m.sel, m.offset = 0, 0
		m.reload()
	}
	return m, nil
}

func (m Model) normalKey(s string) (tea.Model, tea.Cmd) {
	page := max(1, m.previewHeight()/2)
	switch s {
	case "q", "esc":
		switch {
		case m.focus == 1:
			m.focus = 0
		case m.err != "":
			m.err = ""
		case m.ai != "" || m.aiBusy:
			m.ai, m.aiBusy = "", false
		case len(m.selected) > 0:
			m.selected = map[string]bool{}
		case m.filter != "":
			m.filter = ""
			m.reload()
		case m.search != nil:
			m.search = nil
			m.reload()
		case s == "q":
			return m, tea.Quit
		}
		return m, nil
	case "?":
		m.mode = modeHelp
	case "j", "down":
		if m.pickingMsg() {
			m.moveChatSel(1)
		} else if m.focus == 1 {
			m.scroll++
		} else if m.tab == tabMail && m.sel >= m.count()-1 && m.count() > 0 {
			return m, m.loadOlderCmd()
		} else if m.sel < m.count()-1 {
			m.sel++
			m.ai = ""
			m.loadPreview(true)
		}
	case "k", "up":
		if m.pickingMsg() {
			m.moveChatSel(-1)
		} else if m.focus == 1 {
			m.scroll = max(0, m.scroll-1)
		} else if m.sel > 0 {
			m.sel--
			m.ai = ""
			m.loadPreview(true)
		}
	case "g":
		if m.pickingMsg() {
			m.moveChatSel(-len(m.chatMsgs))
		} else if m.focus == 1 {
			m.scroll = 0
		} else {
			m.sel = 0
			m.loadPreview(true)
		}
	case "G":
		if m.pickingMsg() {
			m.moveChatSel(len(m.chatMsgs))
		} else if m.focus == 1 {
			m.scroll = 1 << 30
		} else {
			m.sel = max(0, m.count()-1)
			m.loadPreview(true)
		}
	case "J":
		m.scroll++
	case "K":
		m.scroll = max(0, m.scroll-1)
	case "ctrl+d":
		m.scroll += page
	case "ctrl+u":
		m.scroll = max(0, m.scroll-page)
	case "h", "l":
		n := len(m.sections)
		if m.tab == tabChat {
			n = len(chatSections)
		}
		if m.search != nil {
			m.search = nil
		} else if s == "l" {
			m.sec = (m.sec + 1) % n
		} else {
			m.sec = (m.sec + n - 1) % n
		}
		m.sel, m.offset, m.filter = 0, 0, ""
		m.selected = map[string]bool{}
		m.reload()
	case "tab":
		m.tab = 1 - m.tab
		m.sec, m.sel, m.offset, m.filter, m.search, m.ai = 0, 0, 0, "", nil, ""
		m.selected = map[string]bool{}
		m.reload()
	case "1":
		m.focus = 0
	case "2":
		m.focus = 1
		m.chatSel = len(m.chatMsgs) - 1 // the latest message
		m.fixView()
	case "enter":
		if it, ok := m.curOutbox(); ok {
			// a mail or message still waiting: open it to change it
			switch it.Status {
			case store.StatusApproval:
				m.mode = modeApprove
				return m, nil
			case store.StatusQueued, store.StatusFailed:
				if it.Kind == "chat" {
					m.replaceNext = it.ID
					return m, m.editChat(it.Account, it.Out, 0)
				}
				if it.Kind == "mail" {
					body, quoted := splitQuoted(it.Out.Body)
					out := it.Out
					out.Body = body
					cmd := m.openComposer(it.Account, "edit · "+map[bool]string{true: "scheduled", false: "failed"}[it.Status == store.StatusQueued], out, quoted, 0)
					if m.comp != nil {
						m.comp.replaceID = it.ID
					}
					return m, cmd
				}
			}
			return m, nil
		}
		if m.focus == 1 && m.tab == tabMail {
			m.expandAll = !m.expandAll
			return m, nil
		}
		m.focus = 1
		m.chatSel = len(m.chatMsgs) - 1 // in a chat: pick its latest message
		m.fixView()
		if t, ok := m.curThread(); ok && t.Unread() {
			return m, m.act("", func(ctx context.Context) error { return m.a.MarkRead(ctx, t.Account, t.ID, true) })
		}
		if sp, ok := m.curSpace(); ok && sp.Unread > 0 {
			return m, m.act("", func(ctx context.Context) error { return m.a.MarkSpaceRead(ctx, sp.Account, sp.Name) })
		}
	case "/", "s":
		m.openFinder()
	case "W":
		if m.search != nil {
			m.mode, m.input = modeSaveName, nil // Enter alone names it after the search
		} else {
			m.say("search first (s), then W saves it")
		}
	case "X":
		if m.tab == tabMail && m.search == nil {
			name := m.currentSection().Name
			if m.a.RemoveSearch(name) {
				m.sections = m.a.Sections()
				m.sec = 0
				m.say("removed section " + name)
				m.reload()
			} else {
				m.say(name + " comes from config.toml; remove it there")
			}
		}
	case "u":
		if len(m.accounts) > 1 {
			m.acct++
			if m.acct >= len(m.accounts) {
				m.acct = -1
			}
			m.sel, m.offset = 0, 0
			m.reload()
			if m.acct < 0 {
				m.say("all accounts")
			} else {
				m.say("account " + m.accounts[m.acct])
			}
		} else {
			m.say("only one account (sandesh auth add <name> adds another)")
		}
	case "R":
		if it, ok := m.curOutbox(); ok && it.Status == store.StatusFailed {
			if err := m.a.Retry(it.ID); err != nil {
				m.setErr(err.Error())
			} else {
				m.say("sending again…")
				m.reload()
			}
			return m, m.sent(it)
		}
		m.say("syncing…")
		return m, m.syncCmd()
	case "x":
		if key := m.keyAt(m.sel); key != "" && m.tab == tabMail {
			if m.selected[key] {
				delete(m.selected, key)
			} else {
				m.selected[key] = true
			}
			if m.sel < m.count()-1 {
				m.sel++
				m.loadPreview(true)
			}
		}
	case "e", "d", "*":
		return m, m.modify(s)
	case "m", "M":
		read := s == "m"
		if m.tab == tabChat {
			if sp, ok := m.curSpace(); ok {
				if read {
					return m, m.act("marked read", func(ctx context.Context) error { return m.a.MarkSpaceRead(ctx, sp.Account, sp.Name) })
				}
				return m, m.act("marked unread", func(ctx context.Context) error { return m.a.MarkSpaceUnread(ctx, sp.Account, sp.Name) })
			}
			return m, nil
		}
		return m, m.modify(s)
	case "Q":
		m.showQuoted = !m.showQuoted
		m.say(map[bool]string{true: "showing quoted text", false: "quoted text folded"}[m.showQuoted])
	case "E":
		m.expandAll = !m.expandAll
	case "o":
		switch len(m.links) {
		case 0:
			m.say("no links here")
		case 1:
			m.openLink("1")
		default:
			m.mode, m.input = modeLink, nil
		}
	case "n":
		if sp, ok := m.curSpace(); ok {
			m.mode, m.input = modeRename, []rune(cli.SpaceTitle(sp))
		}
	case "i":
		if m.tab == tabChat {
			if _, ok := m.curSpace(); ok {
				m.mode, m.input = modeChatMsg, nil
			}
		}
	case "+", "-":
		if m.tab == tabMail {
			sizes := []int{10, 25, 50, 100}
			i := slices.Index(sizes, m.olderN)
			if s == "+" {
				i = min(len(sizes)-1, i+1)
			} else {
				i = max(0, i-1)
			}
			m.olderN = sizes[max(0, i)]
			m.say(fmt.Sprintf("older mail loads %d at a time", m.olderN))
		}
	case "N":
		if m.tab == tabChat {
			m.openConvo()
		}
	case "t":
		if it, ok := m.curOutbox(); ok && it.Status == store.StatusQueued {
			m.openWhen("outbox")
			m.when.outboxID = it.ID
		}
	case "z":
		if it, ok := m.curOutbox(); ok && (it.Status == store.StatusQueued || it.Status == store.StatusApproval) {
			if err := m.a.Cancel(it.ID); err != nil {
				m.setErr(err.Error())
			} else {
				m.say("cancelled, not sent")
				m.reload()
			}
			return m, nil
		}
		return m, m.undoCmd()
	case "c":
		return m, m.compose()
	case "r", "a", "f":
		if m.tab == tabChat {
			// reply to the picked message (or the latest from someone
			// else), quoting it, right in the message box
			if _, ok := m.curSpace(); ok && s == "r" {
				m.replyTo = m.replyTarget()
				m.mode, m.input = modeChatMsg, nil
			}
			return m, nil
		}
		return m, m.replyCmd(s)
	case "L":
		return m, m.labelCmd()
	case "v", "V":
		return m, m.saveCmd(s == "V")
	case "U":
		if m.tab != tabChat {
			m.say("U uploads into a chat space; for mail, reply and press + (or write Attach: in the editor)")
			return m, nil
		}
		return m, m.pickFiles("chat")
	case "A":
		if len(m.waiting) > 0 {
			m.mode = modeApprove
		} else {
			m.say("nothing waiting for approval")
		}
	case "b":
		if u := m.link(); u != "" {
			_ = exec.Command("xdg-open", u).Start()
			m.say("opened in the browser")
		}
	case "y":
		if m.pickingMsg() {
			m.say(copyText(m.chatMsgs[m.chatSel].Text))
		} else if u := m.link(); u != "" {
			m.say(copyText(u))
		}
	case "@":
		m.mode, m.input = modeAsk, nil
	case "S":
		return m, m.summarise()
	case "D":
		if _, ok := m.curThread(); ok {
			m.mode, m.input = modeDraftHint, nil
		}
	case "T":
		return m, m.digest()
	}
	return m, nil
}

// modify archives, trashes, stars or toggles read on the targets, with undo.
func (m *Model) modify(key string) tea.Cmd {
	if m.tab != tabMail {
		return nil
	}
	ts := m.targets()
	if len(ts) == 0 {
		return nil
	}
	var ids []string
	for _, t := range ts {
		ids = append(ids, t.ID)
	}
	account := ts[0].Account
	var what, done string
	var f func(ctx context.Context, acc, id string) error
	switch key {
	case "e":
		what, done = "archive", "archived"
		f = m.a.Archive
	case "d":
		what, done = "trash", "moved to trash"
		f = m.a.Trash
	case "*":
		on := !ts[0].Starred()
		what, done = map[bool]string{true: "star", false: "unstar"}[on], map[bool]string{true: "starred", false: "unstarred"}[on]
		f = func(ctx context.Context, acc, id string) error { return m.a.Star(ctx, acc, id, on) }
	case "m", "M":
		read := key == "m"
		what, done = map[bool]string{true: "read", false: "unread"}[read], map[bool]string{true: "marked read", false: "marked unread"}[read]
		f = func(ctx context.Context, acc, id string) error { return m.a.MarkRead(ctx, acc, id, read) }
		for _, t := range ts {
			if read {
				_ = m.a.Store.SetThreadLabels(t.Account, t.ID, nil, []string{"UNREAD"})
			} else {
				_ = m.a.Store.SetThreadLabels(t.Account, t.ID, []string{"UNREAD"}, nil)
			}
		}
	}
	// change the local copy now so the list reacts at once; Google after
	byAcc := map[string][]string{}
	for _, t := range ts {
		byAcc[t.Account] = append(byAcc[t.Account], t.ID)
	}
	switch key {
	case "e":
		for _, t := range ts {
			_ = m.a.Store.SetThreadLabels(t.Account, t.ID, nil, []string{"INBOX"})
		}
	case "d":
		for _, t := range ts {
			_ = m.a.Store.SetThreadLabels(t.Account, t.ID, []string{"TRASH"}, []string{"INBOX"})
		}
	}
	m.selected = map[string]bool{}
	m.undo = &undo{what: what, account: account, ids: ids}
	msg := done
	if len(ids) > 1 {
		msg = fmt.Sprintf("%s %d", done, len(ids))
	}
	if key == "e" || key == "d" {
		msg += " · z undoes"
	}
	m.reload()
	return m.act(msg, func(ctx context.Context) error {
		for acc, list := range byAcc {
			for _, id := range list {
				if err := f(ctx, acc, id); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

func (m *Model) undoCmd() tea.Cmd {
	u := m.undo
	if u == nil {
		// a send in its undo window?
		items, _ := m.a.Store.Outbox(store.StatusQueued)
		if len(items) == 0 {
			m.say("nothing to undo")
			return nil
		}
		u = &undo{what: "send", outboxID: items[0].ID}
	}
	m.undo = nil
	if u.what == "send" {
		if err := m.a.Cancel(u.outboxID); err != nil {
			m.err = err.Error()
			return nil
		}
		m.say("send cancelled, nothing went out")
		return nil
	}
	return m.act("undone", func(ctx context.Context) error {
		for _, id := range u.ids {
			t, err := m.a.Store.Thread(u.account, id)
			acc := u.account
			if err == nil {
				acc = t.Account
			}
			switch u.what {
			case "archive":
				err = m.a.Unarchive(ctx, acc, id)
			case "trash":
				err = m.a.Untrash(ctx, acc, id)
			case "star":
				err = m.a.Star(ctx, acc, id, false)
			case "unstar":
				err = m.a.Star(ctx, acc, id, true)
			case "read":
				err = m.a.MarkRead(ctx, acc, id, false)
			case "unread":
				err = m.a.MarkRead(ctx, acc, id, true)
			}
			if err != nil {
				return err
			}
		}
		return nil
	})
}

func (m *Model) searchCmd(q string) tea.Cmd {
	acc := ""
	if f := m.accountFilter(); len(f) > 0 {
		acc = f[0]
	}
	a := m.a
	m.say("searching Gmail…")
	return func() tea.Msg {
		account, err := a.ResolveAccount(acc)
		if err != nil {
			return searchMsg{q: q, err: err}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		ts, err := a.SearchServer(ctx, account.Name, q, 50)
		// kept like a section, so a refresh shows it and "load older" adds to it
		var ids []string
		for _, t := range ts {
			ids = append(ids, t.ID)
		}
		_ = a.Store.SetSectionThreads(account.Name, "search: "+q, ids)
		return searchMsg{q, ts, err}
	}
}

func (m *Model) link() string {
	if t, ok := m.curThread(); ok {
		acc, _ := m.a.Store.Account(t.Account)
		return fmt.Sprintf("https://mail.google.com/mail/u/%s/#all/%s", acc.Email, t.ID)
	}
	if s, ok := m.curSpace(); ok {
		id := strings.TrimPrefix(s.Name, "spaces/")
		if s.Type == "DIRECT_MESSAGE" {
			return "https://mail.google.com/chat/u/0/#chat/dm/" + id
		}
		return "https://mail.google.com/chat/u/0/#chat/space/" + id
	}
	return ""
}

// openLink opens the preview's link number n in the browser.
func (m *Model) openLink(n string) {
	var i int
	if _, err := fmt.Sscanf(strings.TrimSpace(n), "%d", &i); err != nil || i < 1 || i > len(m.links) {
		m.say(fmt.Sprintf("no link %s (there are %d)", n, len(m.links)))
		return
	}
	u := m.links[i-1]
	if !strings.HasPrefix(u, "https://") && !strings.HasPrefix(u, "http://") {
		m.say("not opening a non-web link: " + u)
		return
	}
	_ = exec.Command("xdg-open", u).Start()
	m.say("opened link " + n)
}

func copyText(s string) string {
	for _, c := range [][]string{{"wl-copy"}, {"xclip", "-selection", "clipboard"}, {"xsel", "-b"}} {
		if _, err := exec.LookPath(c[0]); err == nil {
			cmd := exec.Command(c[0], c[1:]...)
			cmd.Stdin = strings.NewReader(s)
			if cmd.Run() == nil {
				return "copied the link"
			}
		}
	}
	// OSC 52: the terminal (and tmux with set-clipboard) copies it
	fmt.Fprintf(os.Stderr, "\033]52;c;%s\a", b64(s))
	return "copied the link (OSC 52)"
}
