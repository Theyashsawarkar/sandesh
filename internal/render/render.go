// Package render turns a mail body (HTML or plain text) into styled,
// wrapped terminal lines: headings, bold and italics, lists, real data
// tables, buttons, numbered links, and quoted replies that fold away.
// Layout tables (what most HTML mail is built from) are flattened.
package render

import (
	"regexp"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"

	"github.com/Theyashsawarkar/sandesh/internal/mime"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// Styles are the looks the renderer uses.
type Styles struct {
	Text, Bold, Italic, Heading, Link, Quote, Muted, Button, Code, TableHead, Border lipgloss.Style
}

// Result is a rendered body.
type Result struct {
	Lines  []string
	Links  []string // Links[i] is link [i+1]
	Folded int      // quoted lines hidden (0 when shown or none)
}

type span struct {
	text  string
	bold  bool
	ital  bool
	link  int // 1-based link number on the last word of the link text
	inA   bool
	code  bool
	plain bool // keep spacing (pre)
}

type kind int

const (
	kPara kind = iota
	kHeading
	kItem
	kTable
	kRule
	kButton
)

type block struct {
	kind   kind
	spans  []span
	quoted bool
	depth  int // list nesting
	marker string
	rows   [][][]span // tables
	head   []bool
}

type builder struct {
	blocks []block
	cur    *block
	links  []string
	quote  int
	list   []string // list markers stack ("•" or counters)
	counts []int
	pre    int
}

func (b *builder) flush() {
	if b.cur != nil {
		if hasText(b.cur.spans) || b.cur.kind == kRule {
			b.blocks = append(b.blocks, *b.cur)
		}
		b.cur = nil
	}
}

func (b *builder) start(k kind) {
	b.flush()
	b.cur = &block{kind: k, quoted: b.quote > 0, depth: len(b.list)}
}

func (b *builder) add(s span) {
	if b.cur == nil {
		b.start(kPara)
	}
	b.cur.spans = append(b.cur.spans, s)
}

func hasText(sp []span) bool {
	for _, s := range sp {
		if strings.TrimSpace(s.text) != "" {
			return true
		}
	}
	return false
}

var (
	reWS     = regexp.MustCompile(`[ \t\r\n\f\x{a0}]+`)
	reHidden = regexp.MustCompile(`(?i)display\s*:\s*none|max-height\s*:\s*0|font-size\s*:\s*0|opacity\s*:\s*0[;\s]`)
	reBtn    = regexp.MustCompile(`(?i)background(-color)?\s*:|\bbtn\b|button|cta`)
	reQuote  = regexp.MustCompile(`(?i)gmail_quote|moz-cite|yahoo_quoted|protonmail_quote|OutlookMessageHeader|divRplyFwdMsg`)
	reZW     = regexp.MustCompile(`[\x{200b}\x{200c}\x{200d}\x{2060}\x{feff}\x{034f}\x{00ad}]`)
)

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if strings.EqualFold(a.Key, key) {
			return a.Val
		}
	}
	return ""
}

// HTML renders an HTML body at width w.
func HTML(src string, w int, st Styles, showQuoted bool) Result {
	src = mime.Clean(src)
	doc, err := html.Parse(strings.NewReader(src))
	if err != nil {
		return Text(src, w, st, showQuoted)
	}
	b := &builder{}
	b.walk(doc, span{})
	b.flush()
	return layout(b.blocks, b.links, w, st, showQuoted)
}

func isBlock(a atom.Atom) bool {
	switch a {
	case atom.P, atom.Div, atom.Section, atom.Article, atom.Header, atom.Footer, atom.Center, atom.Main, atom.Aside,
		atom.H1, atom.H2, atom.H3, atom.H4, atom.H5, atom.H6, atom.Ul, atom.Ol, atom.Li, atom.Blockquote,
		atom.Table, atom.Tr, atom.Td, atom.Th, atom.Tbody, atom.Thead, atom.Hr, atom.Pre, atom.Dl, atom.Dt, atom.Dd, atom.Form:
		return true
	}
	return false
}

