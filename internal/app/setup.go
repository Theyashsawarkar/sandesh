package app

import (
	"context"
	"errors"
	"os"
	"regexp"

	"github.com/Theyashsawarkar/sandesh/internal/google"
	"github.com/Theyashsawarkar/sandesh/internal/store"
)

var validName = regexp.MustCompile(`^[A-Za-z0-9_-]{1,32}$`)

// ErrDemo: the setup screen asked to try the demo instead.
var ErrDemo = errors.New("demo")

// HasClient reports whether an OAuth client is installed (always true
// against the fake Google).
func (a *App) HasClient() bool {
	if google.FakeURL() != "" {
		return true
	}
	_, err := google.LoadClient(a.Paths.ClientFile(), nil)
	return err == nil
}

// InstallClient checks an OAuth client JSON and saves it as client.json.
func (a *App) InstallClient(data []byte) error {
	if _, err := google.ParseClient(data, nil); err != nil {
		return err
	}
	return os.WriteFile(a.Paths.ClientFile(), data, 0o600)
}

// SignIn signs an account in through the browser and adds it. say gets
// the sign-in URL in case the browser doesn't open.
func (a *App) SignIn(ctx context.Context, name, email string, chat bool, say func(url string)) error {
	if !validName.MatchString(name) {
		return errors.New("an account name is one word of letters, digits, - or _, like work or personal")
	}
	if google.FakeURL() == "" {
		conf, err := google.LoadClient(a.Paths.ClientFile(), google.Scopes(chat))
		if err != nil {
			return err
		}
		tok, err := google.SignIn(ctx, conf, email, say)
		if err != nil {
			return err
		}
		if tok.RefreshToken == "" {
			return errors.New("google gave no refresh token; remove sandesh from https://myaccount.google.com/permissions and try again")
		}
		if err := a.Tokens.Save(name, tok); err != nil {
			return err
		}
	}
	a.mu.Lock()
	delete(a.clients, name)
	a.mu.Unlock()
	return a.Store.AddAccount(store.Account{Name: name, Email: email, Chat: chat})
}
