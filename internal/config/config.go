// Package config holds sandesh's paths and its settings file
// (~/.config/sandesh/config.toml). Every setting has a default, so a
// missing file is a valid configuration.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/BurntSushi/toml"
)

// Paths, following the XDG base directories. SANDESH_HOME, when set, puts
// all of them under one folder (tests and `sandesh demo` use it).
type Paths struct {
	Config string // config.toml, client.json
	Data   string // cache.db
	State  string // agent.log, sections.json
}

func DefaultPaths() Paths {
	if h := os.Getenv("SANDESH_HOME"); h != "" {
		return Paths{Config: filepath.Join(h, "config"), Data: filepath.Join(h, "data"), State: filepath.Join(h, "state")}
	}
	home, _ := os.UserHomeDir()
	env := func(k, def string) string {
		if v := os.Getenv(k); v != "" {
			return v
		}
		return filepath.Join(home, def)
	}
	return Paths{
		Config: filepath.Join(env("XDG_CONFIG_HOME", ".config"), "sandesh"),
		Data:   filepath.Join(env("XDG_DATA_HOME", ".local/share"), "sandesh"),
		State:  filepath.Join(env("XDG_STATE_HOME", ".local/state"), "sandesh"),
	}
}

func (p Paths) Ensure() error {
	for _, d := range []string{p.Config, p.Data, p.State} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return err
		}
	}
	return nil
}

func (p Paths) ConfigFile() string { return filepath.Join(p.Config, "config.toml") }
func (p Paths) ClientFile() string { return filepath.Join(p.Config, "client.json") }
func (p Paths) DB() string         { return filepath.Join(p.Data, "cache.db") }
func (p Paths) AgentLog() string   { return filepath.Join(p.State, "agent.log") }

// Send policies for messages an agent asks to send.
const (
	PolicyConfirm       = "confirm"        // every agent send waits for your OK
	PolicyKnownContacts = "known-contacts" // replies to people you've written to go straight out
	PolicyAllow         = "allow"          // agent sends to known people go straight out
)

type Section struct {
	Name  string `toml:"name"`
	Label string `toml:"label"` // a Gmail label id: INBOX, STARRED, UNREAD, or a user label's id
	Query string `toml:"query"` // a Gmail search, run on the server at every sync
	Today bool   `toml:"today"` // only threads with a message from today
}

type Colors struct {
	Accent  string `toml:"accent"`
	Accent2 string `toml:"accent2"`
	Text    string `toml:"text"`
	Subtle  string `toml:"subtle"`
	Muted   string `toml:"muted"`
	Surface string `toml:"surface"`
	Border  string `toml:"border"`
	Base    string `toml:"base"`
	Mantle  string `toml:"mantle"`
	Green   string `toml:"green"`
	Yellow  string `toml:"yellow"`
	Red     string `toml:"red"`
	Blue    string `toml:"blue"`
	Peach   string `toml:"peach"`
}

type Config struct {
	SendPolicy  string `toml:"send_policy"`
	UndoSeconds int    `toml:"undo_seconds"`
	// ChatUndoSeconds is the undo window for chat messages: none by
	// default, chat should feel instant
	ChatUndoSeconds int      `toml:"chat_undo_seconds"`
	MailInterval    duration `toml:"mail_interval"`
	ChatInterval    duration `toml:"chat_interval"`
	InitialDays     int      `toml:"initial_days"`
	// Keep is how much older mail to download in the background: "all"
	// (default), or an age like "1y", "6m", "90d"; "none" stops at the
	// first sync
	Keep       string   `toml:"keep"`
	MaxThreads int      `toml:"max_threads"`
	Notify     bool     `toml:"notify"`
	AICommand  []string `toml:"ai_command"`
	Editor     string   `toml:"editor"`
	// Compose is "screen" (the built-in compose screen) or "editor"
	// (write the whole mail in $EDITOR)
	Compose  string    `toml:"compose"`
	Sections []Section `toml:"section"`
	Colors   Colors    `toml:"colors"`
	// Include is a theme file read after this one (varna writes
	// ~/.local/state/varna/sandesh.toml); its [colors] win.
	Include string `toml:"include"`
}

type duration struct{ time.Duration }

func (d *duration) UnmarshalText(b []byte) error {
	v, err := time.ParseDuration(string(b))
	d.Duration = v
	return err
}

func (d duration) MarshalText() ([]byte, error) { return []byte(d.String()), nil }