func (b *builder) walk(n *html.Node, st span) {
	switch n.Type {
	case html.TextNode:
		t := mime.Clean(reZW.ReplaceAllString(n.Data, ""))
		if b.pre == 0 {
			t = reWS.ReplaceAllString(t, " ")
		}
		if t == "" {
			return
		}
		s := st
		s.text = t
		s.plain = b.pre > 0
		b.add(s)
		return
	case html.CommentNode, html.DoctypeNode:
		return
	case html.ElementNode:
		switch n.DataAtom {
		case atom.Head, atom.Style, atom.Script, atom.Title, atom.Meta, atom.Link, atom.Noscript, atom.Template, atom.Svg:
			return
		}
		if reHidden.MatchString(attr(n, "style")) || attr(n, "hidden") != "" || strings.EqualFold(attr(n, "aria-hidden"), "true") && n.DataAtom != atom.Img {
			return
		}
	}

	quoted := n.Type == html.ElementNode && (n.DataAtom == atom.Blockquote || reQuote.MatchString(attr(n, "class")+" "+attr(n, "id")))
	if quoted {
		b.flush()
		b.quote++
		defer func() { b.flush(); b.quote-- }()
	}

	if n.Type == html.ElementNode {
		switch n.DataAtom {
		case atom.Br:
			if b.cur != nil {
				b.add(span{text: "\n"})
			}
			return
		case atom.Hr:
			b.start(kRule)
			b.flush()
			return
		case atom.Img:
			alt := strings.TrimSpace(attr(n, "alt"))
			wd, ht := attr(n, "width"), attr(n, "height")
			if alt != "" && wd != "1" && ht != "1" && len(alt) < 80 {
				s := st
				s.text, s.ital = "["+alt+"]", true
				b.add(s)
			}
			return
		case atom.B, atom.Strong:
			st.bold = true
		case atom.I, atom.Em, atom.Cite:
			st.ital = true
		case atom.Code, atom.Tt, atom.Kbd:
			st.code = true
		case atom.Pre:
			b.start(kPara)
			b.pre++
			defer func() { b.pre--; b.flush() }()
		case atom.H1, atom.H2, atom.H3, atom.H4, atom.H5, atom.H6:
			b.start(kHeading)
			defer b.flush()
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				b.walk(c, st)
			}
			return
		case atom.Ul, atom.Ol:
			b.flush()
			b.list = append(b.list, map[bool]string{true: "ol", false: "ul"}[n.DataAtom == atom.Ol])
			b.counts = append(b.counts, 0)
			defer func() { b.flush(); b.list = b.list[:len(b.list)-1]; b.counts = b.counts[:len(b.counts)-1] }()
		case atom.Li:
			b.start(kItem)
			if l := len(b.list); l > 0 {
				b.counts[l-1]++
				if b.list[l-1] == "ol" {
					b.cur.marker = itoa(b.counts[l-1]) + "."
				} else {
					b.cur.marker = []string{"•", "◦", "▪"}[(l-1)%3]
				}
			} else {
				b.cur.marker = "•"
			}
			defer b.flush()
		case atom.Table:
			if b.dataTable(n, st) {
				return
			}
		case atom.A:
			href := strings.TrimSpace(attr(n, "href"))
			text := strings.TrimSpace(textOf(n))
			if href != "" && !strings.HasPrefix(href, "mailto:") && !strings.HasPrefix(href, "#") && !strings.HasPrefix(strings.ToLower(href), "javascript:") {
				if reBtn.MatchString(attr(n, "style")+" "+attr(n, "class")+" "+attr(n, "role")) && text != "" && len(text) < 60 {
					b.links = append(b.links, href)
					b.start(kButton)
					b.cur.spans = []span{{text: text, link: len(b.links)}}
					b.flush()
					return
				}
				if text == "" {
					// an image link with no alt text: keep the link, say so
					text = "link"
				}
				b.links = append(b.links, href)
				num := len(b.links)
				before := len(b.curSpans())
				st.inA = true
				for c := n.FirstChild; c != nil; c = c.NextSibling {
					b.walk(c, st)
				}
				if b.cur != nil && len(b.cur.spans) > before {
					b.cur.spans[len(b.cur.spans)-1].link = num
				} else {
					b.add(span{text: text, inA: true, link: num})
				}
				return
			}
		}
	}
	block := n.Type == html.ElementNode && isBlock(n.DataAtom) && n.DataAtom != atom.Li && n.DataAtom != atom.Ul && n.DataAtom != atom.Ol && n.DataAtom != atom.Pre
	if block {
		b.flush()
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		b.walk(c, st)
	}
	if block {
		b.flush()
	}
}

func (b *builder) curSpans() []span {
	if b.cur == nil {
		return nil
	}
	return b.cur.spans
}

