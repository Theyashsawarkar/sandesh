// Package cli is the `sandesh` command line: every subcommand, its
// --help, and the plain and --json output.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"

	"github.com/Theyashsawarkar/sandesh/internal/app"
	"github.com/Theyashsawarkar/sandesh/internal/config"
	"github.com/Theyashsawarkar/sandesh/internal/mime"
	"github.com/Theyashsawarkar/sandesh/internal/store"
)

const usage = `sandesh -- Gmail and Google Chat in the terminal

Usage:
  sandesh                          open the screen (--tab chat: on chat)
  sandesh mail list [flags]        threads (--today --unread --section NAME --json)
  sandesh mail read <thread>       a whole thread as text (--json, --mark-read)
  sandesh mail search <query>      Gmail search (--local: the local copy only)
  sandesh mail send [flags]        send mail (--to --cc --subject --reply THREAD; body on stdin or $EDITOR)
  sandesh mail archive|unarchive|trash|untrash|mark-read|unread|star|unstar <thread>...
  sandesh chat list                spaces and DMs with unread counts (--json)
  sandesh chat read <space>        a space's recent messages (--json, --mark-read)
  sandesh chat send <space> <text> post in a space or DM (--thread T)
  sandesh status                   unread counts (--json, --tmux)
  sandesh sync                     sync every account now
  sandesh outbox                   sends of the last week (cancel <id> stops one)
  sandesh approve [id]             answer an agent's send that waits for you
  sandesh auth setup <client.json> install the OAuth client from the Cloud console
  sandesh auth add <name>          sign in a Google account (--no-chat for a personal Gmail)
  sandesh auth list|remove <name>  the accounts
  sandesh mcp                      the MCP server for Claude Code and other agents
  sandesh daemon                   the background sync (sandesh service install runs it)
  sandesh service install|remove|status
  sandesh config                   print an example config file
  sandesh demo                     try the screen on example mail, no Google needed
  sandesh doctor                   check sign-in, sync, the service and the tmux plugin

Every command has --help. Account: --account NAME (default: the first).
`

// Run dispatches a command line; it returns the exit code.
func Run(args []string, tui func(*app.App, string) error) int {
	// the screen; its setup screen may ask for the demo instead
	screen := func(launch string) int {
		code := withApp(func(a *app.App) error {
			if launch == "" && !term.IsTerminal(int(os.Stdout.Fd())) {
				return status(a, []string{})
			}
			err := tui(a, launch)
			if errors.Is(err, app.ErrDemo) {
				return errWantDemo
			}
			return err
		})
		if code == demoCode {
			return check(demo(nil, tui))
		}
		return code
	}
	// --tab mail|chat, anywhere before the command: the tab the screen opens on
	for i := 0; i+1 < len(args) && strings.HasPrefix(args[i], "-"); i++ {
		if args[i] == "--tab" {
			if t := args[i+1]; t != "mail" && t != "chat" {
				return check(usageError("--tab is mail or chat, not %q", t))
			}
			os.Setenv("SANDESH_TAB", args[i+1])
			args = append(args[:i:i], args[i+2:]...)
			break
		}
	}
	if len(args) == 0 {
		return screen("")
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "-h", "--help", "help":
		fmt.Print(usage)
		return 0
	case "-V", "--version", "version":
		fmt.Println("sandesh", app.Version)
		return 0
	case "--popup":
		// the tmux plugin: same screen, opened in a popup
		return screen("popup")
	case "config":
		fmt.Print(config.Example)
		return 0
	case "demo":
		return check(demo(rest, tui))
	case "auth":
		return withApp(func(a *app.App) error { return auth(a, rest) })
	}
	return withApp(func(a *app.App) error {
		switch cmd {
		case "mail", "m":
			return mail(a, rest)
		case "chat", "c":
			return chat(a, rest)
		case "status", "st":
			return status(a, rest)
		case "sync":
			return syncCmd(a, rest)
		case "outbox":
			return outbox(a, rest)
		case "approve":
			return approve(a, rest)
		case "mcp":
			return mcpCmd(a, rest)
		case "daemon":
			return daemon(a, rest)
		case "service":
			return service(a, rest)
		case "doctor", "check":
			return doctor(a, rest)
		}
		return usageError("unknown command %q (sandesh --help)", cmd)
	})
}

type exitError struct {
	code int
	msg  string
}

func (e exitError) Error() string { return e.msg }

// errWantDemo leaves withApp (closing the real database) before the demo
// opens its own.
const demoCode = 99

var errWantDemo = exitError{demoCode, ""}

func usageError(f string, a ...any) error { return exitError{2, fmt.Sprintf(f, a...)} }