func Default() Config {
	return Config{
		SendPolicy:   PolicyConfirm,
		UndoSeconds:  10,
		MailInterval: duration{60 * time.Second},
		ChatInterval: duration{30 * time.Second},
		InitialDays:  30,
		Keep:         "all",
		MaxThreads:   150,
		Notify:       true,
		AICommand:    []string{"claude", "-p"},
		Sections: []Section{
			{Name: "Inbox", Label: "INBOX"},
			{Name: "Today", Label: "INBOX", Today: true},
			{Name: "Unread", Label: "UNREAD"},
			{Name: "Starred", Label: "STARRED"},
			{Name: "Sent", Label: "SENT"},
		},
		// Catppuccin Mocha, the fallback when no theme is included.
		Colors: Colors{
			Accent: "#74c7ec", Accent2: "#cba6f7", Text: "#cdd6f4", Subtle: "#a6adc8", Muted: "#7f849c",
			Surface: "#313244", Border: "#585b70", Base: "#1e1e2e", Mantle: "#181825", Green: "#a6e3a1", Yellow: "#f9e2af",
			Red: "#f38ba8", Blue: "#89b4fa", Peach: "#fab387",
		},
	}
}

// Load reads the config file over the defaults. A missing file is fine.
func Load(p Paths) (Config, error) {
	c := Default()
	applyVarna(&c.Colors) // the desktop theme; the file's [colors] win
	sections := c.Sections
	c.Sections = nil
	if _, err := toml.DecodeFile(p.ConfigFile(), &c); err != nil && !errors.Is(err, os.ErrNotExist) {
		d := Default()
		applyVarna(&d.Colors)
		return d, fmt.Errorf("%s: %w", p.ConfigFile(), err)
	}
	if len(c.Sections) == 0 {
		c.Sections = sections
	}
	if c.Include != "" {
		inc := expand(c.Include)
		var theme struct {
			Colors Colors `toml:"colors"`
		}
		if _, err := toml.DecodeFile(inc, &theme); err == nil {
			mergeColors(&c.Colors, theme.Colors)
		}
	}
	switch c.SendPolicy {
	case PolicyConfirm, PolicyKnownContacts, PolicyAllow:
	default:
		return c, fmt.Errorf("send_policy %q: use confirm, known-contacts or allow", c.SendPolicy)
	}
	if c.ChatUndoSeconds < 0 {
		c.ChatUndoSeconds = 0
	}
	if c.UndoSeconds < 0 {
		c.UndoSeconds = 0
	}
	if len(c.AICommand) == 0 {
		c.AICommand = Default().AICommand
	}
	return c, nil
}

func mergeColors(dst *Colors, src Colors) {
	set := func(d *string, s string) {
		if s != "" {
			*d = s
		}
	}
	set(&dst.Accent, src.Accent)
	set(&dst.Accent2, src.Accent2)
	set(&dst.Text, src.Text)
	set(&dst.Subtle, src.Subtle)
	set(&dst.Muted, src.Muted)
	set(&dst.Surface, src.Surface)
	set(&dst.Border, src.Border)
	set(&dst.Base, src.Base)
	set(&dst.Mantle, src.Mantle)
	set(&dst.Green, src.Green)
	set(&dst.Yellow, src.Yellow)
	set(&dst.Red, src.Red)
	set(&dst.Blue, src.Blue)
	set(&dst.Peach, src.Peach)
}

func expand(p string) string {
	if len(p) > 1 && p[:2] == "~/" {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, p[2:])
	}
	return os.ExpandEnv(p)
}

// Example is what `sandesh config` prints and what the README shows.
const Example = `# ~/.config/sandesh/config.toml -- every line is optional.

# What happens when an AI agent asks to send:
#   "confirm"        every send waits for your OK (default)
#   "known-contacts" replies to people you've written to go straight out
#   "allow"          sends to people you've written to go straight out
# A recipient you've never written to always asks, whatever the policy.
send_policy = "confirm"

undo_seconds  = 10     # every mail waits this long; z (or Ctrl+c) cancels
chat_undo_seconds = 0  # the same for chat messages (default: sent at once)
mail_interval = "60s"  # how often the background sync checks Gmail
chat_interval = "30s"  # and Google Chat
initial_days  = 30     # how far back the quick first sync goes
keep          = "all"  # then older mail fills in, in the background: "all", "1y", "6m", "90d" or "none"
max_threads   = 150    # at most this many threads on the first sync (older mail: search)
notify        = true   # desktop notification for new mail and DMs
ai_command    = ["claude", "-p"]   # what @, S, D and T run
# editor      = "nvim"             # default: $VISUAL, $EDITOR, else the first one installed
# compose     = "editor"           # write mail in the editor instead of the compose screen

# Colours follow varna's desktop theme (~/.local/state/varna/palette.json)
# and change live with it. Set any of them here to override, or include a
# theme file read after this one (its [colors] win).
# [colors]
# accent = "#74c7ec"
# include = "~/.config/sandesh/theme.toml"

# Sections are the tabs on top. label is a Gmail label id, query a Gmail
# search run on the server, today keeps only threads with mail from today.
[[section]]
name  = "Inbox"
label = "INBOX"

[[section]]
name  = "Today"
label = "INBOX"
today = true

[[section]]
name  = "GitHub"
query = "from:notifications@github.com newer_than:14d"
`
