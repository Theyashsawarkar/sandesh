package cli

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/Theyashsawarkar/sandesh/internal/app"
	"github.com/Theyashsawarkar/sandesh/internal/fake"
	"github.com/Theyashsawarkar/sandesh/internal/google"
	"github.com/Theyashsawarkar/sandesh/internal/mcpserver"
	"github.com/Theyashsawarkar/sandesh/internal/store"
)

// ------------------------------------------------------------------- auth

func auth(a *app.App, args []string) error {
	if len(args) == 0 {
		return usageError("auth what? setup <client.json>, add <name>, list, remove <name>")
	}
	switch args[0] {
	case "setup":
		if len(args) != 2 {
			return usageError("auth setup <client.json>: the Desktop app OAuth client from the Cloud console")
		}
		b, err := os.ReadFile(args[1])
		if err != nil {
			return err
		}
		if err := a.InstallClient(b); err != nil {
			return fmt.Errorf("%s: %w", args[1], err)
		}
		fmt.Printf("OAuth client saved to %s.\nNext: sandesh auth add work\n", a.Paths.ClientFile())
		return nil
	case "add", "login":
		fs := flags("sandesh auth add", "sandesh auth add <name> [--email you@company.com] [--no-chat]\n\nSigns in a Google account in your browser. --no-chat for a personal Gmail\n(Google Chat's API needs a Workspace account).")
		email := fs.String("email", "", "prefill the account on Google's page")
		noChat := fs.Bool("no-chat", false, "mail only")
		pos, err := parse(fs, args[1:])
		if err != nil {
			return err
		}
		if len(pos) != 1 {
			return usageError("auth add needs a name, like work or personal")
		}
		name := pos[0]
		chat := !*noChat
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		err = a.SignIn(ctx, name, *email, chat, func(url string) {
			fmt.Printf("Opening Google's sign-in page. If it doesn't open, visit:\n\n  %s\n\nWaiting...\n", url)
		})
		if err != nil {
			return err
		}
		fmt.Printf("Signed in as %q. First sync (last %d days)...\n", name, a.Config.InitialDays)
		sctx, scancel := ctxSignal()
		defer scancel()
		if _, err := a.SyncMail(sctx, name); err != nil {
			return fmt.Errorf("signed in, but the first sync failed: %w", err)
		}
		if chat {
			if _, err := a.SyncChat(sctx, name); err != nil {
				fmt.Fprintf(os.Stderr, "sandesh: mail works; chat failed: %v\n  (a personal Gmail has no Chat API: sandesh auth add %s --no-chat)\n", err, name)
			}
		}
		acc, _ := a.Store.Account(name)
		fmt.Printf("Done: %s. Open it with `sandesh`.\n", acc.Email)
		return nil
	case "list", "ls":
		accs, err := a.Store.Accounts()
		if err != nil {
			return err
		}
		if len(accs) == 0 {
			fmt.Println("no accounts: sandesh auth add work")
		}
		for _, x := range accs {
			chat := "mail+chat"
			if !x.Chat {
				chat = "mail"
			}
			synced := "never synced"
			if x.MailSynced > 0 {
				synced = "synced " + When(x.MailSynced)
			}
			fmt.Printf("%-10s %-32s %-9s %s\n", x.Name, x.Email, chat, synced)
		}
		return nil
	case "remove", "rm", "logout":
		if len(args) != 2 {
			return usageError("auth remove <name>")
		}
		if _, err := a.Store.Account(args[1]); err != nil {
			return err
		}
		a.Tokens.Delete(args[1])
		if err := a.Store.RemoveAccount(args[1]); err != nil {
			return err
		}
		fmt.Printf("Removed %s and its local copy. (Revoke access at https://myaccount.google.com/permissions too.)\n", args[1])
		return nil
	}
	return usageError("unknown auth command %q", args[0])
}

// ----------------------------------------------------------------- daemon