func textOf(n *html.Node) string {
	var sb strings.Builder
	var f func(*html.Node)
	f = func(n *html.Node) {
		if n.Type == html.TextNode {
			sb.WriteString(n.Data)
		}
		if n.Type == html.ElementNode && n.DataAtom == atom.Img {
			sb.WriteString(attr(n, "alt"))
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			f(c)
		}
	}
	f(n)
	return reWS.ReplaceAllString(reZW.ReplaceAllString(sb.String(), ""), " ")
}

// dataTable renders a table that holds data (no nested tables, several
// columns, short cells) as a grid; anything else is layout and is walked.
func (b *builder) dataTable(n *html.Node, st span) bool {
	var rows [][]*html.Node
	var head []bool
	nested := false
	var find func(*html.Node, bool)
	find = func(x *html.Node, top bool) {
		for c := x.FirstChild; c != nil; c = c.NextSibling {
			if c.Type != html.ElementNode {
				continue
			}
			switch c.DataAtom {
			case atom.Table:
				nested = true
			case atom.Tr:
				var cells []*html.Node
				isHead := false
				for d := c.FirstChild; d != nil; d = d.NextSibling {
					if d.Type == html.ElementNode && (d.DataAtom == atom.Td || d.DataAtom == atom.Th) {
						cells = append(cells, d)
						if d.DataAtom == atom.Th {
							isHead = true
						}
						var deep func(*html.Node)
						deep = func(y *html.Node) {
							for z := y.FirstChild; z != nil; z = z.NextSibling {
								if z.Type == html.ElementNode && (z.DataAtom == atom.Table || z.DataAtom == atom.Img || z.DataAtom == atom.Div && strings.TrimSpace(textOf(z)) == "") {
									nested = true
								}
								deep(z)
							}
						}
						deep(d)
					}
				}
				rows = append(rows, cells)
				head = append(head, isHead)
			default:
				find(c, false)
			}
		}
	}
	find(n, true)
	if nested || len(rows) < 2 {
		return false
	}
	cols := 0
	for _, r := range rows {
		cols = max(cols, len(r))
		for _, c := range r {
			if t := strings.TrimSpace(textOf(c)); runewidth.StringWidth(t) > 50 {
				return false
			}
		}
	}
	if cols < 2 || cols > 6 {
		return false
	}
	b.flush()
	blk := block{kind: kTable, quoted: b.quote > 0, head: head}
	for _, r := range rows {
		var row [][]span
		for _, c := range r {
			sub := &builder{links: b.links}
			sub.walk(c, st)
			sub.flush()
			b.links = sub.links
			var spans []span
			for _, bl := range sub.blocks {
				if len(spans) > 0 {
					spans = append(spans, span{text: " "})
				}
				spans = append(spans, bl.spans...)
			}
			row = append(row, spans)
		}
		blk.rows = append(blk.rows, row)
	}
	b.blocks = append(b.blocks, blk)
	return true
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var d []byte
	for n > 0 {
		d = append([]byte{byte('0' + n%10)}, d...)
		n /= 10
	}
	return string(d)
}

// ------------------------------------------------------------------ text

var (
	reURL   = regexp.MustCompile(`https?://[^\s<>()\[\]"']+[^\s<>()\[\]"'.,;:!?]`)
	reWrote = regexp.MustCompile(`(?i)^(on .+wrote:|.+ <[^>]+> wrote:|-+ ?(original message|forwarded message) ?-+)$`)
)

// Text renders a plain-text body: paragraphs, numbered links, quoted
// lines folded.
func Text(src string, w int, st Styles, showQuoted bool) Result {
	src = mime.Clean(src)
	var blocks []block
	var links []string
	lines := strings.Split(strings.ReplaceAll(src, "\r\n", "\n"), "\n")
	var cur *block
	flush := func() {
		if cur != nil && hasText(cur.spans) {
			blocks = append(blocks, *cur)
		}
		cur = nil
	}
	inQuote := false
	for i, l := range lines {
		t := strings.TrimRight(l, " ")
		q := strings.HasPrefix(strings.TrimSpace(t), ">")
		if reWrote.MatchString(strings.TrimSpace(t)) && i+1 < len(lines) {
			inQuote = true
		}
		quoted := q || inQuote
		if t == "" && !quoted {
			flush()
			continue
		}
		if cur != nil && cur.quoted != quoted {
			flush()
		}
		if cur == nil {
			cur = &block{kind: kPara, quoted: quoted}
		} else {
			cur.spans = append(cur.spans, span{text: "\n"})
		}
		if q {
			t = strings.TrimLeft(strings.TrimSpace(t), "> ")
		}
		// links
		last := 0
		for _, m := range reURL.FindAllStringIndex(t, -1) {
			if m[0] > last {
				cur.spans = append(cur.spans, span{text: t[last:m[0]], plain: true})
			}
			links = append(links, t[m[0]:m[1]])
			cur.spans = append(cur.spans, span{text: shortURL(t[m[0]:m[1]]), inA: true, link: len(links)})
			last = m[1]
		}
		if last < len(t) {
			cur.spans = append(cur.spans, span{text: t[last:], plain: true})
		}
	}
	flush()
	return layout(blocks, links, w, st, showQuoted)
}

