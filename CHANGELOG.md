# Changelog

Newest first.

## Unreleased

- **Take a reaction back**: in the reaction window (`e`), the ones you
  added are marked ✓, and picking one removes it.

- **Jump to the original**: Enter on a picked reply jumps to the message it
  quotes, loading earlier messages until it's there; Enter again follows the
  chain further back, Backspace retraces it.

- **Chat, redone as bubbles** (option A from the mockup): theirs left,
  yours right; quotes, files and numbered links inside the bubble;
  reactions underneath; "(edited)"; a red "new" line where unread starts
  (the chat opens there); who's in the chat on top.
- **Chat list in groups**: pinned (`p`), today, earlier; `@N` badges for
  unread mentions of you.
- **More on a picked message**: `e` reacts (an emoji window), `E` edits
  yours, `d d` deletes yours; `k` past the first message loads 50 earlier
  ones.
- `[` / `]` move between chats without leaving the conversation; `Ctrl+k`
  opens the chat switcher; `Ctrl+o` adds files to the message you're typing.

- **Reply to a specific chat message**: Enter (or 2) on a chat lets j/k pick
  messages; `r` replies to the picked one, quoting it like Google Chat
  (quotedMessageMetadata), with "↩ replying to …" over the message box;
  from the list `r` answers the latest message from someone else. `y`
  copies and `v` saves the picked message.
- **Quoted replies show**: a message that quotes another shows it above
  (`❝ You: …`); recent chats are fetched again once to fill this in.
- No more "reply in a thread" labels: Google reports every space as
  threaded, and replies show inline as quotes, as in the browser.

- **Name a group**: starting a chat with two or more people asks for a name
  (named: a space; unnamed: a group chat), and `n` names an existing group
  chat on Google Chat (it becomes a space, same people and messages).
- **Sign in again without leaving**: when Google wants the newer permission
  (starting chats, naming groups), the window says so and Enter signs in
  again in the browser, then carries on with what you were doing.

- **Start a conversation (`N` in chat)**: pick people (from chat, mail
  contacts and the Workspace directory); sandesh opens the DM you already
  have with them (found on Google, earlier messages fetched) or starts one,
  a group chat for several, and puts you in the message box.
- **Chat search finds people and conversations**: CONVERSATIONS, PEOPLE and
  MESSAGES groups; Enter on a person opens (or starts) your chat with them.
- MCP `chat_send` takes `to` (email addresses) instead of a space.

