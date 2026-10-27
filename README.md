<p align="center">
  <img alt="sandesh" src="assets/logo.svg" width="120">
</p>

<p align="center">
  <b>Gmail and Google Chat without leaving the terminal.</b><br>
  Every unread mail and chat in one keyboard-driven screen, and an agent that can read, draft and send them for you.
</p>

> *Sandesh* (संदेश) is Sanskrit for "message": the thing carried from one
> person to another, whichever road it took.

Checking mail and Google Chat usually means a browser tab: a context
switch, a mouse, and a page that pulls you into everything else.
**sandesh** keeps a local copy of both, puts the unread counts in your
tmux status line and opens the full screen with `prefix e`, laid out like
[sangam](https://github.com/Theyashsawarkar/sangam). It runs like lazygit:
type `sandesh` in any terminal. And because it ships an MCP server, you can
tell Claude "read today's mails" or "mail Priya that Thursday is fine".

## Contents

- [Features](#features)
- [Install](#install)
- [Getting started](#getting-started)
- [The screen](#the-screen)
- [Keys](#keys)
- [Claude and other agents](#claude-and-other-agents)
- [Sending safely](#sending-safely)
- [Command line](#command-line)
- [Using it inside tmux](#using-it-inside-tmux)
- [Configuration](#configuration)
- [Files and environment](#files-and-environment)
- [How it works](#how-it-works)
- [Troubleshooting](#troubleshooting)
- [Contributing](#contributing)
- [Roadmap](#roadmap)
- [License](#license)

## Features

- **Mail and chat in one place**: Gmail threads and Google Chat spaces and
  DMs, one `Tab` apart.
- **Sections, like saved searches**: Inbox, Today, Unread, Starred, or any
  Gmail search (`from:priya newer_than:7d`). Switch with `h`/`l`; each tab
  shows its unread count. Search with `s`, keep it as a section with `W`.
- **Opens instantly, works offline**: a background service keeps a local
  copy in sync (Gmail's change feed every minute, chat every 30 seconds),
  so nothing waits on Google. Reading, filtering and local search work
  without a network.
- **Write in your editor**: compose, reply, reply all and forward open
  `$EDITOR` with a header block; save and quit to review, then send.
- **Undo**: archive, trash, read and star are undone with `z`; every send
  waits 10 seconds first, so `z` cancels that too.
- **Claude can use it**: list, search, read, archive and draft mail, read
  and post in chat, through an MCP server and a `--json` CLI. In the screen,
  `@` asks Claude, `S` summarises, `D` drafts a reply, `T` gives today's
  digest.
- **Nothing goes out without you**: an agent's send waits for your `y` in a
  popup (the same idea as [anumati](https://github.com/Theyashsawarkar/anumati)
  for root commands).
- **Notifications**: new inbox mail and chat DMs or mentions, through
  `notify-send`.
- **Several accounts**: work and personal side by side; `u` focuses on one.
- **Safe with your login**: Google's own sign-in in your browser; tokens
  live in the system keyring and are never printed.
- **Vim keys throughout**, no arrows needed.

## Install

**Requirements**: Linux, a Google account (Google Chat needs a Google
Workspace account; a personal Gmail gets mail only). Optional: `tmux` for
the popup and status line, `secret-tool` (libsecret) for the keyring,
`notify-send` for notifications, `fzf` for the label picker, `wl-copy` or
`xclip` for copying links, [Claude Code](https://docs.claude.com/en/docs/claude-code)
for the AI keys.

With [kosha](https://github.com/Theyashsawarkar/kosha):

```sh
kosha install sandesh
```

Or with the installer, which downloads the release binary into
`~/.local/share/sandesh` (or builds it with Go) and links
`~/.local/bin/sandesh`:

```sh
curl -fsSL https://raw.githubusercontent.com/Theyashsawarkar/sandesh/main/install.sh | bash
```

Or from source (Go 1.26+):

```sh
git clone https://github.com/Theyashsawarkar/sandesh && cd sandesh
make && sudo make install          # or: make install PREFIX=~/.local
```

Arch: `cd packaging/arch && makepkg -si`.

Update by running the installer again; `install.sh --uninstall` removes it.

## Getting started

Try it first on made-up mail, no Google needed:

```sh
sandesh demo
```

Then just run `sandesh` (or press `prefix e` in tmux). With no
credentials yet it opens a setup screen in three steps. **1, the OAuth
client**: it asks where your client file is (`Tab` completes the path,
`Enter` on a folder opens it), checks it, and copies it into
`~/.config/sandesh/client.json`, so the download can be deleted; a pasted
JSON or a typed client ID and secret work too. **2, sign in**: `Enter`
opens Google's sign-in in your browser (`Tab` switches between a work
account and a personal Gmail). **3, the first sync**, with a progress bar,
and then the inbox.

| Key | Setup screen |
|---|---|
| `Tab` | Switch to the next path completion (Enter on a folder opens it) |
| `Enter` | Save the OAuth client file at this path, or start the Google sign-in |
| `Ctrl+t` | Enter the OAuth client ID and secret by hand instead |
| `Ctrl+o` | Open the Google Cloud console credentials page |
| `Ctrl+g` | Show the steps for creating the OAuth client |
| `Ctrl+d` | Start the demo on example mail instead |
| `n` | Rename the account before signing in |
| `c` | Pick a different OAuth client file |
| `y` | Copy the sign-in link |

To use your own mail, sandesh needs an OAuth client of your own (Google
doesn't hand out one for everyone's terminal tools). This takes about five
minutes, once:

1. Open the [Google Cloud console](https://console.cloud.google.com/), create
   a project (any name), and enable the **Gmail API**, the **Google Chat
   API** and the **People API** (APIs & Services → Library).
2. **OAuth consent screen**: for a Workspace account choose **Internal**
   (no Google review needed; your admin may have to allow it). For a
   personal Gmail choose **External**, add yourself as a test user, then
   **Publish** it, or Google signs you out every 7 days.
3. **Google Chat API → Configuration**: fill in an app name, an avatar URL
   and a description, and turn off "interactive features". Chat's API
   wants this even though sandesh only acts as you.
4. **Credentials → Create credentials → OAuth client ID → Desktop app**,
   and download the JSON.

Then either run `sandesh` and follow the setup screen, or do the same from
the command line:

```sh
sandesh auth setup ~/Downloads/client_secret_*.json
sandesh auth add work                  # opens Google's sign-in page
sandesh auth add personal --no-chat    # a personal Gmail: mail only
sandesh service install                # background sync and notifications
claude mcp add --scope user sandesh -- sandesh mcp
sandesh doctor                         # checks all of it
```

`sandesh` opens the screen.

## The screen

Sections on top, the list on the left, the selected thread or space on
the right, keys at the bottom.

- **Mail list**: one bordered card per thread with a blank line between
  them: who and when, the subject, the start of the last message, and chips
  for the message count and labels. Unread threads have a `●` on the card's
  edge, the selected card is drawn in the accent colour, threads picked with
  `x` show a `✓`.
- **Thread**: the subject and its labels on top, then each message in its
  own card with a From / To / Date header. HTML mail is turned into
  terminal formatting: headings, bold and italics, lists, real tables drawn
  as tables, buttons, and numbered links (`o` and a number opens one in the
  browser). Layout tables and hidden preview text are dropped. Older
  messages fold to one line (`E` opens them all), and quoted replies fold
  away (`Q` shows them).
- **Chat**: messages grouped by sender with their initials, yours on the
  right in green, day separators, replies in a thread marked `↳`,
  @mentions highlighted. `i` types a message right there; `c` writes it in
  your editor. Unread follows Google Chat's own read state.
- **Writing**: `c`, `r`, `a` and `f` open your editor. The file starts with
  `To:`, `Cc:`, `Bcc:` and `Subject:` lines; write under the marker line,
  save and quit. You then see exactly what goes out: `y` sends, `e` edits
  again, `d` saves it as a Gmail draft, `n` discards. An empty message is
  never sent. Every send waits 10 seconds; `z` cancels it.
- **Search**: `/` filters what's on screen as you type; `s` runs a Gmail
  search on the server and shows the result as a temporary section, `W`
  keeps it, `X` removes a kept one.
- **Approvals**: when an agent wants to send, a banner says so; `A` shows
  the message with `y` / `e` / `n`.

## Keys

The screen opens in **normal mode**: letters are commands. `/`, `s` and
`@` switch to typing; `Enter` or `Esc` switches back. `?` shows every key.

**Move and find**

| Key | Does |
|---|---|
| `j` / `k` | Move to the next / previous thread or space (j at the end loads older mail) |
| `+` / `-` | Set how many older mails j at the end loads (10, 25, 50, 100) |
| `g` / `G` | Jump to the first / last |
| `h` / `l` | Switch to the previous / next section |
| `Tab` | Switch between mail and chat |
| `1` / `2` | Focus the list / the preview |
| `Enter` | Open the thread or space (marks it read) |
| `J` / `K` | Scroll the preview down / up |
| `E` | Toggle every message in the thread open / folded |
| `Q` | Toggle the quoted text in the thread |
| `o` | Open a link from the preview by its number |
| `Ctrl+d` / `Ctrl+u` | Scroll the preview half a page down / up |
| `/` or `s` | Open the search window (searches where you are: this section, or this chat) |
| `W` | Save the current search as a section |
| `X` | Remove the current saved-search section |
| `u` | Switch account (all, then each one) |

**Act**

| Key | Does |
|---|---|
| `c` | Create a new mail, or a new message in this space (compose) |
| `r` | Send a reply to the thread (in chat: to the picked message, quoting it) |
| `a` | Send a reply to everyone on the thread (reply all) |
| `f` | Send the thread on to someone else (forward) |
| `e` | Move the thread out of the inbox (archive; or every selected one) |
| `d` | Move the thread to the trash (or every selected one) |
| `m` | Set the thread as read (mark read; or every selected one) |
| `M` | Set the thread as unread (mark unread; or every selected one) |
| `*` | Toggle the star on the thread |
| `L` | Add or remove a label (fzf picker) |
| `x` | Select the thread for e / d / m / M |
| `i` | Start typing a chat message right in the preview (Ctrl+t sends it later) |
| `Enter` / `2` (chat) | Pick messages in the chat with j/k (r replies to one, y copies it, v saves its files) |
| `t` | Move a scheduled send to another time (in Scheduled; Enter edits it) |
| `n` | Rename the chat (spaces and groups on Google Chat; a DM only here) |
| `N` | Start a conversation (opens the one you have with them, earlier messages and all) |
| `v` | Save the attachments to ~/Downloads/sandesh (picker when several) |
| `V` | Open the attachments (saved to ~/Downloads/sandesh first) |
| `U` | Send a file into this chat space (upload; file picker with preview) |
| `z` | Cancel the last change, or a send in its undo window (undo) |
| `A` | Show the sends an agent is waiting on you to approve |
| `R` | Reload mail and chat from Google now (sync) |
| `b` | Open the thread or space in the browser |
| `y` | Copy the link to the thread or space |

**Claude**

| Key | Does |
|---|---|
| `@` | Start a question to Claude, with this thread or space as context (ask) |
| `S` | Show Claude's summary of the thread or the space's recent messages |
| `D` | Create a reply draft with Claude, then edit it in your editor |
| `T` | Show today's digest: what came in and what needs you |

**Other**

| Key | Does |
|---|---|
| `?` | Show every key |
| `q` / `Esc` | Go back, then quit |

**Keys** (`?`): a window in the middle of the screen with every key,
grouped, and a filter: type `send` and only the keys that send stay.

**Picking files** (`Ctrl+o` in a mail, `+` when reviewing, `U` in chat): a
window listing the files under the folder you're in (`Ctrl+r` switches to
home or Downloads, `Ctrl+h` shows hidden ones). Type to narrow it down; the
right side previews the highlighted file: text with line numbers, an
image's size (the picture itself with `chafa` installed), a PDF's first
page (with `pdftotext`). `Tab` picks several, `Enter` adds them.

**Replying to a message in chat**: `Enter` (or `2`) on a chat moves into
the conversation; `j` / `k` now pick a message (highlighted) instead of
scrolling. `r` replies to the picked one: the message box shows "↩ replying
to …", and the reply quotes it the way Google Chat's own reply does, right in
the conversation. From the list, `r` replies
to the latest message from someone else. Replies others send that quote a
message show the quote above them (`❝ You: …`).

**Starting a conversation** (`N` in chat): type a name and pick the
person (people from chat, contacts from mail, and your Workspace
directory); `Tab` adds another for a group. With two or more, Enter first
asks for a name: with one the group is a named space everyone sees, without
one a group chat (name it later with `n`). If your sign-in predates the
permission starting chats needs, the window says so and Enter signs you in
again in the browser, then carries on. sandesh opens the DM you
already have with them, fetching the earlier messages if this machine
hasn't seen them, or starts a new one, and puts you in the message box.
The chat search (`/`) finds conversations and people by name too, not just
words in messages: Enter on a person opens your conversation with them.
Claude can do the same: `chat_send` takes `to` (email addresses) instead of
a space.

**Renaming a chat** (`n`): a space is renamed on Google Chat, so everyone
sees the new name; a group chat gets its name on Google Chat too (that makes
it a space, with the same people and messages) (Google lets only its managers do it; sandesh asks for the
permission when you sign in, so an older sign-in needs `sandesh auth add
<account>` once). DMs and unnamed group chats can't be named on Google;
the name you give them stays on this machine. An empty name goes back to
the default.

**Searching** (`/` or `s`): a floating window that searches where you are.
In a mail section it looks in that section (Today: today's mail, Sent: what
you sent); in chat, the space you're on. `Tab` widens it to all mail or
every chat. Results from this machine show a moment after you stop typing,
matches highlighted, with a preview; then Gmail's own search adds what isn't
here yet (☁). Gmail syntax works: `from:priya has:attachment
newer_than:7d`. Nothing waits on it: you can keep typing, move or close the
window while it looks. Enter opens a result (a chat at that message).

| Key | Search |
|---|---|
| `Up` / `Down` | Move between the results (Ctrl+p / Ctrl+n too) |
| `Enter` | Open the result (mail results stay listed as a search section) |
| `Tab` | Switch where it looks: this section or all mail, this chat or every chat |
| `Ctrl+u` | Clear the search |
| `Esc` | Close the search window |

**Writing a mail** (`c`, `r`, `a`, `f`, or `D`): a new mail takes the whole
screen; a reply keeps the message you're answering on the left (PgUp/PgDn
scrolls it) and your reply on the right. Type a name or part of an address
in To and your contacts appear, best first (people you write to most, then
people who write to you, and colleagues from your Google Workspace
directory, which needs the People API enabled in your Cloud project); the
typed letters are highlighted. Recipients become pills; a peach one says
*first time* for someone you've never written to. Cc and Bcc stay hidden
until `Ctrl+b` (a reply-all shows them). Your Gmail signature is added under
the body (`Ctrl+g` leaves it out), a reply's original stays folded under it,
and the draft is saved on disk every few seconds: if the popup closes, the
same `c` or `r` picks it up again. Prefer your editor? `Ctrl+e` takes the
whole mail there and back, or set `compose = "editor"`.

| Key | Compose |
|---|---|
| `Tab` / `Shift+Tab` | Move to the next / previous field (Tab adds what you typed first) |
| `Up` / `Down` | Pick a contact from the suggestions |
| `Enter` / `,` | Add the picked contact or typed address, and stay for more |
| `Backspace` | Remove the last recipient (in an empty To, Cc or Bcc) |
| `Ctrl+s` | Send the mail (after the undo window) |
| `Ctrl+t` | Send the mail later (pick or type when: tomorrow 9am, fri 6pm, in 2h) |
| `Ctrl+d` | Save the mail as a Gmail draft and close it |
| `Ctrl+o` | Add files to the mail (file picker with preview; x removes one in Files) |
| `Ctrl+e` | Edit the whole mail in your editor, then come back |
| `Ctrl+q` | Toggle the quoted text under a reply or forward |
| `Left` / `Right` | Select a recipient pill (in an empty To, Cc or Bcc; Backspace removes it) |
| `Ctrl+b` | Toggle the Cc and Bcc fields |
| `Ctrl+g` | Toggle your Gmail signature under the mail |
| `Ctrl+f` | Switch the account the mail is sent from |
| `PgUp` / `PgDn` | Scroll the message you're replying to |
| `Esc` | Cancel the mail (twice once something is written) |

**Reviewing your message** (chat written in the editor, or mail with
`compose = "editor"`) and **approving an agent's**
(`A`, or the popup):

| Key | Review | Approve |
|---|---|---|
| `y` | Send the message you wrote (after the undo window) | Send the message an agent wrote (after the undo window) |
| `t` | Send the message later (pick or type when) | |
| `e` | Edit the message again in your editor | Edit the agent's message in your editor first |
| `+` | Add a file to the message (attach; file picker with preview) | |
| `-` | Remove the last file added to the message | |
| `d` | Save the message as a Gmail draft instead of sending | |
| `n` | Delete the message you wrote (discard) | Cancel the agent's message (decline; nothing is sent) |
| `q` | | Leave the agent's message waiting and go back |

## Sending later

`Ctrl+t` in a mail, `t` on the review screen, or `Ctrl+t` while typing a
chat message (`i`) asks *when*: pick one (in an hour, this evening,
tomorrow morning or afternoon, Monday morning) or type it the way you'd say
it: `tomorrow 9am`, `fri 6pm`, `in 2h`, `3 nov 10:30`, `03/11 9am`,
`2026-12-01 18:00`. It shows what it understood before you confirm. The
message waits in sandesh's outbox and the background service sends it then
(Gmail's API has no scheduled send of its own), so this computer must be on
at that time: if it wasn't, the message goes out as soon as it is, and a
notification says it went out late. Scheduled mail is listed in its own
**Scheduled** tab (next to Sent) and scheduled chat messages in the chat's
**Scheduled** tab, soonest first, with their time: `t` moves one, Enter
edits it, `z` cancels it. From the shell: `--at "tomorrow 9am"` on `mail send` and `chat send`;
`sandesh outbox reschedule <id> <when>`. Agents can schedule too
(`send_at`), still after your approval.

## Sent mail

The **Sent** section (next to Starred) lists mail you sent with who it went
to (`→ Priya, Arjun`) and whether they answered: `↩ Priya replied 09:58` or
`no reply yet · 2d`. Mail still in the outbox sits on top: `sending in 7s`
(`z` cancels), an agent's waiting for your OK (`A`), or `failed` in red (`R`
sends it again). Scheduled mail has its own tab, **Scheduled**.

## Older mail

The first sync is quick: the last `initial_days` (30) of mail, at most
`max_threads`. Older mail stays on Gmail until you want it: press `j` at the
end of a list and the next 10 older threads of that section come in (`+` /
`-` make it 25, 50 or 100; `older_page` sets the default), or search (`/`)
and Gmail finds it wherever it is. Want everything here anyway? `keep =
"all"` (or `"1y"`, `"6m"`) downloads older mail in the background, ~150
threads a minute, with a progress bar in the top bar.

## Files

- **Download**: `v` saves a mail's attachments or a chat's files to
  `~/Downloads/sandesh` (`SANDESH_DOWNLOADS` moves it); with several, an fzf
  picker opens (Tab picks more, Ctrl+a all). `V` saves and opens them. It
  never overwrites: a second copy is `name (2).ext`. Chat files kept in
  Drive open in the browser instead (`b`). From the shell: `sandesh mail
  save THREAD [N|all]`, `sandesh chat save MESSAGE [N|all]`.
- **Upload**: in the mail editor, `Attach: ~/report.pdf, ~/notes.txt`; or `+`
  on the review screen to pick files. In chat, `U` picks files to post in the
  space. From the shell: `--attach FILE` (repeatable) on `mail send` and
  `chat send`. Up to 25 MB per message.
- What is attached is copied when you queue the send (privately, 0600), so
  the file you reviewed is the file that goes out. Agents can attach files
  too, but those sends **always** wait for your approval, whatever
  `send_policy` says, and the approval shows each file and where it came
  from. Agents save downloads only inside the downloads folder.

## Claude and other agents

sandesh doesn't embed a model. It offers tools over MCP, and whichever
agent you use calls them. Add it once and every Claude Code session can
use it:

```sh
claude mcp add --scope user sandesh -- sandesh mcp
```

Then ask in plain words:

```
you     read today's mails
claude  → digest → mail_read
        3 new. Priya needs a yes/no on Thursday's release by end of day.
        Arjun asked for a review on #412. Staging DB credentials rotate at 18:00.

you     tell priya we're good for thursday, cc arjun
claude  → contacts_find → mail_send
        (a popup shows the mail; you press y)
        Sent.
```

| Tool | Does | |
|---|---|---|
| `mail_list` | Threads in a section (default Inbox), `today`, `unread` | reads |
| `mail_search` | Gmail search syntax, or the local copy offline | reads |
| `mail_read` | A whole thread as text; attachments listed, not opened | reads |
| `mail_modify` | Archive, unarchive, trash, untrash, mark read/unread, star | changes, undoable |
| `mail_draft` | Save a Gmail draft (replies fill in recipients and quoting) | changes |
| `mail_send` | Send a mail or a reply | **asks you** |
| `chat_spaces` | Spaces and DMs with unread counts | reads |
| `chat_read` | A space's recent messages | reads |
| `chat_send` | Post in a space, DM or thread | **asks you** |
| `contacts_find` | A name to an address you've written to | reads |
| `digest` | Today's mail and unread chat, ready to summarise | reads |

The `@`, `S`, `D` and `T` keys in the screen run `claude -p` (your
`ai_command`) with the same server, so they use your Claude login. No API
key. Any other MCP client works the same way: the server is `sandesh mcp`
on stdio.

## Sending safely

Mail is outward-facing and hard to take back, and the mail an agent reads
is written by strangers. So:

- **Agents draft; you send.** An agent's `mail_send` or `chat_send` waits
  for you. Inside tmux a popup opens on the pane the agent runs in, with
  the exact recipients and text: `y` sends, `e` edits it in your editor
  first, `n` declines. Outside tmux you get a notification; answer with
  `sandesh approve` or `A` in the screen. The agent is told what happened
  (sent, declined, cancelled) and nothing else.
- **Every mail waits** `undo_seconds` (10) in the outbox, yours included,
  so `z` (or `sandesh outbox cancel <id>`, or Ctrl+c on the command line)
  stops it. Chat messages go out at once (`chat_undo_seconds`, default 0)
  and show in the conversation straight away; agents' chat messages still
  wait for your approval.
- **Mail is data, not instructions.** Everything the tools return from
  mail or chat is wrapped in an `<untrusted>` block, and the server tells
  the agent not to follow instructions found there.
- **A recipient you've never written to always asks**, whatever the
  policy. `send_policy` in the config: `confirm` (the default: every agent
  send asks), `known-contacts` (replies to people you've written to go
  straight out), `allow` (anything to people you've written to goes
  straight out). Chat messages from agents always ask.
- **A log of every agent action** in `~/.local/state/sandesh/agent.log`.

## Command line

Every command has `--help`; the read commands take `--json`.

| Command | Does |
|---|---|
| `sandesh` | The screen |
| `sandesh mail list [--today] [--unread] [--section S]` | Threads (`ls`) |
| `sandesh mail read <thread> [--mark-read]` | A whole thread as text |
| `sandesh mail search <query> [--local]` | Gmail search syntax; `--local` searches the local copy |
| `sandesh mail send --to A --subject S [--reply THREAD [--all]]` | Send; the body from stdin or `$EDITOR`; waits the undo window (Ctrl+c cancels) |
| `sandesh mail archive\|unarchive\|trash\|untrash\|mark-read\|unread\|star\|unstar <thread>...` | Change threads |
| `sandesh chat list [--unread]` | Spaces and DMs |
| `sandesh chat read <space> [--mark-read]` | A space's recent messages (`<space>` is its id or part of its name) |
| `sandesh chat send <space> <text> [--thread T]` | Post in a space or DM |
| `sandesh status [--json\|--tmux]` | Unread counts |
| `sandesh sync` | Sync every account now |
| `sandesh outbox` / `outbox cancel <id>` | Sends of the last week; stop one in its undo window |
| `sandesh approve [id]` | Answer an agent's send that waits for you |
| `sandesh auth setup <client.json>` | Install your OAuth client |
| `sandesh auth add <name> [--email E] [--no-chat]` | Sign in a Google account |
| `sandesh auth list` / `auth remove <name>` | The accounts |
| `sandesh mcp` | The MCP server (stdio) |
| `sandesh daemon` | The background sync, in the foreground |
| `sandesh service install\|remove\|status` | The background sync as a systemd user unit |
| `sandesh config` | Print an example config file |
| `sandesh demo` | The screen on example mail, with a fake Google inside the process |
| `sandesh doctor` | Check the setup |

Options: `--account NAME` on most commands (default: the first account),
`-h`, `-V`.

## Using it inside tmux

sandesh is a command first; the tmux plugin adds a key and a status
segment. With [TPM](https://github.com/tmux-plugins/tpm):

```tmux
set -g @plugin 'Theyashsawarkar/sandesh'
set -g status-right '#{sandesh_status} %H:%M'   # 󰇮 3  󰭹 2
```

`prefix e` opens sandesh in a popup. If `sandesh` isn't on your `PATH`, the
plugin builds its own copy with Go the first time.

| Option | Default | |
|---|---|---|
| `@sandesh-key` | `e` | Key after the prefix; `none` binds nothing |
| `@sandesh-width` / `@sandesh-height` | `96%` / `94%` | Popup size |
| `@sandesh-title` | ` sandesh ` | Popup title |
| `@sandesh-launcher` | | Your own popup command, run with display-popup's arguments appended (e.g. `"~/.tmux/scripts/popup.sh '#{client_name}'"`) |

The key carries a `bind -N` note, and `scripts/keybindings` lists the keys
inside the screen as `# keybind:` lines, so both show up in tmux's
`prefix ?` and in Vayu's keybinding search.

## Configuration

`~/.config/sandesh/config.toml`; every line is optional. `sandesh config`
prints this:

```toml
send_policy   = "confirm"   # confirm | known-contacts | allow (see Sending safely)
undo_seconds  = 10          # every mail waits this long; z cancels
chat_undo_seconds = 0       # the same for chat (default: sent at once)
mail_interval = "60s"       # how often the background sync checks Gmail
chat_interval = "30s"       # and Google Chat
initial_days  = 30          # how far back the first sync goes
max_threads   = 150         # at most this many threads on the first sync (older mail: search)
notify        = true        # desktop notifications for new mail and DMs
ai_command    = ["claude", "-p"]   # what @, S, D and T run
# editor      = "nvim"             # default: $VISUAL, $EDITOR, else the first one installed
# compose     = "editor"           # write mail in the editor instead of the compose screen
# include     = "~/.local/state/varna/sandesh.toml"   # a theme file; its [colors] win

# Sections are the tabs on top. label: a Gmail label id (INBOX, STARRED,
# UNREAD, or a label's id), query: a Gmail search run on the server,
# today: only threads with mail from today.
[[section]]
name  = "Inbox"
label = "INBOX"

[[section]]
name  = "GitHub"
query = "from:notifications@github.com newer_than:14d"

[colors]           # Catppuccin Mocha by default
accent  = "#74c7ec"
accent2 = "#cba6f7"
```

Colours follow [varna](https://github.com/Theyashsawarkar/varna)'s desktop
theme (`~/.local/state/varna/palette.json`) and change live when it does;
anything set under `[colors]` wins. Colour names: `accent`, `accent2`, `text`, `subtle`, `muted`, `surface`, `border`,
`base`, `mantle`, `green`, `yellow`, `red`, `blue`, `peach`. With
[varna](https://github.com/Theyashsawarkar/varna), point `include` at the
file it writes and the screen follows your theme.

## Files and environment

| Path | |
|---|---|
| `~/.config/sandesh/config.toml` | Settings |
| `~/.config/sandesh/client.json` | Your OAuth client (`auth setup`) |
| keyring, service `sandesh` | Sign-in tokens (`secret-tool search service sandesh`); without a keyring, `~/.config/sandesh/tokens/` (mode 600) |
| `~/.local/share/sandesh/cache.db` | The local copy (SQLite) |
| `~/.local/state/sandesh/agent.log` | What agents did |
| `~/.local/state/sandesh/sections.json` | Searches saved with `W` |
| `~/.config/systemd/user/sandesh.service` | The background sync (`service install`) |

`SANDESH_HOME` puts all of sandesh's files under one folder (tests and
`demo` use it). `SANDESH_GOOGLE_URL` sends every API call to a fake Google
(the test suite's). The usual `XDG_*` variables are respected.

## How it works

- **One local copy, many front ends.** `sandesh daemon` (the systemd user
  unit) follows Gmail's change feed (`history.list`) every minute and
  checks Google Chat spaces with new activity every 30 seconds, storing
  everything in SQLite with full-text search. The screen, the CLI and the
  MCP server read that copy, so they open instantly; the screen syncs on
  its own only when the daemon isn't running, and right after you change
  something. A lock lets only one process sync an account at a time, and
  Gmail requests are paced well under Google's per-minute limit.
- **One send path.** Every send, from you or an agent, becomes an outbox
  row: `approval` (an agent's, waiting for you) → `queued` (the undo
  window) → claimed by whichever process gets there first → `sent` or
  `failed`. Claiming is one atomic update, so a message is sent once even
  with the daemon, the screen and the MCP server all running.
- **Google's APIs, not IMAP.** Chat has no IMAP, so sign-in is needed
  anyway; the Gmail API also gives real labels, threads, drafts that show
  up on your phone, and Gmail's search syntax. Sign-in is the loopback
  OAuth flow with PKCE. Scopes: `gmail.modify`, Chat's spaces, messages,
  memberships and read state, `directory.readonly` (names for chat
  senders) and `profile`.
- **Written in Go**: one static binary, Bubble Tea for the screen, the
  official MCP Go SDK, pure-Go SQLite.

Design decisions are recorded in [docs/adr](docs/adr).

## Troubleshooting

- **`sandesh doctor`** checks the OAuth client, each account's sign-in and
  last sync, the background service, the tmux key and the Claude Code MCP
  server.
- **"Google said no: … has not been used in project"**: enable that API
  (Gmail, Chat or People) in the Cloud console for your project.
- **Chat fails, mail works**: Google Chat's API needs a Workspace account
  and the Chat API's Configuration page filled in. For a personal Gmail:
  `sandesh auth remove personal && sandesh auth add personal --no-chat`.
- **"access blocked" or "admin_policy_enforced"** on sign-in: your
  Workspace admin has to allow the app (Admin console → Security → API
  controls).
- **Signed out every week**: the consent screen is External and still in
  Testing; publish it.
- **Logs**: `journalctl --user -u sandesh -f`.

## Contributing

`make check` runs what CI runs: `go vet`, `gofmt`, ShellCheck, the tests
(against a fake Google, `internal/fake`) and the website check. Keys live
in `internal/tui/keys.go`; after changing one, run
`go test ./internal/tui -update` (it rewrites `scripts/keybindings`) and
update the tables above. Every change gets a CHANGELOG entry.

## Roadmap

- Attachments: save and open.
- Gmail and Chat push (Pub/Sub, Workspace Events) instead of polling.
- A Vyom glance panel and a waybar module reading `sandesh status --json`.
- Calendar invites: accept and decline from the preview.

## License

MIT