func check(err error) int {
	if err == nil {
		return 0
	}
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	var e exitError
	if errors.As(err, &e) {
		if e.msg != "" {
			fmt.Fprintln(os.Stderr, "sandesh:", e.msg)
		}
		return e.code
	}
	fmt.Fprintln(os.Stderr, "sandesh:", err)
	return 1
}

func withApp(f func(*app.App) error) int {
	a, err := app.Open()
	if err != nil {
		return check(err)
	}
	defer a.Close()
	return check(f(a))
}

func flags(name, help string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "%s\n\nFlags:\n", strings.TrimSpace(help))
		fs.PrintDefaults()
	}
	return fs
}

// parse allows flags after positional arguments too.
func parse(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		args = fs.Args()
		if len(args) == 0 {
			return pos, nil
		}
		pos = append(pos, args[0])
		args = args[1:]
	}
}

func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func ctxSignal() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

// When formats a time the way the screen does.
func When(ms int64) string {
	t := time.UnixMilli(ms)
	now := time.Now()
	y1, m1, d1 := t.Date()
	y2, m2, d2 := now.Date()
	switch {
	case y1 == y2 && m1 == m2 && d1 == d2:
		return t.Format("15:04")
	case now.Sub(t) < 6*24*time.Hour:
		return t.Format("Mon")
	case y1 == y2:
		return t.Format("02 Jan")
	}
	return t.Format("02 Jan 2006")
}

// ------------------------------------------------------------------- mail

func mail(a *app.App, args []string) error {
	if len(args) == 0 {
		return usageError("mail what? list, read, search, send, save, archive, trash, read, unread, star (sandesh --help)")
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "list", "ls":
		return mailList(a, rest)
	case "read", "show", "cat":
		return mailRead(a, rest)
	case "search", "s":
		return mailSearch(a, rest)
	case "send":
		return mailSend(a, rest)
	case "save", "download":
		return mailSave(a, rest)
	case "archive", "unarchive", "trash", "untrash", "mark-read", "unread", "star", "unstar":
		return mailModify(a, sub, rest)
	}
	return usageError("unknown mail command %q", sub)
}

type threadOut struct {
	store.Thread
	When   string `json:"when"`
	Unread bool   `json:"unread"`
}

func printThreads(ts []store.Thread, asJSON bool) error {
	if asJSON {
		out := []threadOut{}
		for _, t := range ts {
			out = append(out, threadOut{t, When(t.LastDate), t.Unread()})
		}
		return printJSON(out)
	}
	if len(ts) == 0 {
		fmt.Println("no threads")
		return nil
	}
	for _, t := range ts {
		mark := " "
		if t.Unread() {
			mark = "●"
		}
		if t.Starred() {
			mark += "★"
		} else {
			mark += " "
		}
		fmt.Printf("%s %-8s %-22.22s %s  [%s]\n", mark, When(t.LastDate), t.Participants, t.Subject, t.ID)
	}
	return nil
}

func accountsFlag(a *app.App, name string) ([]string, error) {
	if name == "" {
		return nil, nil
	}
	acc, err := a.ResolveAccount(name)
	if err != nil {
		return nil, err
	}
	return []string{acc.Name}, nil
}

func mailList(a *app.App, args []string) error {
	fs := flags("sandesh mail list", "sandesh mail list [--today] [--unread] [--section NAME] [--account A] [--limit N] [--json]\n\nThreads from the local copy, newest first. Default: the first section (Inbox).")
	today := fs.Bool("today", false, "only threads with mail from today")
	unread := fs.Bool("unread", false, "only unread threads")
	section := fs.String("section", "", "a section by name (see the tabs in the screen)")
	account := fs.String("account", "", "one account")
	limit := fs.Int("limit", 50, "at most this many")
	asJSON := fs.Bool("json", false, "JSON output")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	accs, err := accountsFlag(a, *account)
	if err != nil {
		return err
	}
	ts, err := listThreads(a, accs, *section, *today, *unread, *limit)
	if err != nil {
		return err
	}
	return printThreads(ts, *asJSON)
}

