package render

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

var st = Styles{Text: lipgloss.NewStyle(), Bold: lipgloss.NewStyle(), Italic: lipgloss.NewStyle(), Heading: lipgloss.NewStyle(),
	Link: lipgloss.NewStyle(), Quote: lipgloss.NewStyle(), Muted: lipgloss.NewStyle(), Button: lipgloss.NewStyle(),
	Code: lipgloss.NewStyle(), TableHead: lipgloss.NewStyle(), Border: lipgloss.NewStyle()}

func TestHTML(t *testing.T) {
	src := `<html><head><style>.x{}</style></head><body>
<table width="100%"><tr><td><table><tr><td>
<div style="display:none">preheader junk</div>
<h2>What's in 3.8</h2>
<p>QA signed off on the <b>3.8 branch</b>. See <a href="https://x.example/notes">the notes</a>.</p>
<ul><li>Session refresh</li><li>Billing export</li></ul>
<table><tr><th>Check</th><th>Status</th></tr><tr><td>Unit tests</td><td>412 passed</td></tr></table>
<a href="https://x.example/checklist" style="background:#1a73e8;color:#fff">Open the checklist</a>
<img src="https://t.example/p.gif" width="1" height="1">
</td></tr></table></td></tr></table>
<div class="gmail_quote">On Mon, Arjun wrote:<blockquote>old text<br>more old</blockquote></div>
</body></html>`
	r := HTML(src, 60, st, false)
	out := strings.Join(r.Lines, "\n")
	for _, want := range []string{"What's in 3.8", "QA signed off on the 3.8 branch. See the notes[1].", "• Session refresh", "• Billing export",
		"│ Check      │ Status     │", "│ Unit tests │ 412 passed │", "[ Open the checklist ] [2]", "▸ "} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	for _, bad := range []string{"preheader", ".x{}", "old text"} {
		if strings.Contains(out, bad) {
			t.Errorf("%q leaked:\n%s", bad, out)
		}
	}
	if len(r.Links) != 2 || r.Links[1] != "https://x.example/checklist" || r.Folded == 0 {
		t.Errorf("links %v folded %d", r.Links, r.Folded)
	}
	shown := strings.Join(HTML(src, 60, st, true).Lines, "\n")
	if !strings.Contains(shown, "│ old text") {
		t.Errorf("quote not shown:\n%s", shown)
	}
}

func TestText(t *testing.T) {
	src := "Hi,\n\nSee https://example.com/a/very/long/path for details.\n\nOn Mon, Priya <p@x.example> wrote:\n> old\n> older"
	r := Text(src, 40, st, false)
	out := strings.Join(r.Lines, "\n")
	if !strings.Contains(out, "example.com/a/very/long/path[1]") || strings.Contains(out, "older") || !strings.Contains(out, "▸ 3 quoted lines") {
		t.Errorf("text:\n%s", out)
	}
	for _, l := range r.Lines {
		if len([]rune(l)) > 40 {
			t.Errorf("line too wide: %q", l)
		}
	}
}
