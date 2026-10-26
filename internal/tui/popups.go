package tui

import (
	"bufio"
	"bytes"
	"fmt"
	"image"
	_ "image/gif" // image sizes in the file preview
	_ "image/jpeg"
	_ "image/png"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/Theyashsawarkar/sandesh/internal/app"
)

// Floating windows over the screen: every key (?) and the file picker.

// popupFrame draws a rounded window with a title around lines.
func (m Model) popupFrame(title string, lines []string, w int) []string {
	s := m.styles
	b := fg(s.c.Accent)
	t := " " + title + " "
	out := []string{b.Render("╭─") + b.Bold(true).Render(t) + b.Render(strings.Repeat("─", max(0, w-3-lipgloss.Width(t)))+"╮")}
	for _, l := range lines {
		out = append(out, b.Render("│")+" "+fit(l, w-4)+" "+b.Render("│"))
	}
	return append(out, b.Render("╰"+strings.Repeat("─", w-2)+"╯"))
}

// overlayAt draws box over base with its top at row y, centred across.
func overlayAt(base string, box []string, width, y int) string {
	lines := strings.Split(base, "\n")
	bw := 0
	for _, l := range box {
		bw = max(bw, lipgloss.Width(l))
	}
	x := max(0, (width-bw)/2)
	for i, l := range box {
		r := y + i
		if r < 0 || r >= len(lines) {
			continue
		}
		left := ansi.Cut(lines[r], 0, x)
		left += strings.Repeat(" ", max(0, x-lipgloss.Width(left)))
		lines[r] = left + l + ansi.Cut(lines[r], x+lipgloss.Width(l), width)
	}
	return strings.Join(lines, "\n")
}

// ------------------------------------------------------------- keys (?)

func (m Model) helpKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch s := k.String(); s {
	case "esc":
		if m.helpQ != "" {
			m.helpQ, m.helpTop = "", 0
		} else {
			m.mode = modeNormal
		}
	case "?", "ctrl+c":
		m.mode, m.helpQ, m.helpTop = modeNormal, "", 0
	case "up", "ctrl+p":
		m.helpTop = max(0, m.helpTop-1)
	case "down", "ctrl+n":
		m.helpTop++
	case "pgup":
		m.helpTop = max(0, m.helpTop-10)
	case "pgdown":
		m.helpTop += 10
	case "backspace":
		if r := []rune(m.helpQ); len(r) > 0 {
			m.helpQ, m.helpTop = string(r[:len(r)-1]), 0
		}
	case "ctrl+u":
		m.helpQ, m.helpTop = "", 0
	default:
		if k.Type == tea.KeyRunes || k.Type == tea.KeySpace {
			m.helpQ, m.helpTop = m.helpQ+string(k.Runes), 0
		}
	}
	return m, nil
}

var groupTitles = map[string]string{"Move": "Moving around", "Act": "Mail and chat", "Claude": "Claude", "Other": "Other",
	"Search": "Search window", "Compose": "Writing a mail", "Review": "Reviewing a message", "Approve": "Approving an agent's send"}

