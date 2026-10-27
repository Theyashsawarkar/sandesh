package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/Theyashsawarkar/sandesh/internal/app"
	"github.com/Theyashsawarkar/sandesh/internal/cli"
	"github.com/Theyashsawarkar/sandesh/internal/store"
)

// Scheduling: a small "when?" window with quick picks, or a time typed the
// way you'd say it ("tomorrow 9am", "fri 6pm", "in 2h"). The message then
// waits in the outbox and the background service sends it at that time.

type whenPick struct {
	label string
	t     time.Time
}

type whenPicker struct {
	input    textinput.Model
	picks    []whenPick
	sel      int
	target   string // compose, review, chat (the inline message), outbox
	outboxID int64
	text     string // the chat message being scheduled
	back     mode
}

func quickPicks(now time.Time) []whenPick {
	at := func(d time.Time, h int) time.Time {
		return time.Date(d.Year(), d.Month(), d.Day(), h, 0, 0, 0, now.Location())
	}
	var p []whenPick
	in1h := now.Add(time.Hour).Truncate(5 * time.Minute)
	p = append(p, whenPick{"In an hour", in1h})
	if now.Hour() < 17 {
		p = append(p, whenPick{"This evening", at(now, 18)})
	}
	tom := now.AddDate(0, 0, 1)
	p = append(p, whenPick{"Tomorrow morning", at(tom, 9)}, whenPick{"Tomorrow afternoon", at(tom, 14)})
	mon := now.AddDate(0, 0, 1)
	for mon.Weekday() != time.Monday {
		mon = mon.AddDate(0, 0, 1)
	}
	p = append(p, whenPick{"Monday morning", at(mon, 9)})
	return p
}

func (m *Model) openWhen(target string) {
	in := newInput(m.styles)
	in.Placeholder = "or type: tomorrow 9am · fri 6pm · in 2h · 3 nov 10:30"
	in.Focus()
	m.when = &whenPicker{input: in, picks: quickPicks(time.Now()), target: target, back: m.mode}
	m.mode = modeWhen
}

func (w *whenPicker) chosen() (time.Time, error) {
	if q := strings.TrimSpace(w.input.Value()); q != "" {
		return app.ParseWhen(q, time.Now())
	}
	return w.picks[w.sel].t, nil
}

func (m Model) whenKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	w := m.when
	switch k.String() {
	case "esc", "ctrl+c":
		m.mode, m.when = w.back, nil
		return m, nil
	case "up", "ctrl+p":
		if w.input.Value() == "" {
			w.sel = max(0, w.sel-1)
		}
		return m, nil
	case "down", "ctrl+n":
		if w.input.Value() == "" {
			w.sel = min(len(w.picks)-1, w.sel+1)
		}
		return m, nil
	case "enter":
		t, err := w.chosen()
		if err != nil {
			m.setErr(err.Error())
			return m, nil
		}
		m.mode, m.when = w.back, nil
		return m.scheduleAt(w, t)
	}
	var cmd tea.Cmd
	w.input, cmd = w.input.Update(k)
	return m, cmd
}

// scheduleAt sends the message the picker was opened for at t.
func (m Model) scheduleAt(w *whenPicker, t time.Time) (tea.Model, tea.Cmd) {
	when := app.SendTime(t, time.Now())
	switch w.target {
	case "compose":
		if m.comp == nil {
			return m, nil
		}
		m.comp.base.ScheduledFor = t.UnixMilli()
		m.comp.armed = true // a missing subject doesn't stop a scheduled mail
		return m.composerSend()
	case "review":
		if m.review == nil {
			return m, nil
		}
		r := m.review
		r.out.ScheduledFor = t.UnixMilli()
		it, err := m.a.Queue(r.account, r.kind, r.out, app.OriginYou)
		if err != nil {
			m.setErr(err.Error())
			return m, nil
		}
		m.review, m.mode = nil, modeNormal
		return m, m.sent(it)
	case "chat":
		sp, ok := m.curSpace()
		if !ok || strings.TrimSpace(w.text) == "" {
			return m, nil
		}
		it, err := m.a.Queue(sp.Account, "chat", store.Outgoing{Space: sp.Name, SpaceName: cli.SpaceTitle(sp), Body: w.text, ScheduledFor: t.UnixMilli()}, app.OriginYou)
		if err != nil {
			m.setErr(err.Error())
			return m, nil
		}
		m.mode, m.input = modeNormal, nil
		return m, m.sent(it)
	case "outbox":
		if err := m.a.Reschedule(w.outboxID, t); err != nil {
			m.setErr(err.Error())
			return m, nil
		}
		m.reload()
		m.say("moved to " + when)
	}
	return m, nil
}

func (m Model) whenPopup() []string {
	w := m.when
	s := m.styles
	width := min(m.w-4, 76)
	iw := width - 4
	now := time.Now()
	in := w.input
	in.Width = iw - 4
	lines := []string{s.accent.Bold(true).Render("⏰ ") + in.View(), s.muted.Render(strings.Repeat("─", iw))}
	if q := strings.TrimSpace(w.input.Value()); q != "" {
		t, err := app.ParseWhen(q, now)
		if err != nil {
			lines = append(lines, "", s.red.Render(" "+err.Error()))
		} else {
			lines = append(lines, "", s.green.Render(" → "+t.Format("Monday 02 January, 15:04")), s.muted.Render("   "+app.SendTime(t, now)+" · enter schedules it"))
		}
		lines = append(lines, "")
	} else {
		for i, p := range w.picks {
			l := "  " + s.text.Render(fmt.Sprintf("%-20s", p.label))
			if i == w.sel {
				l = s.accent.Bold(true).Render("▌ "+fmt.Sprintf("%-20s", p.label)) + ""
			}
			lines = append(lines, row(l, s.muted.Render(p.t.Format("Mon 02 Jan, 15:04")), iw))
		}
	}
	lines = append(lines, s.muted.Render(strings.Repeat("─", iw)),
		s.muted.Render(" ↑↓ pick · enter schedules · esc cancels"),
		s.muted.Render(" the background service sends it then; this computer must be on"))
	return m.popupFrame("send later", lines, width)
}