// listThreads is shared with the MCP server.
func listThreads(a *app.App, accounts []string, section string, today, unread bool, limit int) ([]store.Thread, error) {
	secs := a.Sections()
	s := secs[0]
	if section != "" {
		found := false
		for _, x := range secs {
			if strings.EqualFold(x.Name, section) {
				s, found = x, true
			}
		}
		if !found {
			var names []string
			for _, x := range secs {
				names = append(names, x.Name)
			}
			return nil, fmt.Errorf("no section %q (have: %s)", section, strings.Join(names, ", "))
		}
	}
	if today {
		s.Today = true
	}
	ts, err := a.SectionThreads(s, accounts)
	if err != nil {
		return nil, err
	}
	var out []store.Thread
	for _, t := range ts {
		if unread && !t.Unread() {
			continue
		}
		out = append(out, t)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}

func mailRead(a *app.App, args []string) error {
	fs := flags("sandesh mail read", "sandesh mail read <thread> [--json] [--mark-read]\n\nA whole thread as text, oldest message first.")
	asJSON := fs.Bool("json", false, "JSON output")
	mark := fs.Bool("mark-read", false, "also mark it read")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageError("mail read needs one thread id (sandesh mail list shows them)")
	}
	t, err := a.Store.FindThread(pos[0])
	if err != nil {
		return err
	}
	msgs, err := a.Store.Messages(t.Account, t.ID)
	if err != nil {
		return err
	}
	if *mark && t.Unread() {
		ctx, cancel := ctxSignal()
		defer cancel()
		if err := a.MarkRead(ctx, t.Account, t.ID, true); err != nil {
			return err
		}
	}
	if *asJSON {
		return printJSON(map[string]any{"thread": t, "messages": msgs})
	}
	fmt.Print(FormatThread(t, msgs))
	return nil
}

// FormatThread renders a thread as plain text.
func FormatThread(t store.Thread, msgs []store.Message) string {
	var b strings.Builder
	n := 0 // attachments are numbered across the thread, for mail save
	fmt.Fprintf(&b, "%s\n%s\n", t.Subject, strings.Repeat("─", min(60, max(10, len([]rune(t.Subject))))))
	for i, m := range msgs {
		if i > 0 {
			b.WriteString("\n" + strings.Repeat("·", 40) + "\n")
		}
		fmt.Fprintf(&b, "From: %s\n", fromLine(m))
		if m.To != "" {
			fmt.Fprintf(&b, "To:   %s\n", m.To)
		}
		if m.Cc != "" {
			fmt.Fprintf(&b, "Cc:   %s\n", m.Cc)
		}
		fmt.Fprintf(&b, "Date: %s\n\n%s\n", time.UnixMilli(m.Date).Format("Mon 02 Jan 2006 15:04"), m.Body)
		for _, at := range m.Attachments {
			n++
			fmt.Fprintf(&b, "📎 %d. %s (%s)\n", n, at.Name, humanSize(at.Size))
		}
	}
	return b.String()
}

func fromLine(m store.Message) string {
	if m.FromName != "" {
		return m.FromName + " <" + m.FromAddr + ">"
	}
	return m.FromAddr
}

func humanSize(n int) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%d KB", n>>10)
	}
	return fmt.Sprintf("%d B", n)
}

func mailSearch(a *app.App, args []string) error {
	fs := flags("sandesh mail search", "sandesh mail search <query> [--local] [--account A] [--limit N] [--json]\n\nGmail's search syntax (from:priya newer_than:7d has:attachment), run on the\nserver; --local searches words in the local copy instead (works offline).")
	local := fs.Bool("local", false, "search the local copy only")
	account := fs.String("account", "", "the account")
	limit := fs.Int("limit", 30, "at most this many")
	asJSON := fs.Bool("json", false, "JSON output")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	q := strings.Join(pos, " ")
	if q == "" {
		return usageError("search for what?")
	}
	ts, err := search(a, *account, q, *local, *limit)
	if err != nil {
		return err
	}
	return printThreads(ts, *asJSON)
}

func search(a *app.App, account, q string, local bool, limit int) ([]store.Thread, error) {
	acc, err := a.ResolveAccount(account)
	if err != nil {
		return nil, err
	}
	if !local {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		ts, err := a.SearchServer(ctx, acc.Name, q, limit)
		if err == nil {
			return ts, nil
		}
		fmt.Fprintf(os.Stderr, "sandesh: server search failed (%v); searching the local copy\n", err)
	}
	return a.Store.Threads(store.ThreadQuery{Accounts: []string{acc.Name}, Text: q, Limit: limit})
}