func (m Model) helpPopup() []string {
	s := m.styles
	w := min(m.w-4, 156)
	h := min(m.bodyHeight()-2, 46)
	iw := w - 4
	q := strings.ToLower(strings.TrimSpace(m.helpQ))
	// groups, in an order that reads well; the one for where you are first
	order := []string{"Move", "Act", "Claude", "Search", "Compose", "Review", "Approve", "Other"}
	type entry struct{ keys, desc string }
	groups := map[string][]entry{}
	n := 0
	for _, k := range Keys {
		if k.Group == "Setup" {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(k.Keys+" "+k.Desc+" "+k.Group), q) {
			continue
		}
		groups[k.Group] = append(groups[k.Group], entry{k.Keys, k.Desc})
		n++
	}
	cols := 2
	if iw >= 140 {
		cols = 3
	}
	colW := (iw - 3*(cols-1)) / cols
	var blocks [][]string
	for _, g := range order {
		es := groups[g]
		if len(es) == 0 {
			continue
		}
		// the key column as wide as this group's widest key
		keyW := 0
		for _, e := range es {
			keyW = max(keyW, lipgloss.Width(s.round(s.keycap, e.keys)))
		}
		keyW = min(keyW, 18)
		b := []string{s.accent.Bold(true).Render(groupTitles[g]) + s.muted.Render(fmt.Sprintf("  %d", len(es)))}
		for _, e := range es {
			kc := s.round(s.keycap, e.keys)
			if lipgloss.Width(kc) > keyW {
				kc = s.round(s.keycap, ansi.Truncate(e.keys, keyW-4, "…"))
			}
			// long descriptions wrap onto a second line, never cut short
			dl := wrap(e.desc, colW-keyW-1)
			if len(dl) > 2 {
				dl = append(dl[:1], ansi.Truncate(strings.Join(dl[1:], " "), colW-keyW-1, "…"))
			}
			for i, d := range dl {
				left := strings.Repeat(" ", keyW)
				if i == 0 {
					left = fit(kc, keyW)
				}
				style := s.text
				if i > 0 {
					style = s.sub
				}
				b = append(b, left+" "+highlight(d, strings.Fields(q), style, s.accent.Bold(true)))
			}
		}
		blocks = append(blocks, append(b, ""))
	}
	// fill the columns block by block, shortest column first
	columns := make([][]string, cols)
	for _, b := range blocks {
		i := 0
		for c := range columns {
			if len(columns[c]) < len(columns[i]) {
				i = c
			}
		}
		columns[i] = append(columns[i], b...)
	}
	rows := 0
	for _, c := range columns {
		rows = max(rows, len(c))
	}
	var body []string
	for r := 0; r < rows; r++ {
		var parts []string
		for c := range columns {
			l := ""
			if r < len(columns[c]) {
				l = columns[c][r]
			}
			parts = append(parts, fit(l, colW))
		}
		body = append(body, strings.Join(parts, s.muted.Render(" │ ")))
	}
	avail := h - 4
	top := min(m.helpTop, max(0, len(body)-avail))
	body = body[top:min(len(body), top+avail)]
	filter := s.muted.Render("type to filter")
	if q != "" {
		filter = s.accent.Render(" "+m.helpQ+"▏") + s.muted.Render(fmt.Sprintf("  %d keys", n))
	}
	head := row(filter, s.muted.Render("↑↓ scroll · esc "+map[bool]string{true: "clears", false: "closes"}[q != ""]+" · ? closes"), iw)
	lines := []string{head, s.muted.Render(strings.Repeat("─", iw))}
	lines = append(lines, body...)
	for len(lines) < h-2 && q != "" {
		lines = append(lines, "")
	}
	if n == 0 {
		lines = append(lines, s.muted.Render("  no key matches "+m.helpQ))
	}
	return m.popupFrame("keys", lines, w)
}

// ------------------------------------------------------------ file picker

type picker struct {
	then     string // "compose", "review" or "chat"
	root     string
	files    []string // relative to root
	shown    []string
	sel      int
	marked   map[string]bool
	input    textinput.Model
	scanning bool
	hidden   bool
	err      string
}

type pickerFilesMsg struct {
	root   string
	hidden bool
	files  []string
	err    error
}

// pickFiles opens the file picker; what you pick goes to then.
func (m *Model) pickFiles(then string) tea.Cmd {
	home, _ := os.UserHomeDir()
	dir, err := os.Getwd()
	if err != nil || dir == "/" || !strings.HasPrefix(dir, home) && !strings.HasPrefix(dir, os.TempDir()) {
		dir = home
	}
	in := newInput(m.styles)
	in.Placeholder = "type to find a file"
	in.Focus()
	m.pick = &picker{then: then, root: dir, marked: map[string]bool{}, input: in}
	m.pickMode = m.mode
	m.mode = modePick
	return m.scanFiles()
}

func (m *Model) scanFiles() tea.Cmd {
	p := m.pick
	p.scanning, p.files, p.shown, p.sel, p.err = true, nil, nil, 0, ""
	root, hidden := p.root, p.hidden
	return func() tea.Msg {
		files, err := listFiles(root, hidden, 30000)
		return pickerFilesMsg{root, hidden, files, err}
	}
}

