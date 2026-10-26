# sandesh -- notes for agents

Gmail and Google Chat in the terminal: one Go binary (`cmd/sandesh`) with a
Bubble Tea screen, a CLI, an MCP server and a sync daemon, all over one
local SQLite copy. Usable as the `sandesh` command (install.sh, make
install, packaging/arch, kosha) and as a TPM plugin (`sandesh.tmux` binds a
popup key and `#{sandesh_status}`). It is pulled into Vayu
(github.com/Theyashsawarkar/vayu) as a tmux plugin, but must work on any
Linux setup without Vayu: no hardcoded paths, Vayu-specific behaviour only
through the `@sandesh-*` options and config.toml.

- Layout: `internal/app` is the core every front end shares (sync, mail
  actions, the outbox and send policy); `internal/store` the SQLite schema;
  `internal/google` OAuth and the REST clients; `internal/cli` the commands
  (and the editor template, reply/forward helpers the screen reuses);
  `internal/tui` the screen; `internal/mcpserver` the agent tools;
  `internal/fake` an in-memory Google used by every test and `sandesh demo`.
- Every send goes through `app.Queue` → outbox → `app.DeliverDue`. Never add
  a path that calls the Gmail or Chat send API directly. An agent's send
  must stay behind `needsApproval`; mail and chat text returned to agents
  stays inside `<untrusted>` blocks.
- Keys live in `internal/tui/keys.go`. After adding, changing or removing
  one: `go test ./internal/tui -update` (rewrites `scripts/keybindings`,
  which Vayu's keybinding search reads), and update the README's key
  tables.
- The README documents every feature, key, option, command and dependency:
  a change to any of them updates it. Every change gets a CHANGELOG.md
  entry; releases are tags (`vX.Y.Z`) with `Version` in internal/app/app.go
  and `pkgver` in packaging/arch/PKGBUILD bumped to match.
- `make check` must pass (vet, gofmt, ShellCheck, tests, site check).
- Test against the fake Google (`SANDESH_HOME`, `SANDESH_GOOGLE_URL`), never
  a real account. Drive the screen in a throwaway tmux server:
  `tmux -L t -f /dev/null new -d -s o -x 150 -y 40 "env -u TMUX sandesh demo"`,
  then `tmux -L t send-keys -t o ...` / `capture-pane -p -t o`, and
  `tmux -L t kill-server` after. Never touch the user's live tmux or mail.
- The website (docs/, GitHub Pages) is generated: after changing README.md,
  CHANGELOG.md or docs/adr/, run `python3 scripts/build-site.py` and commit
  docs/ (CI checks it). A significant design decision gets a new ADR in
  docs/adr/ (next number; supersede, never rewrite).