func mailModify(a *app.App, action string, args []string) error {
	if len(args) == 0 {
		return usageError("mail %s needs thread ids", action)
	}
	ctx, cancel := ctxSignal()
	defer cancel()
	for _, id := range args {
		t, err := a.Store.FindThread(id)
		if err != nil {
			return err
		}
		switch action {
		case "archive":
			err = a.Archive(ctx, t.Account, id)
		case "unarchive":
			err = a.Unarchive(ctx, t.Account, id)
		case "trash":
			err = a.Trash(ctx, t.Account, id)
		case "untrash":
			err = a.Untrash(ctx, t.Account, id)
		case "mark-read":
			err = a.MarkRead(ctx, t.Account, id, true)
		case "unread":
			err = a.MarkRead(ctx, t.Account, id, false)
		case "star":
			err = a.Star(ctx, t.Account, id, true)
		case "unstar":
			err = a.Star(ctx, t.Account, id, false)
		}
		if err != nil {
			return fmt.Errorf("%s %s: %w", action, id, err)
		}
		fmt.Printf("%s: %s\n", map[string]string{"archive": "archived", "unarchive": "back in the inbox", "trash": "moved to trash",
			"untrash": "restored", "mark-read": "marked read", "unread": "marked unread", "star": "starred", "unstar": "unstarred"}[action], id)
	}
	return nil
}

func splitAddrs(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func mailSend(a *app.App, args []string) error {
	fs := flags("sandesh mail send", `sandesh mail send --to A[,B] [--cc C] [--subject S] [--attach FILE]... [--at WHEN] [--reply THREAD] [--all] [--account A] [--now]

The body comes from stdin, or $EDITOR when stdin is a terminal. --reply
answers a thread (subject, recipients and quoting filled in; --all is reply
all). The send waits undo_seconds (default 10) first: Ctrl+c cancels.`)
	to := fs.String("to", "", "recipients, comma separated")
	cc := fs.String("cc", "", "cc, comma separated")
	bcc := fs.String("bcc", "", "bcc, comma separated")
	subject := fs.String("subject", "", "the subject")
	reply := fs.String("reply", "", "a thread id to reply to")
	all := fs.Bool("all", false, "with --reply: reply to everyone")
	account := fs.String("account", "", "send as this account")
	nowFlag := fs.Bool("now", false, "skip the undo window")
	var attach listFlag
	fs.Var(&attach, "attach", "a file to attach (repeat for more)")
	at := fs.String("at", "", "send later: \"tomorrow 9am\", \"fri 6pm\", \"in 2h\", \"3 nov 10:30\"")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	acc, err := a.ResolveAccount(*account)
	if err != nil {
		return err
	}
	out := store.Outgoing{To: splitAddrs(*to), Cc: splitAddrs(*cc), Bcc: splitAddrs(*bcc), Subject: *subject, Files: attach.files()}
	quoted := ""
	if *reply != "" {
		t, err := a.Store.FindThread(*reply)
		if err != nil {
			return err
		}
		acc, _ = a.Store.Account(t.Account)
		msgs, err := a.Store.Messages(t.Account, t.ID)
		if err != nil || len(msgs) == 0 {
			return fmt.Errorf("thread %s has no messages in the local copy", t.ID)
		}
		r := ReplyTo(acc, t, msgs, *all)
		if len(out.To) == 0 {
			out.To = r.To
		}
		if len(out.Cc) == 0 {
			out.Cc = r.Cc
		}
		if out.Subject == "" {
			out.Subject = r.Subject
		}
		out.ThreadID, out.InReplyTo, out.References = r.ThreadID, r.InReplyTo, r.References
		quoted = r.Body
	}
	var body string
	if term.IsTerminal(int(os.Stdin.Fd())) {
		edited, err := EditMail(a, out, quoted)
		if err != nil {
			return err
		}
		if edited == nil {
			fmt.Fprintln(os.Stderr, "sandesh: empty message, not sent")
			return nil
		}
		out = *edited
	} else {
		b, err := io.ReadAll(os.Stdin)
		if err != nil {
			return err
		}
		body = string(b)
		out.Body = body
		if quoted != "" {
			out.Body = strings.TrimRight(body, "\n") + "\n\n" + quoted
		}
	}
	if *nowFlag {
		a.Config.UndoSeconds = 0
	}
	if err := scheduleFlag(&out, *at); err != nil {
		return err
	}
	it, err := a.Queue(acc.Name, "mail", out, app.OriginYou)
	if err != nil {
		return err
	}
	return waitSend(a, it)
}

// waitSend waits out the undo window (Ctrl+c cancels), then delivers. A
// scheduled send is left to the background service.
func waitSend(a *app.App, it store.OutboxItem) error {
	if it.Out.ScheduledFor > 0 && it.Status == store.StatusQueued {
		t := time.UnixMilli(it.Out.ScheduledFor)
		fmt.Printf("sandesh: scheduled for %s (#%d) · sandesh outbox cancel %d stops it\n", t.Format("Mon 02 Jan 15:04"), it.ID, it.ID)
		if !a.DaemonRunning() {
			fmt.Fprintln(os.Stderr, "sandesh: the background service isn't running: it sends scheduled mail. Start it with `sandesh service install`.")
		}
		return nil
	}
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sig)
	for {
		left := time.Until(time.UnixMilli(it.SendAt))
		if left <= 0 {
			break
		}
		fmt.Fprintf(os.Stderr, "\rsandesh: sending in %ds, Ctrl+c cancels ", int(left.Seconds()+0.99))
		select {
		case <-sig:
			fmt.Fprintln(os.Stderr)
			if err := a.Cancel(it.ID); err != nil {
				return err
			}
			fmt.Fprintln(os.Stderr, "sandesh: cancelled, nothing was sent")
			return nil
		case <-time.After(min(left, time.Second)):
		}
	}
	fmt.Fprint(os.Stderr, "\r\033[K")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	a.DeliverDue(ctx)
	got, err := a.Store.OutboxItem(it.ID)
	if err != nil {
		return err
	}
	switch got.Status {
	case store.StatusSent:
		fmt.Fprintln(os.Stderr, "sandesh: sent")
		return nil
	case store.StatusFailed:
		return fmt.Errorf("not sent: %s", got.Error)
	}
	// another process (the daemon) may have claimed it
	got, err = a.WaitFor(ctx, it.ID, false, store.StatusQueued, store.StatusSending)
	if err != nil {
		return err
	}
	if got.Status != store.StatusSent {
		return fmt.Errorf("not sent: %s %s", got.Status, got.Error)
	}
	fmt.Fprintln(os.Stderr, "sandesh: sent")
	return nil
}

