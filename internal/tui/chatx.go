package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// Reactions (e on a picked message) and loading older messages (k past
// the first one).

var reactions = []string{"👍", "❤️", "😂", "🎉", "🙏", "👀", "✅", "🔥", "😮", "😢"}

func (m Model) reactKey(s string) (tea.Model, tea.Cmd) {
	switch s {
	case "esc", "q":
		m.mode = modeNormal
	case "left", "h":
		m.reactSel = (m.reactSel + len(reactions) - 1) % len(reactions)
	case "right", "l", "tab":
		m.reactSel = (m.reactSel + 1) % len(reactions)
	case "1", "2", "3", "4", "5", "6", "7", "8", "9", "0":
		n := int(s[0]-'0') - 1
		if n < 0 {
			n = 9
		}
		m.reactSel = n
		return m.sendReaction()
	case "enter", " ":
		return m.sendReaction()
	}
	return m, nil
}

func (m Model) sendReaction() (tea.Model, tea.Cmd) {
	m.mode = modeNormal
	if !m.pickingMsg() {
		return m, nil
	}
	c, emoji, a := m.chatMsgs[m.chatSel], reactions[m.reactSel], m.a
	return m, m.act("reacted "+emoji, func(ctx context.Context) error { return a.React(ctx, c.Account, c.Name, emoji) })
}

func (m Model) reactPopup() []string {
	s := m.styles
	var cells []string
	for i, e := range reactions {
		cell := fmt.Sprintf(" %s ", e)
		if i == m.reactSel {
			cell = s.accent.Bold(true).Render("[") + e + s.accent.Bold(true).Render("]")
		}
		cells = append(cells, cell)
	}
	lines := []string{strings.Join(cells, " "), s.muted.Render(" ←→ or 1-0 picks · enter reacts · esc")}
	return m.popupFrame("react", lines, 62)
}

type olderChatMsg struct {
	key string
	n   int
	err error
}

// loadOlderChat fetches the 50 messages before the oldest one shown.
func (m *Model) loadOlderChat() tea.Cmd {
	sp, ok := m.curSpace()
	if !ok || len(m.chatMsgs) == 0 || m.loadingOlder {
		return nil
	}
	m.loadingOlder = true
	m.say("loading earlier messages…")
	a, oldest, key := m.a, m.chatMsgs[0].Created, sp.Account+"/"+sp.Name
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		n, err := a.LoadOlderChat(ctx, sp.Account, sp.Name, oldest, 50)
		return olderChatMsg{key, n, err}
	}
}

func (m Model) gotOlderChat(msg olderChatMsg) (tea.Model, tea.Cmd) {
	m.loadingOlder = false
	if msg.err != nil {
		m.setErr("earlier messages: " + msg.err.Error())
		return m, nil
	}
	if msg.n == 0 {
		m.say("that's the start of the conversation")
		return m, nil
	}
	m.chatLimit = len(m.chatMsgs) + msg.n
	m.loadPreview(false)
	m.chatSel += msg.n // the same message stays picked
	m.fixView()
	m.say(fmt.Sprintf("%d earlier messages", msg.n))
	return m, nil
}
