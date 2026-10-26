# Security policy

## Supported versions

Only the latest release of sandesh gets fixes.

## Reporting a vulnerability

Please **don't open a public issue**. Report it privately through
[GitHub's security advisories](https://github.com/Theyashsawarkar/sandesh/security/advisories/new),
with what an attacker could do and how to reproduce it. You'll get a reply
within a week; a fix and a release follow as soon as it's confirmed, with
credit to you unless you'd rather not.

## What sandesh handles

sandesh runs as your user and holds your mail, your chat and a Google
sign-in:

- **Your OAuth client** (`~/.config/sandesh/client.json`, mode 0600) is your
  own, from your own Cloud project; sandesh ships none. The repository never
  contains one (`.gitignore` blocks client, token and database files).
- **Sign-in tokens** go to the Secret Service keyring (`secret-tool`); only
  without a keyring do they fall back to `~/.config/sandesh/tokens/` (0600,
  in a 0700 folder, and `sandesh doctor` warns). Sign-in is Google's
  loopback flow on 127.0.0.1 with PKCE and a state check. Tokens are sent
  only to Google, over HTTPS, and never printed or logged.
- **The local copy** (`~/.local/share/sandesh/cache.db`) is 0600 in a 0700
  folder. Remote images are never loaded, so tracking pixels don't fire.
- **Text from other people** (subjects, names, bodies, chat) has escape
  sequences, control characters and bidi overrides removed before it can
  reach your terminal, so a mail can't rewrite the screen, set your
  clipboard or disguise text. Links open in your browser only when they are
  `http(s)`, and only when you ask (`o`, `b`).
- **AI agents** see mail inside `<untrusted>` blocks and are told not to
  follow instructions in it. Every agent send waits for your approval unless
  your `send_policy` says otherwise, and a recipient you have never written
  to always asks. Every send, yours included, waits `undo_seconds` and can be
  cancelled. Agent actions are logged to `~/.local/state/sandesh/agent.log`.
- `SANDESH_GOOGLE_URL` (the test suite's fake Google) is honoured only for a
  local address.

Don't paste `client.json`, tokens, `cache.db` or `sandesh doctor` output with
your email address into public issues.