// ReplyTo prepares a reply (or reply all) to a thread's last message.
func ReplyTo(acc store.Account, t store.Thread, msgs []store.Message, all bool) store.Outgoing {
	last := msgs[len(msgs)-1]
	// reply to the last message that wasn't yours, if any
	for i := len(msgs) - 1; i >= 0; i-- {
		if !strings.EqualFold(msgs[i].FromAddr, acc.Email) {
			last = msgs[i]
			break
		}
	}
	out := store.Outgoing{Subject: mime.ReplySubject(t.Subject), ThreadID: t.ID, InReplyTo: last.MessageID,
		References: strings.TrimSpace(last.References + " " + last.MessageID)}
	if strings.EqualFold(last.FromAddr, acc.Email) {
		out.To = mime.ParseList(last.To)
	} else if last.ReplyTo != "" {
		out.To = mime.ParseList(last.ReplyTo)
	} else {
		out.To = []string{last.FromAddr}
	}
	if all {
		seen := map[string]bool{strings.ToLower(acc.Email): true}
		for _, x := range out.To {
			seen[strings.ToLower(x)] = true
		}
		for _, x := range append(mime.ParseList(last.To), mime.ParseList(last.Cc)...) {
			if !seen[strings.ToLower(x)] {
				seen[strings.ToLower(x)] = true
				out.Cc = append(out.Cc, x)
			}
		}
	}
	out.Body = fmt.Sprintf("On %s, %s wrote:\n%s", time.UnixMilli(last.Date).Format("Mon 02 Jan 2006 15:04"), fromLine(last), mime.Quote(last.Body))
	return out
}

// Forward prepares a forward of a thread's last message.
func Forward(t store.Thread, msgs []store.Message) store.Outgoing {
	last := msgs[len(msgs)-1]
	body := fmt.Sprintf("---------- Forwarded message ---------\nFrom: %s\nDate: %s\nSubject: %s\nTo: %s\n\n%s",
		fromLine(last), time.UnixMilli(last.Date).Format("Mon 02 Jan 2006 15:04"), last.Subject, last.To, last.Body)
	out := store.Outgoing{Subject: mime.ForwardSubject(t.Subject), Body: body}
	// the original's attachments go along (delete them from Attach: to drop)
	for i, at := range last.Attachments {
		out.Files = append(out.Files, store.File{Name: at.Name, Size: int64(at.Size), Type: at.Type,
			Source: "original: " + at.Name, Ref: fmt.Sprintf("%s/%s/%d", last.Account, last.ID, i)})
	}
	return out
}

// ------------------------------------------------------------------- chat

