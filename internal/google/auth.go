// Package google talks to the Gmail, Google Chat and People APIs over
// plain REST with OAuth 2.0: sign-in (loopback + PKCE), tokens in the
// system keyring, and thin typed clients.
package google

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2"
)

var (
	MailScopes = []string{"https://www.googleapis.com/auth/gmail.modify", "profile"}
	ChatScopes = []string{
		"https://www.googleapis.com/auth/chat.spaces.readonly",
		"https://www.googleapis.com/auth/chat.messages",
		"https://www.googleapis.com/auth/chat.memberships.readonly",
		"https://www.googleapis.com/auth/chat.users.readstate",
		"https://www.googleapis.com/auth/directory.readonly",
	}
)

// FakeURL, when set (SANDESH_GOOGLE_URL), sends every API call to one
// local server and skips OAuth: the test suite's fake Google.
// Only a local address is accepted, so a stray variable can never send
// your mail or skip sign-in against a remote server.
func FakeURL() string {
	u := os.Getenv("SANDESH_GOOGLE_URL")
	if u == "" {
		return ""
	}
	for _, ok := range []string{"http://127.0.0.1:", "http://localhost:", "http://[::1]:"} {
		if strings.HasPrefix(u, ok) {
			return u
		}
	}
	fmt.Fprintln(os.Stderr, "sandesh: ignoring SANDESH_GOOGLE_URL: only a local address (http://127.0.0.1:PORT) is allowed")
	return ""
}

type clientFile struct {
	Installed *struct {
		ClientID     string `json:"client_id"`
		ClientSecret string `json:"client_secret"`
		AuthURI      string `json:"auth_uri"`
		TokenURI     string `json:"token_uri"`
	} `json:"installed"`
	Web json.RawMessage `json:"web"`
}

// LoadClient reads the OAuth client downloaded from the Cloud console
// (a "Desktop app" client).
func LoadClient(path string, scopes []string) (*oauth2.Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("no OAuth client at %s: download a Desktop app client from the Cloud console and run `sandesh auth setup <file.json>`", path)
		}
		return nil, err
	}
	conf, err := ParseClient(b, scopes)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return conf, nil
}

// ParseClient reads an OAuth client JSON (the Cloud console's download).
func ParseClient(b []byte, scopes []string) (*oauth2.Config, error) {
	var cf clientFile
	if err := json.Unmarshal(b, &cf); err != nil {
		return nil, fmt.Errorf("not valid JSON: %w", err)
	}
	if cf.Installed == nil {
		if cf.Web != nil {
			return nil, errors.New("this is a Web client; create a \"Desktop app\" OAuth client instead")
		}
		return nil, errors.New("not an OAuth client file (expected {\"installed\": {...}})")
	}
	if cf.Installed.ClientID == "" || cf.Installed.ClientSecret == "" {
		return nil, errors.New("the client has no client_id or client_secret")
	}
	c := cf.Installed
	authURL, tokenURL := c.AuthURI, c.TokenURI
	if authURL == "" {
		authURL = "https://accounts.google.com/o/oauth2/auth"
	}
	if tokenURL == "" {
		tokenURL = "https://oauth2.googleapis.com/token"
	}
	return &oauth2.Config{
		ClientID: c.ClientID, ClientSecret: c.ClientSecret, Scopes: scopes,
		Endpoint: oauth2.Endpoint{AuthURL: authURL, TokenURL: tokenURL, AuthStyle: oauth2.AuthStyleInParams},
	}, nil
}

// ClientJSON builds a Desktop app client file from a typed-in ID and secret.
func ClientJSON(id, secret string) []byte {
	b, _ := json.MarshalIndent(map[string]any{"installed": map[string]any{
		"client_id": strings.TrimSpace(id), "client_secret": strings.TrimSpace(secret),
		"auth_uri": "https://accounts.google.com/o/oauth2/auth", "token_uri": "https://oauth2.googleapis.com/token",
		"redirect_uris": []string{"http://localhost"},
	}}, "", "  ")
	return b
}

func Scopes(chat bool) []string {
	s := append([]string{}, MailScopes...)
	if chat {
		s = append(s, ChatScopes...)
	}
	return s
}

