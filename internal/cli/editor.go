package cli

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/Theyashsawarkar/sandesh/internal/app"
	"github.com/Theyashsawarkar/sandesh/internal/store"
)

const marker = "-- Write below this line (Attach: takes file paths, comma separated). Save and quit to review; an empty message is not sent. --"

// MailTemplate is the file the editor opens: a header block, then the body.
func MailTemplate(out store.Outgoing, quoted string) string {
	var b strings.Builder
	var files []string
	for _, f := range out.Files {
		files = append(files, f.Source)
	}
	fmt.Fprintf(&b, "To: %s\nCc: %s\nBcc: %s\nSubject: %s\nAttach: %s\n%s\n\n", strings.Join(out.To, ", "), strings.Join(out.Cc, ", "),
		strings.Join(out.Bcc, ", "), out.Subject, strings.Join(files, ", "), marker)
	if out.Body != "" {
		b.WriteString(strings.TrimRight(out.Body, "\n") + "\n")
	}
	if quoted != "" {
		b.WriteString("\n\n" + quoted + "\n")
	}
	return b.String()
}

// ParseMailTemplate reads the edited file back. ok is false when the body
// is empty (or only the quote is left).
func ParseMailTemplate(text string, base store.Outgoing) (out store.Outgoing, ok bool) {
	out = base
	head, body, found := strings.Cut(text, marker)
	if !found {
		head, body = "", text
	}
	for _, line := range strings.Split(head, "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		switch strings.ToLower(strings.TrimSpace(k)) {
		case "to":
			out.To = splitAddrs(v)
		case "cc":
			out.Cc = splitAddrs(v)
		case "bcc":
			out.Bcc = splitAddrs(v)
		case "subject":
			out.Subject = v
		case "attach", "attachments", "files":
			// files by path, comma separated; ones already attached keep
			// their copy
			out.Files = nil
			for _, p := range strings.Split(v, ",") {
				if p = strings.TrimSpace(p); p == "" {
					continue
				}
				f := store.File{Source: p}
				for _, old := range base.Files {
					if old.Source == p {
						f = old
					}
				}
				out.Files = append(out.Files, f)
			}
		}
	}
	out.Body = strings.Trim(body, "\n")
	// only quoted lines left: nothing was written
	written := false
	for _, l := range strings.Split(out.Body, "\n") {
		l = strings.TrimSpace(l)
		if l != "" && !strings.HasPrefix(l, ">") && !(strings.HasPrefix(l, "On ") && strings.HasSuffix(l, "wrote:")) {
			written = true
			break
		}
	}
	return out, written || len(out.Files) > 0
}

// ChatTemplate / ParseChatTemplate: a chat message is just text.
const chatMarker = "-- Write your message below. Save and quit to send; empty sends nothing. --"

func ChatTemplate(title, text string) string {
	return "# " + title + "\n" + chatMarker + "\n" + text
}

func ParseChatTemplate(text string) (string, bool) {
	if _, body, ok := strings.Cut(text, chatMarker); ok {
		text = body
	}
	text = strings.TrimSpace(text)
	return text, text != ""
}

// EditorCmd opens path in the configured editor.
func EditorCmd(a *app.App, path string) *exec.Cmd {
	ed := a.Config.Editor
	for _, k := range []string{"VISUAL", "EDITOR"} {
		if ed == "" {
			ed = os.Getenv(k)
		}
	}
	if ed == "" {
		ed = "vi"
	}
	// +/^-- puts the cursor under the header block in vi-likes
	return exec.Command("sh", "-c", ed+` "$1"`, "sh", path)
}

// TempFile writes text to a private temp file named for syntax colouring.
func TempFile(a *app.App, ext, text string) (string, error) {
	f, err := os.CreateTemp(a.Paths.State, "draft-*."+ext)
	if err != nil {
		return "", err
	}
	defer f.Close()
	_, err = f.WriteString(text)
	return f.Name(), err
}

// EditMail runs the editor on a mail in the foreground (CLI use).
func EditMail(a *app.App, out store.Outgoing, quoted string) (*store.Outgoing, error) {
	path, err := TempFile(a, "eml", MailTemplate(out, quoted))
	if err != nil {
		return nil, err
	}
	defer os.Remove(path)
	cmd := EditorCmd(a, path)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("editor: %w", err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	edited, ok := ParseMailTemplate(string(b), out)
	if !ok {
		return nil, nil
	}
	return &edited, nil
}