// listFiles lists files under root (fd when there is one), skipping
// .git and node_modules, at most limit.
func listFiles(root string, hidden bool, limit int) ([]string, error) {
	if _, err := exec.LookPath("fd"); err == nil {
		args := []string{"--type", "f", "--exclude", ".git", "--exclude", "node_modules", "--max-results", fmt.Sprint(limit)}
		if hidden {
			args = append(args, "--hidden")
		}
		cmd := exec.Command("fd", append(args, ".")...)
		cmd.Dir = root
		out, err := cmd.Output()
		if err == nil {
			var files []string
			sc := bufio.NewScanner(bytes.NewReader(out))
			for sc.Scan() {
				files = append(files, strings.TrimPrefix(sc.Text(), "./"))
			}
			return files, nil
		}
	}
	var files []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		name := d.Name()
		if d.IsDir() && p != root && (name == "node_modules" || name == ".git" || !hidden && strings.HasPrefix(name, ".")) {
			return filepath.SkipDir
		}
		if !d.IsDir() && (hidden || !strings.HasPrefix(name, ".")) {
			rel, _ := filepath.Rel(root, p)
			files = append(files, rel)
			if len(files) >= limit {
				return fs.SkipAll
			}
		}
		return nil
	})
	return files, err
}

func (m *Model) gotFiles(msg pickerFilesMsg) {
	p := m.pick
	if p == nil || p.root != msg.root || p.hidden != msg.hidden {
		return
	}
	p.scanning = false
	if msg.err != nil {
		p.err = msg.err.Error()
	}
	// shallow first: what's right here before what's deep inside
	sort.SliceStable(msg.files, func(i, j int) bool {
		di, dj := strings.Count(msg.files[i], "/"), strings.Count(msg.files[j], "/")
		if di != dj {
			return di < dj
		}
		return msg.files[i] < msg.files[j]
	})
	p.files = msg.files
	m.filterFiles()
}

// filterFiles keeps the files whose path holds the typed letters in
// order, best first: in the name, together, short paths.
func (m *Model) filterFiles() {
	p := m.pick
	q := strings.ToLower(strings.TrimSpace(p.input.Value()))
	if q == "" {
		p.shown = p.files
		p.sel = min(p.sel, max(0, len(p.shown)-1))
		return
	}
	type hit struct {
		f     string
		score int
	}
	var hits []hit
	for _, f := range p.files {
		if sc, ok := fuzzy(strings.ToLower(f), q); ok {
			hits = append(hits, hit{f, sc})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].score > hits[j].score })
	p.shown = nil
	for i, h := range hits {
		if i == 500 {
			break
		}
		p.shown = append(p.shown, h.f)
	}
	p.sel = 0
}

func fuzzy(path, q string) (int, bool) {
	base := path[strings.LastIndex(path, "/")+1:]
	score := 0
	if strings.Contains(base, q) {
		score += 100
		if strings.HasPrefix(base, q) {
			score += 50
		}
	} else if strings.Contains(path, q) {
		score += 40
	}
	i := 0
	run := 0
	for _, r := range path {
		if i < len(q) && byte(r) == q[i] {
			i++
			run++
			score += run
		} else {
			run = 0
		}
	}
	if i < len(q) {
		return 0, false
	}
	return score - len(path)/4, true
}