func daemon(a *app.App, args []string) error {
	fs := flags("sandesh daemon", "sandesh daemon\n\nThe background sync: mail every mail_interval, chat every chat_interval,\nsends the outbox, notifies about new mail and DMs. Logs to stderr.")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	ctx, cancel := ctxSignal()
	defer cancel()
	log.SetFlags(0)
	release, ok := a.HoldDaemonLock()
	if !ok {
		return errors.New("another sandesh daemon is already running")
	}
	defer release()
	log.Printf("sandesh %s daemon: mail every %s, chat every %s", app.Version, a.Config.MailInterval.Duration, a.Config.ChatInterval.Duration)
	type backoff struct {
		next  time.Time
		delay time.Duration
	}
	mailNext, chatNext := map[string]*backoff{}, map[string]*backoff{}
	step := func(m map[string]*backoff, acc string, every time.Duration, f func(context.Context, string) ([]app.News, error)) {
		b := m[acc]
		if b == nil {
			b = &backoff{}
			m[acc] = b
		}
		if time.Now().Before(b.next) {
			return
		}
		sctx, scancel := context.WithTimeout(ctx, 5*time.Minute)
		news, err := f(sctx, acc)
		scancel()
		if err != nil {
			b.delay = min(max(b.delay*2, every), 15*time.Minute)
			b.next = time.Now().Add(b.delay)
			log.Printf("%s: %v (retry in %s)", acc, err, b.delay)
		} else {
			b.delay = 0
			b.next = time.Now().Add(every)
		}
		if a.Config.Notify {
			for _, n := range news {
				app.Notify(n.Title, n.Body)
			}
		}
	}
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		for _, sent := range a.DeliverDue(ctx) {
			if sent.Status == store.StatusFailed {
				log.Printf("send #%d failed: %s", sent.ID, sent.Error)
				app.Notify("sandesh: send failed", sent.Error)
			}
		}
		for _, acc := range a.AccountNames() {
			step(mailNext, acc, a.Config.MailInterval.Duration, a.SyncMail)
			step(chatNext, acc, a.Config.ChatInterval.Duration, a.SyncChat)
		}
		select {
		case <-ctx.Done():
			log.Print("stopping")
			return nil
		case <-tick.C:
		}
	}
}

