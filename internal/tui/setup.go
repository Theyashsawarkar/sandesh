package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/Theyashsawarkar/sandesh/internal/app"
	"github.com/Theyashsawarkar/sandesh/internal/google"
)

// The first-run screen, shown instead of the inbox while sandesh has no
// OAuth client or no signed-in account. Three steps: the client file (you
// give its path; sandesh copies it into its config), the Google sign-in,
// the first sync. Then it turns into the inbox.

const consoleURL = "https://console.cloud.google.com/apis/credentials"

type setupStep int

const (
	stepClient setupStep = iota
	stepAccount
	stepSigning
	stepSyncing
)

type setupInput int

const (
	inNone setupInput = iota
	inPath
	inID
	inSecret
	inName
)

type setup struct {
	a      *app.App
	launch string
	styles styles
	w, h   int

	step     setupStep
	in       setupInput
	input    []rune
	sugg     []string // completions of the path being typed
	suggAt   int
	id       string // typed client id, while the secret is asked
	guide    bool
	name     string
	chat     bool
	url      string // the sign-in URL, while waiting
	cancel   context.CancelFunc
	client   string // what was installed, for the ✓ line
	err      string
	note     string
	done     int
	total    int
	progress chan [2]int
}

type signURLMsg string
type signedMsg struct{ err error }
type firstSyncMsg struct{ err error }
type progressMsg [2]int

func newSetup(a *app.App, launch string) setup {
	s := setup{a: a, launch: launch, styles: newStyles(a.Config.Colors), name: "work", chat: true}
	if a.HasClient() {
		s.step = stepAccount
		s.client = describeClient(a.Paths.ClientFile())
	} else {
		s.in = inPath // the path prompt is where it starts
		s.input = []rune("~/")
		s.sugg = completions("~/")
	}
	return s
}

func (s setup) Init() tea.Cmd { return nil }

func expandHome(p string) string {
	p = strings.Trim(strings.TrimSpace(p), `"'`)
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, strings.TrimPrefix(p, "~")) + map[bool]string{true: "/", false: ""}[strings.HasSuffix(p, "/")]
	}
	return p
}

func tildePath(p string) string {
	home, _ := os.UserHomeDir()
	if strings.HasPrefix(p, home+"/") {
		return "~/" + strings.TrimPrefix(p, home+"/")
	}
	return p
}

// completions lists folders and .json files that start with what is typed.
func completions(typed string) []string {
	full := expandHome(typed)
	dir, prefix := filepath.Split(full)
	if dir == "" {
		dir = "."
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		n := e.Name()
		if !strings.HasPrefix(strings.ToLower(n), strings.ToLower(prefix)) || strings.HasPrefix(n, ".") && !strings.HasPrefix(prefix, ".") {
			continue
		}
		p := filepath.Join(dir, n)
		isDir := e.IsDir()
		if e.Type()&os.ModeSymlink != 0 {
			if st, err := os.Stat(p); err == nil {
				isDir = st.IsDir()
			}
		}
		switch {
		case isDir:
			out = append(out, tildePath(p)+"/")
		case strings.HasSuffix(strings.ToLower(n), ".json"):
			out = append(out, tildePath(p))
		}
	}
	// client_secret files first, then folders, then other json
	rank := func(p string) int {
		switch {
		case strings.Contains(filepath.Base(p), "client_secret"):
			return 0
		case strings.HasSuffix(p, "/"):
			return 1
		}
		return 2
	}
	sort.SliceStable(out, func(i, j int) bool { return rank(out[i]) < rank(out[j]) })
	if len(out) > 6 {
		out = out[:6]
	}
	return out
}

// describeClient: "Desktop app client 2736…, project sandesh-512808".
func describeClient(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var f struct {
		Installed struct {
			ClientID  string `json:"client_id"`
			ProjectID string `json:"project_id"`
		} `json:"installed"`
	}
	_ = json.Unmarshal(b, &f)
	id := f.Installed.ClientID
	if i := strings.Index(id, "-"); i > 0 {
		id = id[:i]
	}
	if len(id) > 6 {
		id = id[:4] + "…"
	}
	d := "Desktop app client " + id
	if f.Installed.ProjectID != "" {
		d += ", project " + f.Installed.ProjectID
	}
	return d
}