func (m Model) pickerKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	p := m.pick
	close := func() { m.pick, m.mode = nil, m.pickMode }
	switch k.String() {
	case "esc", "ctrl+c":
		close()
		return m, nil
	case "up", "ctrl+p", "ctrl+k":
		p.sel = max(0, p.sel-1)
		return m, nil
	case "down", "ctrl+n", "ctrl+j":
		p.sel = min(max(0, len(p.shown)-1), p.sel+1)
		return m, nil
	case "pgup":
		p.sel = max(0, p.sel-10)
		return m, nil
	case "pgdown":
		p.sel = min(max(0, len(p.shown)-1), p.sel+10)
		return m, nil
	case "tab":
		if p.sel < len(p.shown) {
			f := p.shown[p.sel]
			p.marked[f] = !p.marked[f]
			if !p.marked[f] {
				delete(p.marked, f)
			}
			p.sel = min(max(0, len(p.shown)-1), p.sel+1)
		}
		return m, nil
	case "ctrl+r":
		// where to look: here, home, Downloads
		home, _ := os.UserHomeDir()
		next := home
		if p.root == home {
			next = filepath.Join(home, "Downloads")
		} else if p.root == filepath.Join(home, "Downloads") {
			if wd, err := os.Getwd(); err == nil {
				next = wd
			}
		}
		p.root = next
		p.marked = map[string]bool{}
		return m, m.scanFiles()
	case "ctrl+h":
		p.hidden = !p.hidden
		return m, m.scanFiles()
	case "enter":
		var paths []string
		for f := range p.marked {
			paths = append(paths, filepath.Join(p.root, f))
		}
		if len(paths) == 0 && p.sel < len(p.shown) {
			paths = []string{filepath.Join(p.root, p.shown[p.sel])}
		}
		sort.Strings(paths)
		then := p.then
		close()
		if len(paths) == 0 {
			return m, nil
		}
		return m.afterPick(pickedFilesMsg{paths: paths, then: then})
	}
	before := p.input.Value()
	var cmd tea.Cmd
	p.input, cmd = p.input.Update(k)
	if p.input.Value() != before {
		m.filterFiles()
	}
	return m, cmd
}

func (m Model) pickerPopup() []string {
	p := m.pick
	s := m.styles
	w := min(m.w-4, 156)
	h := min(m.bodyHeight()-2, 46)
	iw := w - 4
	listW := max(36, iw*45/100)
	prevW := iw - listW - 3
	in := p.input
	in.Width = listW - 4
	status := s.muted.Render(fmt.Sprintf("%d files", len(p.files)))
	if p.scanning {
		status = s.accent.Render("⟳ looking…")
	} else if p.input.Value() != "" {
		status = s.muted.Render(fmt.Sprintf("%d of %d", len(p.shown), len(p.files)))
	}
	if len(p.marked) > 0 {
		status = s.green.Render(fmt.Sprintf("%d picked · ", len(p.marked))) + status
	}
	lines := []string{row(s.accent.Bold(true).Render(" ")+in.View(), status, iw),
		s.muted.Render(" in " + tilde(p.root) + map[bool]string{true: "  (with hidden files)", false: ""}[p.hidden]),
		s.muted.Render(strings.Repeat("─", iw))}
	bodyH := h - 2 - len(lines) - 2
	top := max(0, p.sel-bodyH+3)
	words := []string{strings.ToLower(strings.TrimSpace(p.input.Value()))}
	var list []string
	for i := top; i < min(len(p.shown), top+bodyH); i++ {
		f := p.shown[i]
		dir, base := filepath.Split(f)
		mark := "  "
		if p.marked[f] {
			mark = s.green.Render("✓ ")
		}
		name := highlight(base, words, s.text, s.accent.Bold(true))
		l := mark + fileIcon(base, s) + " " + name + " " + s.muted.Render(ansi.Truncate(strings.TrimSuffix(dir, "/"), max(0, listW-lipgloss.Width(base)-8), "…"))
		if i == p.sel {
			l = s.accent.Bold(true).Render("▌") + l[1:]
		}
		list = append(list, ansi.Truncate(l, listW, "…"))
	}
	if len(p.shown) == 0 && !p.scanning {
		list = append(list, "", s.muted.Render("  no file matches"))
	}
	var prev []string
	if p.sel < len(p.shown) {
		prev = m.filePreview(filepath.Join(p.root, p.shown[p.sel]), prevW, bodyH)
	}
	for i := 0; i < bodyH; i++ {
		l, r := "", ""
		if i < len(list) {
			l = list[i]
		}
		if i < len(prev) {
			r = prev[i]
		}
		lines = append(lines, fit(l, listW)+s.muted.Render(" │ ")+fit(r, prevW))
	}
	lines = append(lines, s.muted.Render(strings.Repeat("─", iw)))
	lines = append(lines, s.muted.Render(" ↑↓ move · tab picks several · enter adds · ctrl+r here/home/Downloads · ctrl+h hidden · esc closes"))
	if p.err != "" {
		lines[len(lines)-1] = s.red.Render(" " + p.err)
	}
	title := "attach files"
	if p.then == "chat" {
		title = "send files into this chat"
	}
	return m.popupFrame(title, lines, w)
}