- **Scheduled tabs**: scheduled mail has its own **Scheduled** section next
  to Sent, and scheduled chat messages a **Scheduled** tab in chat, soonest
  first with their time and counts on the tabs (`t` moves, Enter edits, `z`
  cancels). Sent goes back to mail that went (and what's sending or failed).

- **Send later**: `Ctrl+t` in a mail, `t` on review, `Ctrl+t` while typing a
  chat message: pick a time or type it ("tomorrow 9am", "fri 6pm", "in 2h",
  "3 nov 10:30"). Scheduled sends wait in the outbox and the background
  service sends them on time (on its own loop now, so a slow sync never
  delays one); a send that goes out late because the computer was off says
  so. They're listed on top of Sent (`t` moves, Enter edits, `z` cancels).
  CLI `--at` and `outbox reschedule`; MCP `send_at`.

- **Search no longer freezes the screen**: a one-letter search took ~5 s on a
  real mailbox (the full-text index was queried once per thread); it is now
  one lookup (~20-50 ms), and it runs in the background after a short pause
  in typing, with stale answers dropped and Gmail's search cancelled when
  you type on. Previews are drawn in the background, once.
- **Search where you are**: a mail section searches that section (Today,
  Sent, Inbox...), a chat space searches that space; Tab widens to all mail
  or every chat. Opening a chat result scrolls to the message and marks it.

- **`?` is a window**: every key, grouped in columns in the middle of the
  screen, with a filter as you type.
- **A file picker of its own** (replacing fzf): a window over the screen
  with fuzzy matching and a preview of the highlighted file (text, image
  size or the picture itself with chafa, a PDF's first page with
  pdftotext); Tab picks several; Ctrl+r switches folder, Ctrl+h hidden.
- **Rename a chat (`n`)**: spaces on Google Chat (new `chat.spaces`
  permission; sign in again once), DMs and unnamed groups by a name kept on
  this machine.
- The popup is bigger by default (96% × 94%).

- **Round ends on every filled label**: @mentions in chat, the active tab,
  the mode label, key hints, the send button and the approval banner use
  Nerd Font half circles like the avatars (`nerd_font = false` keeps square).

- **A search window**: `/` (or `s`) opens a floating search over the
  screen. Mail and chat from this machine show as you type, matches
  highlighted, with a preview of the highlighted result; when typing pauses,
  Gmail's own search adds what isn't here yet (☁). Gmail search syntax
  works. Replaces the old local filter and separate server search.
- **Older mail on demand**: no background download by default any more
  (`keep = "none"`; it competed with searches for Gmail's rate limit). `j`
  at the end of a list loads the next 10 older threads of that section;
  `+` / `-` change the batch (10, 25, 50, 100), `older_page` the default.
- **Search results survive a refresh**: they're kept like a section.
- **Rounded everywhere, round avatars**: the selected card keeps rounded
  corners (heavier sides mark it), mail tables have rounded corners, and
  initials sit in round pills (Nerd Font half circles; `nerd_font = false`
  for plain ones).

- **Compose, redone** (from the approved mockup): a new mail gets the whole
  screen; a reply keeps the original on the left (PgUp/PgDn). Recipients are
  pills with initials (←/→ selects, Backspace removes); someone you've never
  written to is marked *first time*. Suggestions highlight the typed letters
  and say why they're there (`wrote 4× · 2d ago`, `wrote you 9×`,
  `directory`). Cc/Bcc hidden until `Ctrl+b`. Your Gmail signature is
  fetched and added (`Ctrl+g` toggles). Drafts are saved on disk every few
  seconds and reopened by the same key. A status line with recipients,
  files, words and buttons; `Ctrl+f` switches the sending account. Tab now
  adds a typed contact and moves on.
- **A Sent section**: who you wrote to, and whether they answered (`↩ Priya
  replied 09:58`, `no reply yet · 2d`); mail in the outbox (sending, waiting
  for approval, failed) on top, `z` cancels, `R` retries a failed send.
- **All your mail, not just 30 days**: after the first sync, older mail is
  downloaded in the background (~150 threads a minute, newest first) until
  the mailbox is complete, with a progress bar in the top bar. `keep`
  limits it (`"1y"`, `"6m"`, `"none"`).

- **A compose screen**: `c`, `r`, `a`, `f` and `D` open a form in the right
  panel instead of a bare file in the editor: From, To, Cc, Bcc as chips,
  Subject, Files and the body, with a reply's quote folded underneath.
  Recipients autocomplete from your contacts (ranked by how often you write
  to them) and from your Google Workspace directory (People API). `Ctrl+s`
  sends, `Ctrl+d` saves a draft, `Ctrl+o` attaches, `Ctrl+e` still takes
  the whole mail to your editor and back; `compose = "editor"` keeps the
  old way. An agent's message is edited here too.

- **Syncs ~10× cheaper**: a sync with nothing new went from 23 Google calls
  (12.6 s) to 2 (1.1 s) on a real account. Lookups that fail (a DM's title, a
  person's name) are remembered and retried every 6 hours instead of on
  every sync; changed spaces are fetched four at a time; cached spaces are
  read in one query. The background service has a soft 128 MB memory cap.
- **Chat read state follows the browser**: a space read on the web or phone
  turns read here too, without waiting for a new message. `M` on a space is
  Chat's own "mark as unread" (it stays unread after a sync, and marks the
  latest message from someone else, even when you wrote last).
- **Forwarding keeps attachments**: they're listed as `Attach: original: …`
  in the editor (delete to drop) and fetched from Gmail when sent.
- File picker (`+`, `U`) starts in the folder you're in; the review screen
  shows each file's name, size and folder, and says when one is missing.
- Fixes: `W` no longer glues the name onto the search (an empty name keeps
  the search); `mail archive` and friends say what they did; the outbox
  names files; `sandesh mcp` exits quietly when its client closes it; the
  setup screen's border no longer shifts on the title line.
- `SANDESH_DEBUG_HTTP=1` logs every Google request with its time.

- **Compose works without $EDITOR**: with no editor set, sandesh used `vi`,
  which many systems don't have, so `c`, `r` and friends failed at once. It
  now picks the first installed of nvim, vim, vi, helix, micro, nano, kak
  and emacs, and says which editor failed and how to set one.

- **Chat sends instantly**: chat messages skip the 10-second undo window
  (`chat_undo_seconds`, default 0; mail keeps `undo_seconds`), appear in the
  conversation as soon as Chat accepts them, and sending always jumps to the
  end of the chat.

- **The tmux popup runs the current sandesh**: tmux's environment often lacks
  `~/.local/bin`, so the plugin fell back to a copy built once in its own
  folder and kept running it after updates. It now looks in `~/.local/bin`
  and `~/go/bin` too, and rebuilds its own copy whenever the code is newer.
- **New chat messages show up**: a conversation scrolled to the bottom stays
  at the bottom when messages arrive, like a chat app; before, they landed
  below the visible part. The space list says "You:" for your own last
  message.

- **Files, both ways**: `v` saves a mail's attachments or a chat's files to
  `~/Downloads/sandesh` (an fzf picker when there are several), `V` saves
  and opens them. Send files with `Attach:` in the mail editor, `+` on the
  review screen, or `U` in a chat space; `--attach` on `mail send` and
  `chat send`; `mail save` / `chat save` download from the shell. Big mails
  (over 4 MB) go through Gmail's upload endpoint. Files are copied privately
  when queued, so what you reviewed is what is sent.
- **Agents and files**: `mail_send`, `mail_draft` and `chat_send` take
  `attachments`; such sends always ask for approval, which lists each file.
  New `attachment_save` tool, confined to the downloads folder.
- Other people's chat messages get a solid bar in their colour.

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