func (s *setup) installFile(path string) {
	full := expandHome(path)
	st, err := os.Stat(full)
	if err != nil {
		s.err = "No file at " + path
		return
	}
	if st.IsDir() {
		s.err = path + " is a folder; pick the .json file in it (Tab lists them)"
		return
	}
	b, err := os.ReadFile(full)
	if err != nil {
		s.err = "Can't read " + path + ": " + err.Error()
		return
	}
	s.install(b, filepath.Base(full))
}

func (s *setup) install(b []byte, from string) {
	if strings.HasPrefix(strings.TrimSpace(string(b)), "AIza") {
		s.err = "That's an API key. sandesh needs an OAuth client: Credentials → Create credentials → OAuth client ID → Desktop app."
		return
	}
	if err := s.a.InstallClient(b); err != nil {
		s.err = from + ": " + err.Error()
		return
	}
	s.err, s.note = "", ""
	s.client = describeClient(s.a.Paths.ClientFile())
	s.step, s.in, s.input, s.sugg = stepAccount, inNone, nil, nil
}

func (s setup) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		s.w, s.h = msg.Width, msg.Height
		return s, nil
	case signURLMsg:
		s.url = string(msg)
		return s, nil
	case progressMsg:
		s.done, s.total = msg[0], msg[1]
		return s, s.waitProgress()
	case signedMsg:
		s.cancel = nil
		if msg.err != nil {
			s.step, s.url = stepAccount, ""
			if errors.Is(msg.err, context.Canceled) {
				s.err = "Sign-in cancelled."
			} else {
				s.err = "Sign-in failed: " + msg.err.Error()
			}
			return s, nil
		}
		s.step, s.err = stepSyncing, ""
		s.progress = make(chan [2]int, 64)
		ch := s.progress
		s.a.Progress = func(d, t int) {
			select {
			case ch <- [2]int{d, t}:
			default:
			}
		}
		a, name, chat := s.a, s.name, s.chat
		return s, tea.Batch(s.waitProgress(), func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
			defer cancel()
			_, err := a.SyncMail(ctx, name)
			a.Progress = nil
			if err != nil {
				return firstSyncMsg{err}
			}
			if chat {
				_, _ = a.SyncChat(ctx, name) // a chat problem shows in the screen; mail works
			}
			return firstSyncMsg{nil}
		})
	case firstSyncMsg:
		m := New(s.a, s.launch)
		m.w, m.h = s.w, s.h
		if msg.err != nil {
			m.setErr("first sync: " + msg.err.Error())
		}
		m.fixView()
		cmds := []tea.Cmd{m.Init()}
		if !app.ServiceInstalled() && google.FakeURL() == "" {
			// keep the copy fresh from now on, with or without this screen
			cmds = append(cmds, func() tea.Msg {
				if err := app.InstallService(); err != nil {
					return doneMsg{err: fmt.Errorf("background sync: %w (sandesh service install retries)", err)}
				}
				return doneMsg{msg: "background sync started"}
			})
		}
		return m, tea.Batch(cmds...)
	case tea.KeyMsg:
		return s.key(msg)
	}
	return s, nil
}

func (s setup) waitProgress() tea.Cmd {
	ch := s.progress
	if ch == nil {
		return nil
	}
	return func() tea.Msg {
		p := <-ch
		for {
			select {
			case q := <-ch:
				p = q
			default:
				return progressMsg(p)
			}
		}
	}
}

// demoWanted tells Run to start the demo after the setup screen quits.
var demoWanted bool