func chat(a *app.App, args []string) error {
	if len(args) == 0 {
		return usageError("chat what? list, read, send, save")
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "list", "ls":
		fs := flags("sandesh chat list", "sandesh chat list [--unread] [--account A] [--json]\n\nSpaces and DMs, most recently active first.")
		unread := fs.Bool("unread", false, "only spaces with unread messages")
		account := fs.String("account", "", "one account")
		asJSON := fs.Bool("json", false, "JSON output")
		if _, err := parse(fs, rest); err != nil {
			return err
		}
		accs, err := accountsFlag(a, *account)
		if err != nil {
			return err
		}
		sps, err := a.Store.Spaces(accs, nil)
		if err != nil {
			return err
		}
		var out []store.Space
		for _, s := range sps {
			if !*unread || s.Unread > 0 {
				out = append(out, s)
			}
		}
		if *asJSON {
			if out == nil {
				out = []store.Space{}
			}
			return printJSON(out)
		}
		for _, s := range out {
			n := "  "
			if s.Unread > 0 {
				n = fmt.Sprintf("%-2d", s.Unread)
			}
			fmt.Printf("%s %-8s %-24.24s %s  [%s]\n", n, When(s.LastActive), SpaceTitle(s), oneLine(s.LastText, 60), s.Name)
		}
		return nil
	case "read", "show":
		fs := flags("sandesh chat read", "sandesh chat read <space> [--limit N] [--mark-read] [--json]\n\nA space's recent messages, oldest first. <space> is its id (spaces/...) or name.")
		limit := fs.Int("limit", 50, "at most this many")
		mark := fs.Bool("mark-read", false, "also mark the space read")
		asJSON := fs.Bool("json", false, "JSON output")
		pos, err := parse(fs, rest)
		if err != nil {
			return err
		}
		if len(pos) != 1 {
			return usageError("chat read needs a space")
		}
		sp, err := FindSpace(a, pos[0])
		if err != nil {
			return err
		}
		msgs, err := a.Store.ChatMessages(sp.Account, sp.Name, *limit)
		if err != nil {
			return err
		}
		if *mark {
			ctx, cancel := ctxSignal()
			defer cancel()
			if err := a.MarkSpaceRead(ctx, sp.Account, sp.Name); err != nil {
				return err
			}
		}
		if *asJSON {
			if msgs == nil {
				msgs = []store.ChatMessage{}
			}
			return printJSON(map[string]any{"space": sp, "messages": msgs})
		}
		fmt.Printf("%s\n\n", SpaceTitle(sp))
		for _, m := range msgs {
			who := m.SenderName
			if m.Mine {
				who = "you"
			}
			fmt.Printf("%s  %s\n%s\n", who, When(m.Created), m.Text)
			if len(m.Files) > 0 {
				fmt.Printf("  ↳ sandesh chat save %s\n", m.Name)
			}
			fmt.Println()
		}
		return nil
	case "save", "download":
		return chatSave(a, rest)
	case "send", "post":
		fs := flags("sandesh chat send", "sandesh chat send <space> <text...> [--attach FILE]... [--at WHEN] [--thread T] [--now]\n\nPost in a space or DM (text from stdin when not given). Waits undo_seconds first.")
		thread := fs.String("thread", "", "reply in this thread (spaces/x/threads/y)")
		nowFlag := fs.Bool("now", false, "skip the undo window")
		var attach listFlag
		fs.Var(&attach, "attach", "a file to upload with it (repeat for more)")
		at := fs.String("at", "", "send later: \"tomorrow 9am\", \"in 2h\"...")
		pos, err := parse(fs, rest)
		if err != nil {
			return err
		}
		if len(pos) < 1 {
			return usageError("chat send needs a space")
		}
		sp, err := FindSpace(a, pos[0])
		if err != nil {
			return err
		}
		text := strings.Join(pos[1:], " ")
		if text == "" && (len(attach) == 0 || !term.IsTerminal(int(os.Stdin.Fd()))) {
			b, _ := io.ReadAll(os.Stdin)
			text = strings.TrimSpace(string(b))
		}
		if *nowFlag {
			a.Config.ChatUndoSeconds = 0
		}
		out := store.Outgoing{Space: sp.Name, SpaceName: SpaceTitle(sp), Thread: *thread, Body: text, Files: attach.files()}
		if err := scheduleFlag(&out, *at); err != nil {
			return err
		}
		it, err := a.Queue(sp.Account, "chat", out, app.OriginYou)
		if err != nil {
			return err
		}
		return waitSend(a, it)
	}
	return usageError("unknown chat command %q", sub)
}

// listFlag collects a repeated flag.
type listFlag []string

func (l *listFlag) String() string     { return strings.Join(*l, ",") }
func (l *listFlag) Set(v string) error { *l = append(*l, v); return nil }
func (l listFlag) files() []store.File {
	var out []store.File
	for _, p := range l {
		out = append(out, store.File{Source: p})
	}
	return out
}