func service(a *app.App, args []string) error {
	if len(args) == 0 {
		return usageError("service install, remove or status")
	}
	sc := func(args ...string) error {
		cmd := exec.Command("systemctl", append([]string{"--user"}, args...)...)
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		return cmd.Run()
	}
	switch args[0] {
	case "install":
		if err := app.InstallService(); err != nil {
			return err
		}
		fmt.Printf("Installed %s and started it.\nLogs: journalctl --user -u sandesh -f\n", app.UnitPath())
		return nil
	case "remove", "uninstall":
		_ = sc("disable", "--now", "sandesh.service")
		if err := os.Remove(app.UnitPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		_ = sc("daemon-reload")
		fmt.Println("Removed the background sync.")
		return nil
	case "status":
		return sc("status", "--no-pager", "sandesh.service")
	}
	return usageError("unknown service command %q", args[0])
}

// -------------------------------------------------------------------- mcp

func mcpCmd(a *app.App, args []string) error {
	fs := flags("sandesh mcp", "sandesh mcp\n\nThe MCP server (stdio) for Claude Code and other agents:\n  claude mcp add --scope user sandesh -- sandesh mcp")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	ctx, cancel := ctxSignal()
	defer cancel()
	return mcpserver.Run(ctx, a, MCPHelpers())
}

// MCPHelpers hands the CLI's lookups and formatting to the MCP server.
func MCPHelpers() mcpserver.Helpers {
	return mcpserver.Helpers{
		ListThreads: listThreads, Search: search, FormatThread: FormatThread, ReplyTo: ReplyTo, FindSpace: FindSpace,
		SpaceTitle: SpaceTitle, When: When, Counts: func(a *app.App) any { return GetCounts(a) },
	}
}

// ------------------------------------------------------------------- demo

func demo(args []string, tui func(*app.App, string) error) error {
	fs := flags("sandesh demo", "sandesh demo [--server ADDR]\n\nThe screen on made-up mail and chat, served by a fake Google inside this\nprocess. Nothing touches your accounts; everything is gone when you quit.\n--server only runs the fake Google (for development): point other sandesh\ncommands at it with the SANDESH_HOME and SANDESH_GOOGLE_URL it prints.")
	serve := fs.String("server", "", "run only the fake Google on this address, e.g. 127.0.0.1:8765")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	if *serve != "" {
		g := fake.New()
		g.Seed()
		home, err := os.MkdirTemp("", "sandesh-dev-")
		if err != nil {
			return err
		}
		fmt.Printf("export SANDESH_HOME=%s SANDESH_GOOGLE_URL=http://%s\nthen: sandesh auth add demo && sandesh\n", home, *serve)
		return (&http.Server{Addr: *serve, Handler: g, ReadHeaderTimeout: 10 * time.Second}).ListenAndServe()
	}
	home, err := os.MkdirTemp("", "sandesh-demo-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(home)
	// a closed terminal or tmux kill: still leave nothing behind
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGHUP, syscall.SIGTERM)
	go func() { <-sig; os.RemoveAll(home); os.Exit(1) }()
	g := fake.New()
	g.Seed()
	srv := httptest.NewServer(g)
	defer srv.Close()
	os.Setenv("SANDESH_HOME", home)
	os.Setenv("SANDESH_GOOGLE_URL", srv.URL)
	a, err := app.Open()
	if err != nil {
		return err
	}
	defer a.Close()
	if err := a.Store.AddAccount(store.Account{Name: "demo", Chat: true}); err != nil {
		return err
	}
	if _, errs := a.SyncAll(context.Background()); len(errs) > 0 {
		for _, e := range errs {
			return e
		}
	}
	return tui(a, "demo")
}

// ----------------------------------------------------------------- doctor

func doctor(a *app.App, args []string) error {
	fs := flags("sandesh doctor", "sandesh doctor\n\nChecks the OAuth client, each account's sign-in and sync, the background\nservice, the tmux plugin and the Claude Code MCP server.")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	bad := 0
	okf := func(f string, x ...any) { fmt.Printf("  \033[32m✓\033[0m "+f+"\n", x...) }
	warn := func(f string, x ...any) { fmt.Printf("  \033[33m!\033[0m "+f+"\n", x...) }
	fail := func(f string, x ...any) { bad++; fmt.Printf("  \033[31m✗\033[0m "+f+"\n", x...) }
	look := func(bin string) bool { _, err := exec.LookPath(bin); return err == nil }

	fmt.Printf("sandesh %s\n", app.Version)
	fmt.Println("Setup")
	if _, err := os.Stat(a.Paths.ConfigFile()); err == nil {
		okf("config: %s", a.Paths.ConfigFile())
	} else {
		okf("config: defaults (sandesh config > %s to change them)", a.Paths.ConfigFile())
	}
	if google.FakeURL() != "" {
		warn("SANDESH_GOOGLE_URL is set: talking to a fake Google at %s", google.FakeURL())
	} else if _, err := google.LoadClient(a.Paths.ClientFile(), nil); err != nil {
		fail("%v", err)
	} else {
		okf("OAuth client: %s", a.Paths.ClientFile())
	}
	if look("secret-tool") {
		okf("keyring: secret-tool")
	} else {
		warn("no secret-tool: tokens are kept in private files under %s (install libsecret)", a.Paths.Config)
	}
	accs, _ := a.Store.Accounts()
	fmt.Println("Accounts")
	if len(accs) == 0 {
		fail("none: sandesh auth add work")
	}
	for _, x := range accs {
		where := a.Tokens.Where(x.Name)
		switch {
		case google.FakeURL() != "":
		case where == "":
			fail("%s: not signed in (sandesh auth add %s)", x.Name, x.Name)
			continue
		case where == "file":
			warn("%s: token in a private file, not the keyring", x.Name)
		}
		if x.LastError != "" {
			fail("%s: last sync failed: %s", x.Name, x.LastError)
		} else if x.MailSynced == 0 {
			warn("%s: never synced (sandesh sync)", x.Name)
		} else {
			age := time.Since(time.UnixMilli(x.MailSynced)).Round(time.Second)
			if age > 10*time.Minute {
				warn("%s (%s): last synced %s ago; is the background sync running?", x.Name, x.Email, age)
			} else {
				okf("%s (%s): synced %s ago", x.Name, x.Email, age)
			}
		}
	}
	fmt.Println("Integrations")
	if look("systemctl") {
		out, _ := exec.Command("systemctl", "--user", "is-active", "sandesh.service").Output()
		if strings.TrimSpace(string(out)) == "active" {
			okf("background sync: running (sandesh.service)")
		} else {
			warn("background sync: not running (sandesh service install)")
		}
	}
	if os.Getenv("TMUX") != "" {
		out, _ := exec.Command("tmux", "list-keys", "-T", "prefix").Output()
		if strings.Contains(string(out), "sandesh") {
			okf("tmux: popup key bound")
		} else {
			warn("tmux: no sandesh key (add set -g @plugin 'Theyashsawarkar/sandesh')")
		}
	}
	if look("claude") {
		home, _ := os.UserHomeDir()
		b, _ := os.ReadFile(filepath.Join(home, ".claude.json"))
		if strings.Contains(string(b), `"sandesh"`) {
			okf("Claude Code: sandesh MCP server added")
		} else {
			warn("Claude Code: run `claude mcp add --scope user sandesh -- sandesh mcp`")
		}
	} else {
		warn("no claude on PATH: @, S, D and T in the screen need it (or set ai_command)")
	}
	if look("notify-send") {
		okf("notifications: notify-send")
	} else {
		warn("no notify-send: no desktop notifications")
	}
	if items, _ := a.Store.Outbox(store.StatusApproval); len(items) > 0 {
		warn("%d send(s) waiting for your approval: sandesh approve", len(items))
	}
	if bad > 0 {
		return exitError{1, fmt.Sprintf("%d problem(s)", bad)}
	}
	return nil
}