// SignIn runs the loopback flow: a one-shot server on 127.0.0.1, the
// browser opened on Google's consent page, the code exchanged with PKCE.
// say() is told the URL in case the browser doesn't open.
func SignIn(ctx context.Context, conf *oauth2.Config, hint string, say func(string)) (*oauth2.Token, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	defer ln.Close()
	c := *conf
	c.RedirectURL = fmt.Sprintf("http://127.0.0.1:%d/", ln.Addr().(*net.TCPAddr).Port)
	state := randString()
	verifier := oauth2.GenerateVerifier()
	opts := []oauth2.AuthCodeOption{oauth2.AccessTypeOffline, oauth2.ApprovalForce, oauth2.S256ChallengeOption(verifier)}
	if hint != "" {
		opts = append(opts, oauth2.SetAuthURLParam("login_hint", hint))
	}
	url := c.AuthCodeURL(state, opts...)

	type result struct {
		code string
		err  error
	}
	got := make(chan result, 1)
	var once sync.Once
	srv := &http.Server{ReadHeaderTimeout: 10 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("state") == "" && q.Get("code") == "" && q.Get("error") == "" {
			http.NotFound(w, r)
			return
		}
		var res result
		switch {
		case q.Get("error") != "":
			res.err = fmt.Errorf("google said: %s", q.Get("error"))
		case q.Get("state") != state:
			res.err = errors.New("sign-in answer did not match this request; try again")
		default:
			res.code = q.Get("code")
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if res.err != nil {
			fmt.Fprintf(w, "<p style='font:16px sans-serif'>sandesh: %s</p>", html.EscapeString(res.err.Error()))
		} else {
			fmt.Fprint(w, "<p style='font:16px sans-serif'>sandesh is signed in. You can close this tab.</p>")
		}
		once.Do(func() { got <- res })
	})}
	go srv.Serve(ln)
	defer srv.Close()

	say(url)
	_ = exec.Command("xdg-open", url).Start()

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case res := <-got:
		if res.err != nil {
			return nil, res.err
		}
		return c.Exchange(ctx, res.code, oauth2.VerifierOption(verifier))
	}
}

func randString() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// ---------------------------------------------------------------- tokens

// TokenStore keeps refresh tokens in the Secret Service keyring through
// secret-tool, or, when there is no keyring, in a 0600 file (doctor warns).
type TokenStore struct{ FallbackDir string }

func keyringOK() bool {
	_, err := exec.LookPath("secret-tool")
	return err == nil
}

func (ts TokenStore) file(account string) string {
	return filepath.Join(ts.FallbackDir, "tokens", account+".json")
}

func (ts TokenStore) Save(account string, tok *oauth2.Token) error {
	b, _ := json.Marshal(tok)
	if keyringOK() {
		cmd := exec.Command("secret-tool", "store", "--label=sandesh: "+account, "service", "sandesh", "account", account)
		cmd.Stdin = strings.NewReader(string(b))
		if out, err := cmd.CombinedOutput(); err == nil {
			_ = os.Remove(ts.file(account))
			return nil
		} else if len(out) > 0 {
			fmt.Fprintf(os.Stderr, "sandesh: keyring: %s (keeping the token in a private file instead)\n", strings.TrimSpace(string(out)))
		}
	}
	if err := os.MkdirAll(filepath.Dir(ts.file(account)), 0o700); err != nil {
		return err
	}
	return os.WriteFile(ts.file(account), b, 0o600)
}

func (ts TokenStore) Load(account string) (*oauth2.Token, error) {
	var b []byte
	if keyringOK() {
		if out, err := exec.Command("secret-tool", "lookup", "service", "sandesh", "account", account).Output(); err == nil && len(out) > 0 {
			b = out
		}
	}
	if b == nil {
		f, err := os.ReadFile(ts.file(account))
		if err != nil {
			return nil, fmt.Errorf("account %q is not signed in (sandesh auth add %s)", account, account)
		}
		b = f
	}
	var tok oauth2.Token
	if err := json.Unmarshal(b, &tok); err != nil {
		return nil, err
	}
	return &tok, nil
}

// Where tells doctor where an account's token lives.
func (ts TokenStore) Where(account string) string {
	if keyringOK() {
		if out, err := exec.Command("secret-tool", "lookup", "service", "sandesh", "account", account).Output(); err == nil && len(out) > 0 {
			return "keyring"
		}
	}
	if _, err := os.Stat(ts.file(account)); err == nil {
		return "file"
	}
	return ""
}

func (ts TokenStore) Delete(account string) {
	if keyringOK() {
		_ = exec.Command("secret-tool", "clear", "service", "sandesh", "account", account).Run()
	}
	_ = os.Remove(ts.file(account))
}

// savingSource writes a refreshed token back when it changes.
type savingSource struct {
	src     oauth2.TokenSource
	store   TokenStore
	account string
	mu      sync.Mutex
	last    string
}

func (s *savingSource) Token() (*oauth2.Token, error) {
	t, err := s.src.Token()
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if t.RefreshToken != "" && t.AccessToken != s.last {
		s.last = t.AccessToken
		_ = s.store.Save(s.account, t)
	}
	return t, nil
}

// HTTPClient returns an authorised client for an account.
func HTTPClient(ctx context.Context, clientPath string, store TokenStore, account string, chat bool) (*http.Client, error) {
	if FakeURL() != "" {
		return &http.Client{Timeout: 30 * time.Second}, nil
	}
	conf, err := LoadClient(clientPath, Scopes(chat))
	if err != nil {
		return nil, err
	}
	tok, err := store.Load(account)
	if err != nil {
		return nil, err
	}
	src := &savingSource{src: conf.TokenSource(ctx, tok), store: store, account: account, last: tok.AccessToken}
	c := oauth2.NewClient(ctx, oauth2.ReuseTokenSource(tok, src))
	c.Timeout = 60 * time.Second
	return c, nil
}
