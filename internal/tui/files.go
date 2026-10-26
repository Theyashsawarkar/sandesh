package tui

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Theyashsawarkar/sandesh/internal/app"
	"github.com/Theyashsawarkar/sandesh/internal/cli"
	"github.com/Theyashsawarkar/sandesh/internal/store"
)

// a file that can be saved from what's on screen
type savable struct {
	label string
	save  func(ctx context.Context) (string, error)
}

func (m *Model) savables() []savable {
	var out []savable
	a := m.a
	if m.tab == tabMail {
		for _, msg := range m.msgs {
			for i, at := range msg.Attachments {
				msg, i := msg, i
				out = append(out, savable{
					label: fmt.Sprintf("%s\t%s · %s", at.Name, app.HumanSize(int64(at.Size)), firstNonEmpty(msg.FromName, msg.FromAddr)),
					save: func(ctx context.Context) (string, error) {
						return a.SaveMailAttachment(ctx, msg.Account, msg.ID, i, "")
					},
				})
			}
		}
		return out
	}
	for j := len(m.chatMsgs) - 1; j >= 0; j-- { // newest first
		cm := m.chatMsgs[j]
		for i, f := range cm.Files {
			cm, i := cm, i
			where := "Drive: opens in Chat (b)"
			if f.Ref != "" {
				where = cli.When(cm.Created)
			}
			out = append(out, savable{
				label: fmt.Sprintf("%s\t%s · %s", f.Name, cm.SenderName, where),
				save: func(ctx context.Context) (string, error) {
					return a.SaveChatAttachment(ctx, cm.Account, cm.Name, i, "")
				},
			})
		}
	}
	return out
}

// saveCmd saves attachments: the only one straight away, else the ones
// picked in fzf (Tab marks several). open also opens them.
func (m *Model) saveCmd(open bool) tea.Cmd {
	items := m.savables()
	if len(items) == 0 {
		m.say("no attachments here")
		return nil
	}
	run := func(picked []savable) tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		var saved []string
		for _, it := range picked {
			p, err := it.save(ctx)
			if err != nil {
				return doneMsg{err: err}
			}
			saved = append(saved, p)
			if open {
				_ = exec.Command("xdg-open", p).Start()
			}
		}
		verb := "saved"
		if open {
			verb = "opened"
		}
		if len(saved) == 1 {
			return doneMsg{msg: verb + " " + tilde(saved[0])}
		}
		return doneMsg{msg: fmt.Sprintf("%s %d files in %s", verb, len(saved), tilde(filepath.Dir(saved[0])))}
	}
	if len(items) == 1 {
		m.say("downloading " + strings.Split(items[0].label, "\t")[0] + "…")
		return func() tea.Msg { return run(items) }
	}
	if _, err := exec.LookPath("fzf"); err != nil {
		m.say("several attachments: saving all (install fzf to pick)")
		return func() tea.Msg { return run(items) }
	}
	var lines []string
	for i, it := range items {
		lines = append(lines, fmt.Sprintf("%d\t%s", i, it.label))
	}
	var out bytes.Buffer
	cmd := exec.Command("fzf", "--multi", "--prompt", "save> ", "--with-nth", "2..", "--delimiter", "\t", "--height", "40%", "--reverse",
		"--header", "Enter saves · Tab picks several · Ctrl+a picks all · Esc cancels", "--bind", "ctrl-a:select-all")
	fzfColors(cmd)
	cmd.Stdin = strings.NewReader(strings.Join(lines, "\n"))
	cmd.Stdout = &out
	return tea.ExecProcess(cmd, func(err error) tea.Msg {
		if err != nil {
			return doneMsg{}
		}
		var picked []savable
		for _, l := range strings.Split(strings.TrimSpace(out.String()), "\n") {
			var i int
			if _, err := fmt.Sscanf(l, "%d\t", &i); err == nil && i >= 0 && i < len(items) {
				picked = append(picked, items[i])
			}
		}
		if len(picked) == 0 {
			return doneMsg{}
		}
		return run(picked)
	})
}

func fzfColors(cmd *exec.Cmd) {
	if c := os.Getenv("VARNA_FZF_COLORS"); c != "" {
		cmd.Args = append(cmd.Args, "--color", c)
	}
}

func tilde(p string) string {
	if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(p, home+"/") {
		return "~" + p[len(home):]
	}
	return p
}

type pickedFilesMsg struct {
	paths []string
	then  string // "chat" uploads into the space; "review" adds to the message
}

func (m Model) afterPick(msg pickedFilesMsg) (tea.Model, tea.Cmd) {
	var files []store.File
	for _, p := range msg.paths {
		files = append(files, store.File{Source: tilde(p)})
	}
	switch msg.then {
	case "compose":
		if m.comp != nil {
			m.comp.files = append(m.comp.files, files...)
			m.comp.focus = fFiles
			m.comp.fileSel = len(m.comp.files) - 1
			m.comp.applyFocus()
			m.mode = modeCompose
		}
		return m, nil
	case "review":
		if m.review != nil {
			m.review.out.Files = append(m.review.out.Files, files...)
			m.mode = modeReview
		}
		return m, nil
	case "chat":
		sp, ok := m.curSpace()
		if !ok {
			return m, nil
		}
		// shown for review first, like any message you write
		m.review = &review{kind: "chat", account: sp.Account,
			out: store.Outgoing{Space: sp.Name, SpaceName: cli.SpaceTitle(sp), Files: files}}
		m.mode = modeReview
	}
	return m, nil
}
