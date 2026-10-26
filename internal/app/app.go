// Package app is what every front end shares: the screen, the CLI, the
// MCP server and the daemon all open an App and go through it to sync,
// change mail, and send. One send path, one set of rules.
package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/Theyashsawarkar/sandesh/internal/config"
	"github.com/Theyashsawarkar/sandesh/internal/google"
	"github.com/Theyashsawarkar/sandesh/internal/store"
)

const Version = "0.1.0"

type App struct {
	Paths  config.Paths
	Config config.Config
	Store  *store.Store
	Tokens google.TokenStore

	mu      sync.Mutex
	clients map[string]*google.Client
	missed  map[string]time.Time // chat users whose name lookup failed

	// ChatAppMissing is set once Chat said the Cloud project has no Chat
	// app: reading works, read state and sending don't.
	ChatAppMissing atomic.Bool

	// Progress, when set, hears how a mail sync is going (threads fetched
	// of total); the setup screen draws its bar from it.
	Progress func(done, total int)
}

func Open() (*App, error) {
	p := config.DefaultPaths()
	if err := p.Ensure(); err != nil {
		return nil, err
	}
	cfg, err := config.Load(p)
	if err != nil {
		return nil, err
	}
	st, err := store.Open(p.DB())
	if err != nil {
		return nil, err
	}
	return &App{Paths: p, Config: cfg, Store: st, Tokens: google.TokenStore{FallbackDir: p.Config}, clients: map[string]*google.Client{}}, nil
}

func (a *App) Close() error { return a.Store.Close() }

// Client returns the API client for an account, signing it in from the
// stored token.
func (a *App) Client(account string) (*google.Client, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if c, ok := a.clients[account]; ok {
		return c, nil
	}
	acc, err := a.Store.Account(account)
	if err != nil {
		return nil, err
	}
	h, err := google.HTTPClient(context.Background(), a.Paths.ClientFile(), a.Tokens, account, acc.Chat)
	if err != nil {
		return nil, err
	}
	c := google.NewClient(h)
	a.clients[account] = c
	return c, nil
}

// Accounts returns the names of the signed-in accounts.
func (a *App) AccountNames() []string {
	accs, _ := a.Store.Accounts()
	var out []string
	for _, x := range accs {
		out = append(out, x.Name)
	}
	return out
}

// ResolveAccount: the named one, or the first when name is empty.
func (a *App) ResolveAccount(name string) (store.Account, error) {
	accs, err := a.Store.Accounts()
	if err != nil {
		return store.Account{}, err
	}
	if len(accs) == 0 {
		return store.Account{}, fmt.Errorf("no account yet: run `sandesh auth add work`")
	}
	if name == "" {
		return accs[0], nil
	}
	for _, x := range accs {
		if x.Name == name || strings.EqualFold(x.Email, name) {
			return x, nil
		}
	}
	return store.Account{}, fmt.Errorf("no account %q (have: %s)", name, strings.Join(a.AccountNames(), ", "))
}

// LogAgent appends one line to the agent log.
func (a *App) LogAgent(format string, args ...any) {
	f, err := os.OpenFile(a.Paths.AgentLog(), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s %s\n", time.Now().Format(time.RFC3339), fmt.Sprintf(format, args...))
}

// lock takes a cross-process lock (a file under state/), so two sandesh
// processes never sync the same account at once. ok is false when another
// one holds it.
func (a *App) lock(name string) (unlock func(), ok bool) {
	f, err := os.OpenFile(filepath.Join(a.Paths.State, name+".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return func() {}, true
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, false
	}
	return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }, true
}

// DaemonRunning reports whether the background sync is running (it holds
// state/daemon.lock while it lives).
func (a *App) DaemonRunning() bool {
	unlock, ok := a.lock("daemon")
	if ok {
		unlock()
	}
	return !ok
}

// HoldDaemonLock is taken by `sandesh daemon` for its whole life.
func (a *App) HoldDaemonLock() (func(), bool) { return a.lock("daemon") }

// Executable is the path to this binary, for popups and units.
func Executable() string {
	p, err := os.Executable()
	if err != nil {
		return "sandesh"
	}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}
