package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/Theyashsawarkar/sandesh/internal/config"
	"github.com/Theyashsawarkar/sandesh/internal/mime"
	"github.com/Theyashsawarkar/sandesh/internal/store"
)

// Origins of a send.
const (
	OriginYou   = "you"
	OriginAgent = "agent"
)

// Queue puts a message in the outbox. Yours go straight to the undo
// window; an agent's go through the send policy and usually wait for
// your approval first. Returns the outbox item.
func (a *App) Queue(account, kind string, out store.Outgoing, origin string) (store.OutboxItem, error) {
	if err := validate(kind, out); err != nil {
		return store.OutboxItem{}, err
	}
	it := store.OutboxItem{Account: account, Kind: kind, Out: out, Origin: origin}
	if origin == OriginAgent {
		if reason := a.needsApproval(account, kind, out); reason != "" {
			it.Status = store.StatusApproval
			it.Reason = reason
		}
	}
	if it.Status == "" {
		it.Status = store.StatusQueued
		it.SendAt = time.Now().Add(time.Duration(a.Config.UndoSeconds) * time.Second).UnixMilli()
	}
	id, err := a.Store.AddOutbox(it)
	if err != nil {
		return it, err
	}
	if origin == OriginAgent {
		a.LogAgent("queue #%d %s %s status=%s %s", id, kind, describe(kind, out), it.Status, it.Reason)
	}
	return a.Store.OutboxItem(id)
}

func validate(kind string, out store.Outgoing) error {
	switch kind {
	case "mail":
		if out.DraftID == "" && len(out.To)+len(out.Cc)+len(out.Bcc) == 0 {
			return errors.New("no recipients")
		}
		for _, list := range [][]string{out.To, out.Cc, out.Bcc} {
			for _, addr := range list {
				if _, a := mime.ParseAddress(addr); !strings.Contains(a, "@") {
					return fmt.Errorf("%q is not an email address", addr)
				}
			}
		}
		if strings.TrimSpace(out.Body) == "" && out.DraftID == "" {
			return errors.New("the message is empty")
		}
	case "chat":
		if out.Space == "" {
			return errors.New("no space")
		}
		if strings.TrimSpace(out.Body) == "" {
			return errors.New("the message is empty")
		}
	default:
		return fmt.Errorf("unknown kind %q", kind)
	}
	return nil
}

func describe(kind string, out store.Outgoing) string {
	if kind == "chat" {
		return "to " + firstNonEmpty(out.SpaceName, out.Space)
	}
	return fmt.Sprintf("to %s subject %q", strings.Join(append(append([]string{}, out.To...), out.Cc...), ","), out.Subject)
}

func firstNonEmpty(s ...string) string {
	for _, x := range s {
		if x != "" {
			return x
		}
	}
	return ""
}

// needsApproval returns why an agent's send must wait for you ("" = it
// may go out). Recipients you have never written to always ask.
func (a *App) needsApproval(account, kind string, out store.Outgoing) string {
	policy := a.Config.SendPolicy
	if policy == config.PolicyConfirm {
		return "send_policy is confirm"
	}
	if kind == "chat" {
		return "chat messages from agents always ask"
	}
	for _, list := range [][]string{out.To, out.Cc, out.Bcc} {
		for _, addr := range list {
			_, ad := mime.ParseAddress(addr)
			if !a.Store.KnownContact(account, ad) {
				return "you have never written to " + ad
			}
		}
	}
	if policy == config.PolicyKnownContacts && out.ThreadID == "" {
		return "known-contacts only lets replies through"
	}
	return ""
}

// Approve moves an item from approval to the undo window, with the
// (possibly edited) message.
func (a *App) Approve(id int64, edited *store.Outgoing) error {
	it, err := a.Store.OutboxItem(id)
	if err != nil {
		return err
	}
	if it.Status != store.StatusApproval {
		return fmt.Errorf("#%d is %s, not waiting for approval", id, it.Status)
	}
	if edited != nil {
		if err := validate(it.Kind, *edited); err != nil {
			return err
		}
		if err := a.Store.UpdateOutgoing(id, *edited); err != nil {
			return err
		}
	}
	ok, err := a.Store.Transition(id, store.StatusApproval, store.StatusQueued, time.Now().Add(time.Duration(a.Config.UndoSeconds)*time.Second).UnixMilli())
	if err == nil && !ok {
		err = fmt.Errorf("#%d changed meanwhile", id)
	}
	if err == nil {
		a.LogAgent("approved #%d", id)
	}
	return err
}

func (a *App) Decline(id int64) error {
	ok, err := a.Store.Transition(id, store.StatusApproval, store.StatusDeclined, 0)
	if err == nil && !ok {
		return fmt.Errorf("#%d is not waiting for approval", id)
	}
	a.LogAgent("declined #%d", id)
	return err
}

// Cancel stops a queued send during its undo window.
func (a *App) Cancel(id int64) error {
	ok, err := a.Store.Transition(id, store.StatusQueued, store.StatusCancelled, 0)
	if err != nil {
		return err
	}
	if !ok {
		it, err := a.Store.OutboxItem(id)
		if err != nil {
			return err
		}
		if it.Status == store.StatusApproval {
			return a.Decline(id)
		}
		return fmt.Errorf("too late: #%d is %s", id, it.Status)
	}
	return nil
}