func shortURL(u string) string {
	s := strings.TrimPrefix(strings.TrimPrefix(u, "https://"), "http://")
	if r := []rune(s); len(r) > 48 {
		return string(r[:47]) + "…"
	}
	return s
}

// ---------------------------------------------------------------- layout

func styleOf(s span, st Styles, base lipgloss.Style) lipgloss.Style {
	out := base
	switch {
	case s.inA || s.link > 0:
		out = st.Link
	case s.code:
		out = st.Code
	}
	if s.bold {
		out = out.Bold(true)
	}
	if s.ital {
		out = out.Italic(true)
	}
	return out
}

type word struct {
	text  string
	style lipgloss.Style
	space bool // a space before it
	brk   bool // a line break before it
}

// wrapSpans wraps styled spans to width w.
func wrapSpans(spans []span, w int, st Styles, base lipgloss.Style) []string {
	var words []word
	space := false
	for _, s := range spans {
		sty := styleOf(s, st, base)
		parts := strings.Split(s.text, "\n")
		for pi, p := range parts {
			if pi > 0 {
				words = append(words, word{brk: true})
				space = false
			}
			if s.plain {
				// keep runs of spaces in plain text
				fields := strings.SplitAfter(p, " ")
				for _, f := range fields {
					if f == "" {
						continue
					}
					words = append(words, word{text: f, style: sty})
				}
				continue
			}
			if strings.HasPrefix(p, " ") {
				space = true
			}
			fs := strings.Fields(p)
			for i, f := range fs {
				words = append(words, word{text: f, style: sty, space: space || i > 0})
				space = false
			}
			if strings.HasSuffix(p, " ") {
				space = true
			}
		}
		if s.link > 0 && len(words) > 0 {
			words = append(words, word{text: "[" + itoa(s.link) + "]", style: st.Muted})
		}
	}
	var lines []string
	var cur strings.Builder
	cw := 0
	emit := func() {
		lines = append(lines, cur.String())
		cur.Reset()
		cw = 0
	}
	for _, wd := range words {
		if wd.brk {
			emit()
			continue
		}
		t := wd.text
		tw := runewidth.StringWidth(t)
		gap := 0
		if wd.space && cw > 0 {
			gap = 1
		}
		if cw+gap+tw > w && cw > 0 {
			emit()
			gap = 0
			t = strings.TrimLeft(t, " ")
			tw = runewidth.StringWidth(t)
		}
		for tw > w { // a word longer than the line
			cut := runewidth.Truncate(t, w, "")
			cur.WriteString(wd.style.Render(cut))
			emit()
			t = t[len(cut):]
			tw = runewidth.StringWidth(t)
		}
		if gap > 0 {
			cur.WriteString(" ")
		}
		cur.WriteString(wd.style.Render(t))
		cw += gap + tw
	}
	if cw > 0 || len(lines) == 0 {
		emit()
	}
	return lines
}

