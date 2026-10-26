# Changelog

Newest first.

## Unreleased

- **Follows the desktop theme live**: colours come from varna's
  `palette.json` (sandesh's own pick under `tools.sandesh` if the theme has
  one) and change on the running screen when the theme changes; `[colors]`
  in the config still win. `SANDESH_NO_VARNA=1` turns it off.
- **Cleaner borders**: panels and frames use the `border` colour instead of
  one almost the same as the background; the selected card is a heavy
  accent frame instead of a background fill, which looked muddy on
  translucent terminals.
- **No red error when Chat has no app configured**: marking a space read
  works on this machine, and a quiet line says what to set up; messages that
  were cached empty are fetched again.

- **Syncs in the background, not when you open it**: the screen shows the
  local copy at once and leaves syncing to the background service (setup
  now installs it); it syncs by itself only when the service isn't
  running. After a send, an archive or another change it syncs again
  shortly so the copy matches Google. Only one process syncs an account at
  a time.
- **Never hits Gmail's rate limit**: requests are paced (40 quota units a
  second), Google's "slow down" answers are retried with backoff, and a
  first sync cut short resumes instead of starting over. The first sync
  takes the newest 150 threads (`max_threads`); older mail comes through
  search.
- **Real names in chat**: names come from Chat's own messages, sandesh
  finds which member is you from your DMs (no People API needed), DMs are
  named after the other person and groups "Naeem, Rahul, Rugved +2";
  placeholder "user 1234" names are no longer stored.
- **Chat is compact**: one name line per group, a coloured bar per
  message, no boxes; messages that are only an attachment or a card say so.
- Errors get their own line (Esc hides it) instead of covering the keys,
  and the common Google errors say what to do. Card borders are brighter
  (new `border` colour).

- **A new look**: the mail list is one bordered card per thread with gaps
  between them; the thread view puts each message in its own card with a
  From / To / Date header and renders HTML mail as terminal formatting
  (headings, bold, lists, tables, buttons, numbered links), folding older
  messages and quoted replies (`E`, `Q`, `o` opens a link). Chat reads like
  a conversation: grouped by sender, yours on the right, day separators,
  thread replies and mentions marked; `i` types a message in place.
- **`m` marks read and `M` marks unread** (no more toggle), for the thread
  or every selected one, in mail and chat.
- **Setup asks for the client file's path** (with completion) instead of
  looking in Downloads, copies it into its config, and shows the three
  steps with a progress bar for the first sync.
- **Chat works without the People API**: names fall back to "user 1234"
  instead of the whole chat sync failing.
- The local copy keeps each message's HTML; existing copies refetch once.

- **Setup screen**: opening sandesh (or `prefix e`) with no credentials no
  longer stops with an error. It says what's missing and takes it right
  there: the client file found in `~/Downloads`, a path or pasted JSON, or
  a typed client ID and secret; then the Google sign-in in your browser
  (work account or personal Gmail) and the first sync, straight into the
  inbox. `d` tries the demo instead.

## v0.1.0 -- 2026-10-26

First release.

- **The screen**: Gmail and Google Chat in one keyboard-driven terminal UI,
  laid out like sangam: sections on top (Inbox, Today, Unread, Starred,
  saved Gmail searches; All, Unread, DMs, Spaces for chat), the list on the
  left, the thread or space on the right. Vim keys, `?` for all of them.
- **Write in your editor**: compose, reply, reply all, forward and chat
  messages open `$EDITOR`; a review step shows exactly what goes out, then
  `y` sends, `d` saves a Gmail draft.
- **Undo**: `z` undoes archive, trash, read and star, and cancels a send in
  its 10-second undo window.
- **Background sync** (`sandesh service install`): Gmail's change feed and
  Chat's active spaces into a local SQLite copy with full-text search;
  desktop notifications for new inbox mail, DMs and mentions.
- **MCP server** (`sandesh mcp`) with 11 tools for Claude Code and other
  agents. Agents' sends wait for your approval in a tmux popup, `A` in the
  screen or `sandesh approve`; mail content reaches them marked untrusted;
  `send_policy` sets how much they may send alone.
- **Claude keys in the screen**: `@` ask, `S` summarise, `D` draft a reply,
  `T` today's digest, through `claude -p`.
- **Command line** for everything, with `--json`: `mail`, `chat`,
  `status`, `sync`, `outbox`, `approve`, `auth`, `doctor`, `demo`.
- **tmux plugin**: `prefix e` popup and `#{sandesh_status}` for the status
  line.