// DeliverDue sends every queued item whose undo window is over. Any
// long-running process (daemon, screen, MCP server) calls it; claiming
// an item is atomic, so each is sent once.
func (a *App) DeliverDue(ctx context.Context) []store.OutboxItem {
	_ = a.Store.RecoverSending()
	due, err := a.Store.DueOutbox()
	if err != nil {
		return nil
	}
	var done []store.OutboxItem
	for _, it := range due {
		ok, err := a.Store.Transition(it.ID, store.StatusQueued, store.StatusSending, 0)
		if err != nil || !ok {
			continue
		}
		result, err := a.deliver(ctx, it)
		if err != nil {
			_ = a.Store.FinishOutbox(it.ID, store.StatusFailed, err.Error(), "")
			it.Status, it.Error = store.StatusFailed, err.Error()
		} else {
			_ = a.Store.FinishOutbox(it.ID, store.StatusSent, "", result)
			it.Status, it.Result = store.StatusSent, result
		}
		if it.Origin == OriginAgent {
			a.LogAgent("deliver #%d %s %s", it.ID, it.Status, it.Error)
		}
		done = append(done, it)
	}
	return done
}

func (a *App) deliver(ctx context.Context, it store.OutboxItem) (string, error) {
	c, err := a.Client(it.Account)
	if err != nil {
		return "", err
	}
	out := it.Out
	if it.Kind == "chat" {
		return c.PostMessage(ctx, out.Space, out.Body, out.Thread)
	}
	acc, err := a.Store.Account(it.Account)
	if err != nil {
		return "", err
	}
	raw, err := mime.Build(mime.Draft{From: acc.Email, To: out.To, Cc: out.Cc, Bcc: out.Bcc, Subject: out.Subject, Body: out.Body,
		InReplyTo: out.InReplyTo, References: out.References})
	if err != nil {
		return "", err
	}
	id, err := c.Send(ctx, raw, out.ThreadID)
	if err != nil {
		return "", err
	}
	if out.DraftID != "" {
		_ = c.DeleteDraft(ctx, out.DraftID)
	}
	for _, addr := range append(append(append([]string{}, out.To...), out.Cc...), out.Bcc...) {
		_, ad := mime.ParseAddress(addr)
		_ = a.Store.NoteContact(it.Account, ad, "", true, time.Now().UnixMilli())
	}
	// pull the sent message into the local copy
	if gt, err := c.GetThread(ctx, firstNonEmpty(out.ThreadID, id)); err == nil {
		t, msgs := convertThread(acc, gt)
		_ = a.Store.PutThread(it.Account, t, msgs)
	}
	return id, nil
}

// SaveDraft saves a Gmail draft (it shows up in Gmail on every device).
func (a *App) SaveDraft(ctx context.Context, account string, out store.Outgoing) (string, error) {
	acc, err := a.Store.Account(account)
	if err != nil {
		return "", err
	}
	c, err := a.Client(account)
	if err != nil {
		return "", err
	}
	raw, err := mime.Build(mime.Draft{From: acc.Email, To: out.To, Cc: out.Cc, Bcc: out.Bcc, Subject: out.Subject, Body: out.Body,
		InReplyTo: out.InReplyTo, References: out.References})
	if err != nil {
		return "", err
	}
	if out.DraftID != "" {
		return out.DraftID, c.UpdateDraft(ctx, out.DraftID, raw, out.ThreadID)
	}
	return c.CreateDraft(ctx, raw, out.ThreadID)
}

// WaitFor waits until an outbox item leaves the given statuses, or ctx ends.
func (a *App) WaitFor(ctx context.Context, id int64, deliver bool, statuses ...string) (store.OutboxItem, error) {
	t := time.NewTicker(500 * time.Millisecond)
	defer t.Stop()
	for {
		if deliver {
			a.DeliverDue(ctx)
		}
		it, err := a.Store.OutboxItem(id)
		if err != nil {
			return it, err
		}
		still := false
		for _, s := range statuses {
			if it.Status == s {
				still = true
			}
		}
		if !still {
			return it, nil
		}
		select {
		case <-ctx.Done():
			return it, ctx.Err()
		case <-t.C:
		}
	}
}

// AskApproval shows the approval prompt: a tmux popup when there is a
// tmux server to show it on, and a desktop notification either way.
func (a *App) AskApproval(it store.OutboxItem) {
	what := describe(it.Kind, it.Out)
	Notify("sandesh: approve a send?", "An agent wants to send "+it.Kind+" "+what+". Open sandesh (or run sandesh approve) to answer.")
	if os.Getenv("TMUX") == "" {
		return
	}
	args := []string{"display-popup", "-E", "-w", "84%", "-h", "80%", "-T", " sandesh: approve send ",
		fmt.Sprintf("%s approve %d", shellQuote(Executable()), it.ID)}
	if pane := os.Getenv("TMUX_PANE"); pane != "" {
		args = append(args[:1], append([]string{"-t", pane}, args[1:]...)...)
	}
	cmd := exec.Command("tmux", args...)
	_ = cmd.Start()
	go func() { _ = cmd.Wait() }()
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// Notify sends a desktop notification when notify-send exists.
func Notify(title, body string) {
	if _, err := exec.LookPath("notify-send"); err != nil {
		return
	}
	_ = exec.Command("notify-send", "-a", "sandesh", "-i", "mail-unread", title, body).Start()
}
