package tui

import (
	"fmt"
	"strings"
)

// Key is one row of the key sheet. The same table feeds the ? sheet, the
// README and scripts/keybindings (Vayu's keybinding search reads that
// file; a test keeps it in sync).
type Key struct {
	Group string
	Keys  string
	Desc  string
}

var Keys = []Key{
	{"Move", "j / k", "Move to the next / previous thread or space (j at the end loads older mail)"},
	{"Move", "+ / -", "Set how many older mails j at the end loads (10, 25, 50, 100)"},
	{"Move", "g / G", "Jump to the first / last"},
	{"Move", "h / l", "Switch to the previous / next section"},
	{"Move", "Tab", "Switch between mail and chat"},
	{"Move", "1 / 2", "Focus the list / the preview"},
	{"Move", "Enter", "Open the thread or space (marks it read)"},
	{"Move", "J / K", "Scroll the preview down / up"},
	{"Move", "E", "Toggle every message in the thread open / folded"},
	{"Move", "Q", "Toggle the quoted text in the thread"},
	{"Move", "o", "Open a link from the preview by its number"},
	{"Move", "Ctrl+d / Ctrl+u", "Scroll the preview half a page down / up"},
	{"Move", "/ or s", "Open the search window (searches where you are: this section, or this chat)"},
	{"Move", "W", "Save the current search as a section"},
	{"Move", "X", "Remove the current saved-search section"},
	{"Move", "u", "Switch account (all, then each one)"},
	{"Act", "c", "Create a new mail, or a new message in this space (compose)"},
	{"Act", "r", "Send a reply to the thread (in chat: in the latest thread)"},
	{"Act", "a", "Send a reply to everyone on the thread (reply all)"},
	{"Act", "f", "Send the thread on to someone else (forward)"},
	{"Act", "e", "Move the thread out of the inbox (archive; or every selected one)"},
	{"Act", "d", "Move the thread to the trash (or every selected one)"},
	{"Act", "m", "Set the thread as read (mark read; or every selected one)"},
	{"Act", "M", "Set the thread as unread (mark unread; or every selected one)"},
	{"Act", "*", "Toggle the star on the thread"},
	{"Act", "L", "Add or remove a label (fzf picker)"},
	{"Act", "x", "Select the thread for e / d / m / M"},
	{"Act", "i", "Start typing a chat message right in the preview"},
	{"Act", "n", "Rename the chat (a space on Google Chat; a DM or group only here)"},
	{"Act", "v", "Save the attachments to ~/Downloads/sandesh (picker when several)"},
	{"Act", "V", "Open the attachments (saved to ~/Downloads/sandesh first)"},
	{"Act", "U", "Send a file into this chat space (upload; file picker with preview)"},
	{"Act", "z", "Cancel the last change, or a send in its undo window (undo)"},
	{"Act", "A", "Show the sends an agent is waiting on you to approve"},
	{"Act", "R", "Reload mail and chat from Google now (sync)"},
	{"Act", "b", "Open the thread or space in the browser"},
	{"Act", "y", "Copy the link to the thread or space"},
	{"Claude", "@", "Start a question to Claude, with this thread or space as context (ask)"},
	{"Claude", "S", "Show Claude's summary of the thread or the space's recent messages"},
	{"Claude", "D", "Create a reply draft with Claude, then edit it in your editor"},
	{"Claude", "T", "Show today's digest: what came in and what needs you"},
	{"Other", "?", "Show every key"},
	{"Other", "q / Esc", "Go back, then quit"},
	{"Compose", "Tab / Shift+Tab", "Move to the next / previous field (Tab adds what you typed first)"},
	{"Compose", "Up / Down", "Pick a contact from the suggestions"},
	{"Compose", "Enter / ,", "Add the picked contact or typed address, and stay for more"},
	{"Compose", "Backspace", "Remove the last recipient (in an empty To, Cc or Bcc)"},
	{"Compose", "Ctrl+s", "Send the mail (after the undo window)"},
	{"Compose", "Ctrl+d", "Save the mail as a Gmail draft and close it"},
	{"Compose", "Ctrl+o", "Add files to the mail (file picker with preview; x removes one in Files)"},
	{"Compose", "Ctrl+e", "Edit the whole mail in your editor, then come back"},
	{"Compose", "Ctrl+q", "Toggle the quoted text under a reply or forward"},
	{"Compose", "Left / Right", "Select a recipient pill (in an empty To, Cc or Bcc; Backspace removes it)"},
	{"Compose", "Ctrl+b", "Toggle the Cc and Bcc fields"},
	{"Compose", "Ctrl+g", "Toggle your Gmail signature under the mail"},
	{"Compose", "Ctrl+f", "Switch the account the mail is sent from"},
	{"Compose", "PgUp / PgDn", "Scroll the message you're replying to"},
	{"Compose", "Esc", "Cancel the mail (twice once something is written)"},
	{"Search", "Up / Down", "Move between the results (Ctrl+p / Ctrl+n too)"},
	{"Search", "Enter", "Open the result (mail results stay listed as a search section)"},
	{"Search", "Tab", "Switch where it looks: this section or all mail, this chat or every chat"},
	{"Search", "Ctrl+u", "Clear the search"},
	{"Search", "Esc", "Close the search window"},
	{"Review", "y", "Send the message you wrote (after the undo window)"},
	{"Review", "e", "Edit the message again in your editor"},
	{"Review", "+", "Add a file to the message (attach; file picker with preview)"},
	{"Review", "-", "Remove the last file added to the message"},
	{"Review", "d", "Save the message as a Gmail draft instead of sending"},
	{"Review", "n", "Delete the message you wrote (discard)"},
	{"Approve", "y", "Send the message an agent wrote (after the undo window)"},
	{"Approve", "e", "Edit the agent's message in your editor first"},
	{"Approve", "n", "Cancel the agent's message (decline; nothing is sent)"},
	{"Approve", "q", "Leave the agent's message waiting and go back"},
	{"Setup", "Tab", "Switch to the next path completion (Enter on a folder opens it)"},
	{"Setup", "Enter", "Save the OAuth client file at this path, or start the Google sign-in"},
	{"Setup", "Ctrl+t", "Enter the OAuth client ID and secret by hand instead"},
	{"Setup", "Ctrl+o", "Open the Google Cloud console credentials page"},
	{"Setup", "Ctrl+g", "Show the steps for creating the OAuth client"},
	{"Setup", "Ctrl+d", "Start the demo on example mail instead"},
	{"Setup", "n", "Rename the account before signing in"},
	{"Setup", "c", "Pick a different OAuth client file"},
	{"Setup", "y", "Copy the sign-in link"},
}

// KeybindLines renders the table as Vayu `# keybind:` annotations.
func KeybindLines() string {
	var b strings.Builder
	b.WriteString("# Keys inside the sandesh screen, for Vayu's keybinding search\n")
	b.WriteString("# (keybind-search.py reads ~/.tmux/plugins/*/scripts). Generated from\n")
	b.WriteString("# internal/tui/keys.go by `go test ./internal/tui -update`; don't edit.\n")
	for _, k := range Keys {
		scope := "sandesh"
		switch k.Group {
		case "Compose":
			scope = "sandesh compose"
		case "Search":
			scope = "sandesh search"
		case "Review":
			scope = "sandesh review"
		case "Approve":
			scope = "sandesh approve"
		case "Setup":
			scope = "sandesh setup"
		}
		fmt.Fprintf(&b, "# keybind: Tmux/%s | %s | %s\n", scope, k.Keys, k.Desc)
	}
	return b.String()
}
