package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// VarnaPalette is the desktop theme varna (github.com/Theyashsawarkar/varna)
// writes; sandesh follows it, and recolours live when it changes.
func VarnaPalette() string {
	dir := os.Getenv("XDG_STATE_HOME")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(dir, "varna", "palette.json")
}

// applyVarna overlays the theme's colours: sandesh's own pick under
// tools.sandesh if the theme has one, else the desktop's. A missing or
// broken palette leaves c alone.
func applyVarna(c *Colors) {
	if os.Getenv("SANDESH_NO_VARNA") != "" {
		return
	}
	b, err := os.ReadFile(VarnaPalette())
	if err != nil {
		return
	}
	type theme struct {
		Colors map[string]any `json:"colors"`
	}
	var data struct {
		theme
		Tools map[string]theme `json:"tools"`
	}
	if json.Unmarshal(b, &data) != nil {
		return
	}
	cols := data.Colors
	if t, ok := data.Tools["sandesh"]; ok && len(t.Colors) > 0 {
		cols = t.Colors
	}
	pick := func(dst *string, names ...string) {
		for _, n := range names {
			if v, ok := cols[n].(string); ok && strings.HasPrefix(v, "#") && len(v) >= 7 {
				*dst = v[:7]
				return
			}
		}
	}
	pick(&c.Accent, "accent")
	pick(&c.Accent2, "accent2", "pink")
	pick(&c.Text, "text", "fg")
	pick(&c.Subtle, "subtext0", "subtext1")
	pick(&c.Muted, "overlay1", "muted")
	pick(&c.Surface, "surface0", "selection")
	pick(&c.Border, "surface2", "frame", "line")
	pick(&c.Base, "base", "bg")
	pick(&c.Mantle, "mantle", "crust")
	pick(&c.Green, "green", "ok")
	pick(&c.Yellow, "yellow", "warn")
	pick(&c.Red, "red", "error")
	pick(&c.Blue, "blue", "info")
	pick(&c.Peach, "peach", "maroon")
}

// ThemeStamp changes whenever a file the colours come from changes (the
// config, its include, varna's palette), so the screen knows to recolour.
func ThemeStamp(p Paths, c Config) string {
	var b strings.Builder
	files := []string{p.ConfigFile(), VarnaPalette()}
	if c.Include != "" {
		files = append(files, expand(c.Include))
	}
	for _, f := range files {
		if st, err := os.Stat(f); err == nil {
			b.WriteString(st.ModTime().Format(time.RFC3339Nano))
		}
		b.WriteByte('|')
	}
	return b.String()
}