func mailSave(a *app.App, args []string) error {
	fs := flags("sandesh mail save", `sandesh mail save <thread-id> [N|all] [--dir DIR]

Downloads the thread's attachments (numbered as mail read shows them; all
by default) into DIR, default ~/Downloads/sandesh. Never overwrites.`)
	dir := fs.String("dir", "", "where to save")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) < 1 {
		return usageError("mail save needs a thread id")
	}
	want := 0
	if len(pos) > 1 && pos[1] != "all" {
		if want, err = strconv.Atoi(pos[1]); err != nil || want < 1 {
			return usageError("N is an attachment number from mail read, or all")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	saved, err := a.SaveThreadAttachments(ctx, pos[0], want, *dir)
	for _, p := range saved {
		fmt.Println(p)
	}
	return err
}

func chatSave(a *app.App, args []string) error {
	fs := flags("sandesh chat save", `sandesh chat save <message-id> [N|all] [--dir DIR]

Downloads a chat message's files (chat read --json shows the ids) into
DIR, default ~/Downloads/sandesh. Drive files open in the browser instead.`)
	dir := fs.String("dir", "", "where to save")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) < 1 {
		return usageError("chat save needs a message id (spaces/x/messages/y)")
	}
	want := 0
	if len(pos) > 1 && pos[1] != "all" {
		if want, err = strconv.Atoi(pos[1]); err != nil || want < 1 {
			return usageError("N is a file number, or all")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	saved, err := a.SaveChatFiles(ctx, pos[0], want, *dir)
	for _, p := range saved {
		fmt.Println(p)
	}
	return err
}

// FindSpace finds a space by id or (part of) its name.
func FindSpace(a *app.App, key string) (store.Space, error) {
	sps, err := a.Store.Spaces(nil, nil)
	if err != nil {
		return store.Space{}, err
	}
	var matches []store.Space
	for _, s := range sps {
		if s.Name == key || "spaces/"+key == s.Name {
			return s, nil
		}
		if strings.Contains(strings.ToLower(SpaceTitle(s)), strings.ToLower(key)) {
			matches = append(matches, s)
		}
	}
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return store.Space{}, fmt.Errorf("no space matches %q (sandesh chat list)", key)
	}
	var names []string
	for _, m := range matches {
		names = append(names, SpaceTitle(m))
	}
	return store.Space{}, fmt.Errorf("%q matches several spaces: %s", key, strings.Join(names, ", "))
}

func SpaceTitle(s store.Space) string {
	switch {
	case s.Alias != "":
		return s.Alias
	case s.DisplayName != "":
		return s.DisplayName
	case s.Type == "DIRECT_MESSAGE":
		return "Direct message"
	case s.Type == "GROUP_CHAT":
		return "Group chat"
	}
	return s.Name
}

func oneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

// ----------------------------------------------------------------- status

type Counts struct {
	Mail    int               `json:"mail"`
	Chat    int               `json:"chat"`
	Waiting int               `json:"waiting_approval"`
	Errors  map[string]string `json:"errors,omitempty"`
}

func GetCounts(a *app.App) Counts {
	c := Counts{}
	inbox, _ := a.Store.Threads(store.ThreadQuery{Label: "INBOX", Unread: true})
	c.Mail = len(inbox)
	sps, _ := a.Store.Spaces(nil, nil)
	for _, s := range sps {
		if s.Unread > 0 {
			c.Chat++
		}
	}
	w, _ := a.Store.Outbox(store.StatusApproval)
	c.Waiting = len(w)
	accs, _ := a.Store.Accounts()
	for _, x := range accs {
		if x.LastError != "" {
			if c.Errors == nil {
				c.Errors = map[string]string{}
			}
			c.Errors[x.Name] = x.LastError
		}
	}
	return c
}

func status(a *app.App, args []string) error {
	fs := flags("sandesh status", "sandesh status [--json | --tmux]\n\nUnread mail threads in the inbox and chat spaces with unread messages.\n--tmux prints a status-line segment (empty when there is nothing).")
	asJSON := fs.Bool("json", false, "JSON output")
	tmux := fs.Bool("tmux", false, "a tmux status-line segment")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	c := GetCounts(a)
	switch {
	case *asJSON:
		return printJSON(c)
	case *tmux:
		var parts []string
		if c.Waiting > 0 {
			parts = append(parts, fmt.Sprintf("#[fg=#{?@red,#{@red},red}]󰀦 %d#[default]", c.Waiting))
		}
		if c.Mail > 0 {
			parts = append(parts, fmt.Sprintf("󰇮 %d", c.Mail))
		}
		if c.Chat > 0 {
			parts = append(parts, fmt.Sprintf("󰭹 %d", c.Chat))
		}
		if len(c.Errors) > 0 && len(parts) == 0 {
			parts = append(parts, "󰇮 !")
		}
		fmt.Print(strings.Join(parts, "  "))
		return nil
	}
	fmt.Printf("mail: %d unread\nchat: %d spaces unread\n", c.Mail, c.Chat)
	if c.Waiting > 0 {
		fmt.Printf("waiting for your approval: %d (sandesh approve)\n", c.Waiting)
	}
	for acc, e := range c.Errors {
		fmt.Printf("%s: %s\n", acc, e)
	}
	return nil
}

func syncCmd(a *app.App, args []string) error {
	fs := flags("sandesh sync", "sandesh sync [--account A]\n\nSync mail and chat now.")
	account := fs.String("account", "", "one account")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	ctx, cancel := ctxSignal()
	defer cancel()
	names := a.AccountNames()
	if *account != "" {
		acc, err := a.ResolveAccount(*account)
		if err != nil {
			return err
		}
		names = []string{acc.Name}
	}
	if len(names) == 0 {
		return errors.New("no account yet: run `sandesh auth add work`")
	}
	var failed bool
	for _, n := range names {
		start := time.Now()
		_, err := a.SyncMail(ctx, n)
		if err == nil {
			_, err = a.SyncChat(ctx, n)
		}
		if err != nil {
			failed = true
			fmt.Fprintf(os.Stderr, "%s: %v\n", n, err)
			continue
		}
		fmt.Fprintf(os.Stderr, "%s: synced in %s\n", n, time.Since(start).Round(100*time.Millisecond))
	}
	a.DeliverDue(ctx)
	if failed {
		return exitError{1, ""}
	}
	return nil
}

// ----------------------------------------------------------------- outbox

// scheduleFlag reads --at into the message.
func scheduleFlag(out *store.Outgoing, at string) error {
	if strings.TrimSpace(at) == "" {
		return nil
	}
	t, err := app.ParseWhen(at, time.Now())
	if err != nil {
		return usageError("--at: %v", err)
	}
	out.ScheduledFor = t.UnixMilli()
	return nil
}

func outbox(a *app.App, args []string) error {
	if len(args) > 0 && args[0] == "reschedule" {
		if len(args) < 3 {
			return usageError("outbox reschedule <id> <when>")
		}
		id, err := strconv.ParseInt(strings.TrimPrefix(args[1], "#"), 10, 64)
		if err != nil {
			return usageError("bad id %q", args[1])
		}
		t, err := app.ParseWhen(strings.Join(args[2:], " "), time.Now())
		if err != nil {
			return err
		}
		if err := a.Reschedule(id, t); err != nil {
			return err
		}
		fmt.Printf("#%d goes out %s\n", id, t.Format("Mon 02 Jan 15:04"))
		return nil
	}
	if len(args) > 0 && args[0] == "cancel" {
		if len(args) != 2 {
			return usageError("outbox cancel <id>")
		}
		id, err := strconv.ParseInt(strings.TrimPrefix(args[1], "#"), 10, 64)
		if err != nil {
			return usageError("bad id %q", args[1])
		}
		if err := a.Cancel(id); err != nil {
			return err
		}
		fmt.Println("cancelled")
		return nil
	}
	fs := flags("sandesh outbox", "sandesh outbox [--json]\nsandesh outbox cancel <id>\nsandesh outbox reschedule <id> <when>\n\nSends of the last week and scheduled ones: waiting for approval, scheduled, in the undo window, sent, failed.")
	asJSON := fs.Bool("json", false, "JSON output")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	items, err := a.Store.Outbox()
	if err != nil {
		return err
	}
	if *asJSON {
		if items == nil {
			items = []store.OutboxItem{}
		}
		return printJSON(items)
	}
	if len(items) == 0 {
		fmt.Println("nothing sent this week")
	}
	for _, it := range items {
		what := it.Out.Subject
		to := strings.Join(it.Out.To, ", ")
		if it.Kind == "chat" {
			what, to = oneLine(it.Out.Body, 40), it.Out.SpaceName
		}
		for _, f := range it.Out.Files {
			what = strings.TrimSpace(what + " 📎 " + f.Name)
		}
		status := it.Status
		if it.Status == store.StatusQueued && it.Out.ScheduledFor > 0 {
			status = "scheduled"
			what += "  ⏰ " + app.SendTime(time.UnixMilli(it.Out.ScheduledFor), time.Now())
		}
		fmt.Printf("#%-4d %-9s %-5s %-6s %-28.28s %s\n", it.ID, status, it.Kind, it.Origin, to, what)
		if it.Error != "" {
			fmt.Printf("      %s\n", it.Error)
		}
	}
	return nil
}