func fileIcon(name string, s styles) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".svg":
		return s.peach.Render("▣")
	case ".pdf":
		return s.red.Render("▤")
	case ".zip", ".gz", ".tar", ".7z", ".rar":
		return s.yellow.Render("▥")
	case ".go", ".js", ".ts", ".py", ".rs", ".sh", ".json", ".yaml", ".yml", ".toml", ".html", ".css":
		return s.blue.Render("▦")
	}
	return s.muted.Render("▢")
}

// filePreview describes a file and shows what's in it: text, an image's
// size, a PDF's first page (with pdftotext), or its type.
func (m Model) filePreview(path string, w, h int) []string {
	s := m.styles
	st, err := os.Stat(path)
	if err != nil {
		return []string{s.red.Render(err.Error())}
	}
	out := []string{s.bold.Render(ansi.Truncate(filepath.Base(path), w, "…")),
		s.muted.Render(app.HumanSize(st.Size()) + " · changed " + st.ModTime().Format("02 Jan 2006 15:04")), ""}
	if st.Size() > app.MaxAttach {
		out = append(out, s.red.Render(fmt.Sprintf("over %d MB: too big to attach", app.MaxAttach>>20)), "")
	}
	f, err := os.Open(path)
	if err != nil {
		return append(out, s.red.Render(err.Error()))
	}
	defer f.Close()
	head := make([]byte, 64<<10)
	n, _ := f.Read(head)
	head = head[:n]
	kind := http.DetectContentType(head)
	switch {
	case strings.HasPrefix(kind, "image/"):
		_, _ = f.Seek(0, 0)
		if cfg, format, err := image.DecodeConfig(f); err == nil {
			out = append(out, s.peach.Render(fmt.Sprintf("▣ %s image, %d × %d", strings.ToUpper(format), cfg.Width, cfg.Height)))
		} else {
			out = append(out, s.peach.Render("▣ "+kind))
		}
		// the picture itself, drawn in characters, when chafa is installed
		if _, err := exec.LookPath("chafa"); err == nil && h-len(out) > 4 {
			pic, err := exec.Command("chafa", "--format", "symbols", "--animate", "off", "--polite", "on",
				"--size", fmt.Sprintf("%dx%d", w, h-len(out)-1), path).Output()
			if err == nil {
				out = append(out, "")
				out = append(out, strings.Split(strings.TrimRight(string(pic), "\n"), "\n")...)
			}
		}
		return out
	case kind == "application/pdf":
		out = append(out, s.red.Render("▤ PDF"))
		if _, err := exec.LookPath("pdftotext"); err == nil {
			txt, err := exec.Command("pdftotext", "-l", "1", "-layout", path, "-").Output()
			if err == nil {
				out = append(out, "")
				for _, l := range strings.Split(string(txt), "\n") {
					if len(out) >= h {
						break
					}
					out = append(out, s.sub.Render(ansi.Truncate(strings.TrimRight(l, " "), w, "…")))
				}
			}
		}
		return out
	case bytes.IndexByte(head, 0) >= 0 || !utf8.Valid(head[:utf8Trim(head)]):
		return append(out, s.muted.Render("no preview: "+kind))
	}
	for i, l := range strings.Split(string(head), "\n") {
		if len(out) >= h {
			break
		}
		l = strings.ReplaceAll(strings.TrimRight(l, "\r"), "\t", "    ")
		out = append(out, s.muted.Render(fmt.Sprintf("%3d ", i+1))+s.text.Render(ansi.Truncate(l, w-4, "…")))
	}
	return out
}

// utf8Trim: the length of b without a rune cut off at the end.
func utf8Trim(b []byte) int {
	for i := len(b); i > 0 && i > len(b)-4; i-- {
		if utf8.Valid(b[:i]) {
			return i
		}
	}
	return len(b)
}
