package cli

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/Theyashsawarkar/sandesh/internal/app"
	"github.com/Theyashsawarkar/sandesh/internal/store"
)

// approve shows each send waiting for approval and asks y/e/n. It is what
// the tmux popup runs when an agent wants to send.
func approve(a *app.App, args []string) error {
	fs := flags("sandesh approve", "sandesh approve [id]\n\nAnswer the sends an agent asked for: y sends (after the undo window), e edits\nin your editor first, n declines, q leaves it waiting.")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	var items []store.OutboxItem
	if len(pos) > 0 {
		id, err := strconv.ParseInt(strings.TrimPrefix(pos[0], "#"), 10, 64)
		if err != nil {
			return usageError("bad id %q", pos[0])
		}
		it, err := a.Store.OutboxItem(id)
		if err != nil {
			return fmt.Errorf("no outbox item #%d", id)
		}
		items = []store.OutboxItem{it}
	} else {
		items, err = a.Store.Outbox(store.StatusApproval)
		if err != nil {
			return err
		}
	}
	if len(items) == 0 {
		fmt.Println("nothing is waiting for your approval")
		return nil
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return usageError("approve needs a terminal")
	}
	for _, it := range items {
		if it.Status != store.StatusApproval {
			fmt.Printf("#%d is %s, nothing to approve\n", it.ID, it.Status)
			continue
		}
		if err := approveOne(a, it); err != nil {
			return err
		}
	}
	return nil
}

func approveOne(a *app.App, it store.OutboxItem) error {
	out := it.Out
	edited := false
	for {
		fmt.Print("\033[2J\033[H")
		fmt.Print(RenderApproval(it, out))
		fmt.Printf("\n\033[1my\033[0m send   \033[1me\033[0m edit   \033[1mn\033[0m decline   \033[1mq\033[0m later  ")
		k, err := readKey()
		if err != nil {
			return err
		}
		fmt.Println()
		switch k {
		case 'y', 'Y':
			var e *store.Outgoing
			if edited {
				e = &out
			}
			if err := a.Approve(it.ID, e); err != nil {
				return err
			}
			fmt.Printf("Approved. Sends in %ds; `sandesh outbox cancel %d` stops it.\n", a.Config.UndoSeconds, it.ID)
			time.Sleep(1200 * time.Millisecond)
			return nil
		case 'n', 'N':
			if err := a.Decline(it.ID); err != nil {
				return err
			}
			fmt.Println("Declined. Nothing was sent.")
			time.Sleep(800 * time.Millisecond)
			return nil
		case 'q', 'Q', 3, 27:
			return nil
		case 'e', 'E':
			if it.Kind == "chat" {
				path, err := TempFile(a, "txt", ChatTemplate(out.SpaceName, out.Body))
				if err != nil {
					return err
				}
				if err := runEditor(a, path); err != nil {
					return err
				}
				b, _ := os.ReadFile(path)
				os.Remove(path)
				if text, ok := ParseChatTemplate(string(b)); ok {
					out.Body, edited = text, true
				}
				continue
			}
			res, err := EditMail(a, out, "")
			if err != nil {
				return err
			}
			if res != nil {
				out, edited = *res, true
			}
		}
	}
}

func runEditor(a *app.App, path string) error {
	cmd := EditorCmd(a, path)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}

func readKey() (byte, error) {
	fd := int(os.Stdin.Fd())
	old, err := term.MakeRaw(fd)
	if err != nil {
		return 0, err
	}
	defer term.Restore(fd, old)
	b := make([]byte, 1)
	_, err = os.Stdin.Read(b)
	return b[0], err
}

// RenderApproval is the text shown before an agent's send: exactly who
// gets what.
func RenderApproval(it store.OutboxItem, out store.Outgoing) string {
	var b strings.Builder
	fmt.Fprintf(&b, "\033[1mAn agent wants to send this %s\033[0m  (#%d, account %s)\n", it.Kind, it.ID, it.Account)
	if it.Reason != "" {
		fmt.Fprintf(&b, "\033[2mAsking because: %s\033[0m\n", it.Reason)
	}
	b.WriteString("\n")
	if it.Kind == "chat" {
		fmt.Fprintf(&b, "  To:      %s\n", firstNonEmpty(out.SpaceName, out.Space))
		if out.Thread != "" {
			fmt.Fprintf(&b, "  Thread:  %s\n", out.Thread)
		}
	} else {
		fmt.Fprintf(&b, "  To:      %s\n", strings.Join(out.To, ", "))
		if len(out.Cc) > 0 {
			fmt.Fprintf(&b, "  Cc:      %s\n", strings.Join(out.Cc, ", "))
		}
		if len(out.Bcc) > 0 {
			fmt.Fprintf(&b, "  Bcc:     %s\n", strings.Join(out.Bcc, ", "))
		}
		fmt.Fprintf(&b, "  Subject: %s\n", out.Subject)
		if out.ThreadID != "" {
			fmt.Fprintf(&b, "  Reply in thread %s\n", out.ThreadID)
		}
	}
	for _, f := range out.Files {
		fmt.Fprintf(&b, "  \033[33mFile:    📎 %s (%s) from %s\033[0m\n", f.Name, app.HumanSize(f.Size), f.Source)
	}
	b.WriteString("  " + strings.Repeat("─", 60) + "\n")
	for _, l := range strings.Split(out.Body, "\n") {
		b.WriteString("  " + l + "\n")
	}
	return b.String()
}

func firstNonEmpty(s ...string) string {
	for _, x := range s {
		if x != "" {
			return x
		}
	}
	return ""
}
