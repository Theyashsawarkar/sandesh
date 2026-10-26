package tui

import (
	"os"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Theyashsawarkar/sandesh/internal/app"
)

func TestTimingOnRealData(t *testing.T) {
	// BENCH_HOME=<a copy of a sandesh home> go test ./internal/tui -run Timing -v
	if os.Getenv("BENCH_HOME") == "" {
		t.Skip()
	}
	t.Setenv("SANDESH_HOME", os.Getenv("BENCH_HOME"))
	t.Setenv("SANDESH_GOOGLE_URL", "")
	a, err := app.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	m := New(a, "work")
	mm, _ := m.Update(tea.WindowSizeMsg{Width: 200, Height: 50})
	m = mm.(Model)
	timeit := func(name string, n int, f func()) {
		s := time.Now()
		for i := 0; i < n; i++ {
			f()
		}
		t.Logf("%-28s %v", name, time.Since(s)/time.Duration(n))
	}
	timeit("reload (every 3 s)", 20, func() { m.reload() })
	timeit("View", 50, func() { _ = m.View() })
	timeit("key j (move + preview)", 50, func() { x, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")}); m = x.(Model) })
	x, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = x.(Model)
	timeit("chat reload", 20, func() { m.reload() })
	timeit("chat View", 50, func() { _ = m.View() })
	timeit("chat key j", 50, func() { x, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")}); m = x.(Model) })
	timeit("recolour check", 50, func() { m.recolour() })
	timeit("DaemonRunning", 50, func() { a.DaemonRunning() })
}

func TestSearchTimingOnRealData(t *testing.T) {
	if os.Getenv("BENCH_HOME") == "" {
		t.Skip()
	}
	t.Setenv("SANDESH_HOME", os.Getenv("BENCH_HOME"))
	t.Setenv("SANDESH_GOOGLE_URL", "")
	a, err := app.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	m := New(a, "work")
	mm, _ := m.Update(tea.WindowSizeMsg{Width: 200, Height: 50})
	m = mm.(Model)
	m.openFinder()
	for _, q := range []string{"s", "re", "release", "naeem", "deploy build"} {
		m.find.input.SetValue(q)
		m.findChanged()
		s := time.Now()
		cmd := m.findLocal(m.find.seq)
		if cmd != nil {
			nm, _ := m.Update(cmd())
			m = nm.(Model)
		}
		t.Logf("local %-14q %4d results  %v", q, m.find.count(), time.Since(s))
	}
	s := time.Now()
	for i := 0; i < 20; i++ {
		_ = m.View()
	}
	t.Logf("View with the window open: %v", time.Since(s)/20)
}