func (s setup) key(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := k.String()
	if key == "ctrl+c" {
		if s.cancel != nil {
			s.cancel()
		}
		return s, tea.Quit
	}
	// these work while typing too
	if s.step == stepClient {
		switch key {
		case "ctrl+t":
			s.in, s.input, s.sugg, s.err = inID, nil, nil, ""
			return s, nil
		case "ctrl+o":
			_ = exec.Command("xdg-open", consoleURL).Start()
			s.note = "Opened the Cloud console in your browser."
			return s, nil
		case "ctrl+g":
			s.guide = !s.guide
			return s, nil
		case "ctrl+d":
			demoWanted = true
			return s, tea.Quit
		}
	}
	if s.in != inNone {
		return s.inputKey(k)
	}
	switch s.step {
	case stepClient:
		if key == "q" || key == "esc" {
			return s, tea.Quit
		}
		s.in = inPath
	case stepAccount:
		switch key {
		case "enter":
			return s.startSignIn()
		case "n":
			s.in, s.input, s.err = inName, []rune(s.name), ""
		case "tab":
			s.chat = !s.chat
			if s.name == "work" && !s.chat {
				s.name = "personal"
			} else if s.name == "personal" && s.chat {
				s.name = "work"
			}
		case "c":
			s.step, s.err, s.note, s.in, s.input = stepClient, "", "", inPath, []rune("~/")
			s.sugg = completions("~/")
		case "q", "esc":
			return s, tea.Quit
		}
	case stepSigning:
		switch key {
		case "y":
			if s.url != "" {
				s.note = copyText(s.url)
			}
		case "o":
			if s.url != "" {
				_ = exec.Command("xdg-open", s.url).Start()
			}
		case "esc", "q":
			if s.cancel != nil {
				s.cancel()
			}
		}
	}
	return s, nil
}

func (s setup) startSignIn() (tea.Model, tea.Cmd) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	s.cancel = cancel
	s.step, s.err, s.note, s.url = stepSigning, "", "", ""
	a, name, chat := s.a, s.name, s.chat
	urls := make(chan string, 1)
	done := make(chan error, 1)
	go func() {
		err := a.SignIn(ctx, name, "", chat, func(u string) {
			select {
			case urls <- u:
			default:
			}
		})
		close(urls)
		cancel()
		done <- err
	}()
	waitURL := func() tea.Msg {
		if u, ok := <-urls; ok {
			return signURLMsg(u)
		}
		return nil
	}
	waitDone := func() tea.Msg { return signedMsg{<-done} }
	return s, tea.Batch(waitURL, waitDone)
}

func (s setup) inputKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.Type {
	case tea.KeyEsc:
		switch s.in {
		case inPath:
			return s, tea.Quit
		case inID, inSecret:
			s.in, s.input, s.err = inPath, []rune("~/"), ""
			s.sugg = completions("~/")
			return s, nil
		}
		s.in, s.input = inNone, nil
		return s, nil
	case tea.KeyTab, tea.KeyDown, tea.KeyUp:
		if s.in != inPath || len(s.sugg) == 0 {
			return s, nil
		}
		if k.Type == tea.KeyUp {
			s.suggAt = (s.suggAt - 2 + 2*len(s.sugg)) % len(s.sugg)
		}
		pick := s.sugg[s.suggAt%len(s.sugg)]
		if len(s.sugg) == 1 && strings.HasSuffix(pick, "/") {
			// the only match is a folder: go into it
			s.input, s.suggAt = []rune(pick), 0
			s.sugg = completions(pick)
			return s, nil
		}
		s.input = []rune(pick)
		s.suggAt = s.suggAt%len(s.sugg) + 1
		return s, nil
	case tea.KeyEnter:
		text := strings.TrimSpace(string(s.input))
		switch s.in {
		case inPath:
			switch {
			case strings.HasPrefix(text, "{"):
				s.install([]byte(text), "The pasted JSON")
			case strings.HasSuffix(text, "/"):
				// a folder: open it
				s.sugg, s.suggAt = completions(text), 0
				if len(s.sugg) == 0 {
					s.err = "No folders or .json files in " + text
				}
			case text != "":
				s.installFile(text)
			}
			return s, nil
		case inID:
			if strings.HasPrefix(text, "AIza") {
				s.err = "That's an API key, not an OAuth client ID."
				return s, nil
			}
			if !strings.HasSuffix(text, ".apps.googleusercontent.com") {
				s.err = "A client ID ends in .apps.googleusercontent.com"
				return s, nil
			}
			s.id, s.in, s.input, s.err = text, inSecret, nil, ""
			return s, nil
		case inSecret:
			if text != "" {
				s.install(google.ClientJSON(s.id, text), "The ID and secret")
			}
			return s, nil
		case inName:
			s.in, s.input = inNone, nil
			if text != "" && !strings.ContainsAny(text, " /") {
				s.name = text
			} else if text != "" {
				s.err = "A name is one word, like work or personal"
			}
			return s, nil
		}
	case tea.KeyBackspace:
		if len(s.input) > 0 {
			s.input = s.input[:len(s.input)-1]
		}
	case tea.KeyCtrlU:
		s.input = nil
	case tea.KeySpace:
		s.input = append(s.input, ' ')
	case tea.KeyRunes:
		s.input = append(s.input, k.Runes...) // a paste arrives here too
	}
	if s.in == inPath {
		s.suggAt, s.err = 0, ""
		if strings.HasPrefix(strings.TrimSpace(string(s.input)), "{") {
			s.sugg = nil
		} else {
			s.sugg = completions(string(s.input))
		}
	}
	return s, nil
}