func layout(blocks []block, links []string, w int, st Styles, showQuoted bool) Result {
	if w < 20 {
		w = 20
	}
	var res Result
	res.Links = links
	var out []string
	gap := func() {
		if len(out) > 0 && out[len(out)-1] != "" {
			out = append(out, "")
		}
	}
	folded := 0
	foldAt := -1
	prev := kind(-1)
	for _, bl := range blocks {
		var lines []string
		bar := ""
		width := w
		if bl.quoted {
			bar = st.Quote.Render("│ ")
			width = w - 2
		}
		switch bl.kind {
		case kHeading:
			for _, l := range wrapSpans(bl.spans, width, st, st.Heading) {
				lines = append(lines, l)
			}
		case kItem:
			ind := strings.Repeat("  ", max(0, bl.depth-1))
			mk := ind + bl.marker + " "
			mw := runewidth.StringWidth(mk)
			for i, l := range wrapSpans(bl.spans, width-mw, st, st.Text) {
				if i == 0 {
					lines = append(lines, st.Link.UnsetUnderline().Render(mk)+l)
				} else {
					lines = append(lines, strings.Repeat(" ", mw)+l)
				}
			}
		case kRule:
			lines = append(lines, st.Muted.Render(strings.Repeat("─", min(width, 40))))
		case kButton:
			text := strings.TrimSpace(bl.spans[0].text)
			lines = append(lines, st.Button.Render("[ "+text+" ]")+st.Muted.Render(" ["+itoa(bl.spans[0].link)+"]"))
		case kTable:
			lines = append(lines, table(bl, width, st)...)
		default:
			lines = wrapSpans(bl.spans, width, st, st.Text)
		}
		if bl.quoted {
			if !showQuoted {
				if foldAt < 0 {
					gap()
					foldAt = len(out)
					out = append(out, "")
				}
				folded += len(lines)
				continue
			}
			for i := range lines {
				lines[i] = bar + st.Quote.Render(stripStyle(lines[i]))
			}
		}
		// list items sit together; everything else gets a blank line before
		if bl.kind != kItem || prev != kItem {
			gap()
		}
		prev = bl.kind
		out = append(out, lines...)
	}
	if foldAt >= 0 {
		out[foldAt] = st.Muted.Render("▸ " + itoa(folded) + " quoted lines (Q shows them)")
		res.Folded = folded
	}
	for len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	res.Lines = out
	return res
}

var reANSI = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func stripStyle(s string) string { return reANSI.ReplaceAllString(s, "") }

func table(bl block, w int, st Styles) []string {
	cols := 0
	for _, r := range bl.rows {
		cols = max(cols, len(r))
	}
	texts := make([][]string, len(bl.rows))
	widths := make([]int, cols)
	for i, r := range bl.rows {
		texts[i] = make([]string, cols)
		for j := 0; j < cols; j++ {
			if j < len(r) {
				var sb strings.Builder
				for _, s := range r[j] {
					sb.WriteString(s.text)
				}
				texts[i][j] = strings.TrimSpace(reWS.ReplaceAllString(sb.String(), " "))
			}
			widths[j] = max(widths[j], runewidth.StringWidth(texts[i][j]))
		}
	}
	// fit: shrink the widest column until the table fits
	total := func() int {
		t := 1
		for _, x := range widths {
			t += x + 3
		}
		return t
	}
	for total() > w {
		mi := 0
		for j := range widths {
			if widths[j] > widths[mi] {
				mi = j
			}
		}
		if widths[mi] <= 4 {
			break
		}
		widths[mi]--
	}
	bd := st.Border
	line := func(l, m, r string) string {
		var parts []string
		for _, x := range widths {
			parts = append(parts, strings.Repeat("─", x+2))
		}
		return bd.Render(l + strings.Join(parts, m) + r)
	}
	var out []string
	out = append(out, line("╭", "┬", "╮"))
	for i, row := range texts {
		var cells []string
		for j, t := range row {
			t = runewidth.Truncate(t, widths[j], "…")
			pad := strings.Repeat(" ", widths[j]-runewidth.StringWidth(t))
			sty := st.Text
			if bl.head != nil && i < len(bl.head) && bl.head[i] || i == 0 && allBold(bl.rows[0]) {
				sty = st.TableHead
			}
			cells = append(cells, " "+sty.Render(t)+pad+" ")
		}
		out = append(out, bd.Render("│")+strings.Join(cells, bd.Render("│"))+bd.Render("│"))
		if i == 0 && (len(bl.head) > 0 && bl.head[0] || allBold(bl.rows[0])) && len(texts) > 1 {
			out = append(out, line("├", "┼", "┤"))
		}
	}
	out = append(out, line("╰", "┴", "╯"))
	return out
}

func allBold(row [][]span) bool {
	if len(row) == 0 {
		return false
	}
	for _, c := range row {
		any := false
		for _, s := range c {
			if strings.TrimSpace(s.text) != "" {
				if !s.bold {
					return false
				}
				any = true
			}
		}
		if !any {
			return false
		}
	}
	return true
}