// ------------------------------------------------------------------ view

func (s setup) View() string {
	if s.w == 0 {
		return ""
	}
	st := s.styles
	w := min(s.w-4, 86)
	inner := w - 6
	var b []string
	b = append(b, st.accent.Bold(true).Render("sandesh")+st.muted.Render(" · संदेश"), "")
	cur := int(s.step)
	if s.step == stepSigning {
		cur = 1
	} else if s.step == stepSyncing {
		cur = 2
	}
	var parts []string
	for i, name := range []string{"OAuth client", "Sign in", "First sync"} {
		switch {
		case i < cur:
			parts = append(parts, st.green.Render("✓ "+name))
		case i == cur:
			parts = append(parts, st.accent.Bold(true).Render(fmt.Sprintf("%d %s", i+1, name)))
		default:
			parts = append(parts, st.muted.Render(fmt.Sprintf("%d %s", i+1, name)))
		}
	}
	b = append(b, strings.Join(parts, st.muted.Render("  ──  ")), "")
	kc := func(k, d string) string { return st.keycap.Render(k) + " " + st.sub.Render(d) }
	field := func(label, text string) []string {
		box := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color(st.c.Accent)).Width(inner-2).Padding(0, 1)
		return strings.Split(box.Render(st.muted.Render(label+"  ")+st.text.Render(ansi.TruncateLeft(text, max(0, lipgloss.Width(text)-(inner-len(label)-8)), "…"))+st.accent.Render("▏")), "\n")
	}

	switch s.step {
	case stepClient:
		b = append(b, st.bold.Render("sandesh doesn't have your Google credentials yet."),
			wrapS(st.sub, "Where is your OAuth client file? It's the JSON from Google Cloud → Clients → Desktop app → Download JSON. sandesh copies it into ~/.config/sandesh/client.json, so you can delete the download afterwards.", inner), "")
		switch s.in {
		case inID:
			b = append(b, field("client ID", string(s.input))...)
			b = append(b, kc("⏎", "next")+"  "+kc("esc", "back to the file"))
		case inSecret:
			b = append(b, st.muted.Render("client ID  ")+st.text.Render(s.id))
			b = append(b, field("client secret", strings.Repeat("•", len(s.input)))...)
			b = append(b, kc("⏎", "save")+"  "+kc("esc", "back to the file"))
		default:
			text := string(s.input)
			if strings.HasPrefix(strings.TrimSpace(text), "{") {
				text = fmt.Sprintf("(pasted JSON, %d characters)", len(s.input))
			}
			b = append(b, field("path", text)...)
			for i, sg := range s.sugg {
				line := "  " + sg
				if s.suggAt > 0 && (s.suggAt-1)%len(s.sugg) == i {
					line = st.sel.Render(fit(st.text.Render(line), inner))
				} else if strings.Contains(filepath.Base(sg), "client_secret") {
					line = st.green.Render(line)
				} else {
					line = st.sub.Render(line)
				}
				b = append(b, line)
			}
			b = append(b, "", kc("tab", "complete")+"  "+kc("⏎", "use this file")+"  "+kc("ctrl+t", "type ID & secret")+"  "+kc("ctrl+o", "open console"))
			b = append(b, kc("ctrl+g", "how to create one")+"  "+kc("ctrl+d", "try the demo")+"  "+kc("esc", "quit"))
		}
		if s.guide {
			b = append(b, "", st.accent.Render("Creating the client (about five minutes, once)"))
			for i, l := range []string{
				"Create a project at console.cloud.google.com.",
				"APIs & Services → Library: enable Gmail API, Google Chat API and People API.",
				"Audience: Make internal (work account); for a personal Gmail add yourself as a test user.",
				"Google Chat API → Configuration: an app name, an avatar URL, a description; interactive features off.",
				"Clients → Create client → Desktop app → Download JSON, then give its path here.",
			} {
				b = append(b, wrapIndent(st.sub, fmt.Sprintf("%d. %s", i+1, l), inner, "   ")...)
			}
		}
	case stepAccount:
		kind := "work account: mail and Google Chat"
		if !s.chat {
			kind = "personal Gmail: mail only (Chat's API needs Workspace)"
		}
		ok := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color(st.c.Green)).Width(inner-2).Padding(0, 1).
			Render(st.green.Render("✓ ") + st.text.Render(firstNonEmpty(s.client, "OAuth client")) + st.muted.Render(" · saved to "+tildePath(s.a.Paths.ClientFile())))
		b = append(b, strings.Split(ok, "\n")...)
		b = append(b, "", st.bold.Render("Sign in a Google account"), "",
			"  "+st.muted.Render("name  ")+st.bold.Render(s.name),
			"  "+st.muted.Render("kind  ")+st.text.Render(kind), "")
		if s.in == inName {
			b = append(b, field("name", string(s.input))...)
		}
		b = append(b, kc("⏎", "sign in with Google (opens your browser)")+"  "+kc("n", "rename"),
			kc("tab", "work ↔ personal Gmail")+"  "+kc("c", "another client file")+"  "+kc("q", "quit"))
	case stepSigning:
		b = append(b, st.bold.Render("Waiting for you to sign in to Google in your browser…"), "")
		if s.url != "" {
			b = append(b, wrapS(st.sub, "If no browser tab opened, open this link:", inner))
			b = append(b, wrapIndent(st.blue, s.url, inner, "")...)
			b = append(b, "", kc("o", "open it again")+"  "+kc("y", "copy it")+"  "+kc("esc", "cancel"))
		} else {
			b = append(b, kc("esc", "cancel"))
		}
	case stepSyncing:
		b = append(b, st.bold.Render(fmt.Sprintf("Copying the last %d days of mail…", s.a.Config.InitialDays)), "")
		bw := inner - 22
		filled := 0
		if s.total > 0 {
			filled = min(bw, bw*s.done/s.total)
		}
		count := "starting"
		if s.total > 0 {
			count = fmt.Sprintf("%d / %d threads", s.done, s.total)
		}
		b = append(b, st.accent.Render(strings.Repeat("█", filled))+st.muted.Render(strings.Repeat("░", bw-filled))+"  "+st.sub.Render(count), "",
			st.muted.Render("Only the first sync takes a while; after it the inbox opens instantly."))
	}
	if s.err != "" {
		b = append(b, "")
		b = append(b, wrapIndent(st.red, s.err, inner, "")...)
	} else if s.note != "" {
		b = append(b, "", st.green.Render(s.note))
	}
	card := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color(st.c.Accent)).
		Padding(1, 3).Width(w).Render(strings.Join(b, "\n"))
	return lipgloss.Place(s.w, s.h, lipgloss.Center, lipgloss.Center, card)
}

func wrapS(style lipgloss.Style, text string, w int) string {
	return strings.Join(wrapIndent(style, text, w, ""), "\n")
}

func wrapIndent(style lipgloss.Style, text string, w int, indent string) []string {
	var out []string
	for i, l := range strings.Split(ansi.Wrap(text, max(20, w-len(indent)), ""), "\n") {
		if i > 0 {
			l = indent + l
		}
		out = append(out, style.Render(l))
	}
	return out
}
