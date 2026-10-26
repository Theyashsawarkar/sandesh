// Package mime turns Gmail's message payloads into plain text and builds
// the RFC 5322 messages sandesh sends.
package mime

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"html"
	stdmime "mime"
	"net/mail"
	"regexp"
	"strings"
	"time"
)

// Part mirrors Gmail's MessagePart.
type Part struct {
	PartID   string   `json:"partId"`
	MimeType string   `json:"mimeType"`
	Filename string   `json:"filename"`
	Headers  []Header `json:"headers"`
	Body     struct {
		AttachmentID string `json:"attachmentId"`
		Size         int    `json:"size"`
		Data         string `json:"data"`
	} `json:"body"`
	Parts []Part `json:"parts"`
}

type Header struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

func (p Part) Header(name string) string {
	for _, h := range p.Headers {
		if strings.EqualFold(h.Name, name) {
			return h.Value
		}
	}
	return ""
}

type Attachment struct {
	Name string
	Type string
	Size int
}

// Text returns the readable body: text/plain when there is one, else
// text/html turned into text. Attachments are listed, never decoded.
func Text(p Part) (string, []Attachment) {
	text, _, atts := Bodies(p)
	return text, atts
}

// Bodies returns the readable text (as Text does), the raw HTML part when
// there is one (for the formatted preview), and the attachments.
func Bodies(p Part) (text, htmlBody string, atts []Attachment) {
	var plain, htm []string
	var htmlOnly []string // html alternatives of plain parts: kept for the preview
	var walk func(Part)
	walk = func(p Part) {
		if p.Filename != "" && p.Body.AttachmentID != "" {
			atts = append(atts, Attachment{Name: p.Filename, Type: p.MimeType, Size: p.Body.Size})
			return
		}
		mt := strings.ToLower(p.MimeType)
		switch {
		case mt == "text/plain":
			plain = append(plain, decode(p.Body.Data))
		case mt == "text/html":
			htm = append(htm, decode(p.Body.Data))
		case strings.HasPrefix(mt, "multipart/"):
			if mt == "multipart/alternative" {
				// prefer the plain alternative, keep html as a fallback
				var hasPlain bool
				for _, c := range p.Parts {
					if strings.EqualFold(c.MimeType, "text/plain") {
						hasPlain = true
					}
				}
				for _, c := range p.Parts {
					if hasPlain && strings.EqualFold(c.MimeType, "text/html") {
						htmlOnly = append(htmlOnly, decode(c.Body.Data))
						continue
					}
					walk(c)
				}
				return
			}
			for _, c := range p.Parts {
				walk(c)
			}
		}
	}
	walk(p)
	if len(plain) > 0 {
		text = strings.Join(plain, "\n")
	} else if len(htm) > 0 {
		text = HTMLToText(strings.Join(htm, "\n"))
	}
	for i := range atts {
		atts[i].Name = Clean(atts[i].Name)
	}
	return Clean(tidy(text)), strings.Join(append(htm, htmlOnly...), "\n"), atts
}

func decode(data string) string {
	if data == "" {
		return ""
	}
	b, err := base64.URLEncoding.DecodeString(data)
	if err != nil {
		b, err = base64.RawURLEncoding.DecodeString(data)
		if err != nil {
			return ""
		}
	}
	return string(b)
}

var (
	reDrop     = regexp.MustCompile(`(?is)<(script|style|head|title)[^>]*>.*?</(script|style|head|title)>`)
	reBreak    = regexp.MustCompile(`(?i)<br\s*/?>`)
	reBlockEnd = regexp.MustCompile(`(?i)</(p|div|tr|h[1-6]|li|table|blockquote)>`)
	reLi       = regexp.MustCompile(`(?i)<li[^>]*>`)
	reLink     = regexp.MustCompile(`(?is)<a\s[^>]*href=["']([^"']+)["'][^>]*>(.*?)</a>`)
	reTag      = regexp.MustCompile(`(?s)<[^>]+>`)
	reSpaces   = regexp.MustCompile(`[ \t\x{a0}]+`)
	reBlank    = regexp.MustCompile(`\n{3,}`)
)

// HTMLToText is a small, forgiving html-to-text: enough for reading mail
// in a terminal, not a renderer.
func HTMLToText(s string) string {
	s = reDrop.ReplaceAllString(s, "")
	s = reLink.ReplaceAllStringFunc(s, func(m string) string {
		sub := reLink.FindStringSubmatch(m)
		text := strings.TrimSpace(reTag.ReplaceAllString(sub[2], ""))
		href := sub[1]
		if text == "" || strings.EqualFold(html.UnescapeString(text), href) || strings.HasPrefix(href, "mailto:") {
			if text == "" {
				return ""
			}
			return text
		}
		return text + " (" + href + ")"
	})
	s = reBreak.ReplaceAllString(s, "\n")
	s = reLi.ReplaceAllString(s, "\n• ")
	s = reBlockEnd.ReplaceAllString(s, "\n\n")
	s = reTag.ReplaceAllString(s, "")
	s = html.UnescapeString(s)
	return s
}

func tidy(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(reSpaces.ReplaceAllString(l, " "), " ")
	}
	s = strings.Join(lines, "\n")
	s = reBlank.ReplaceAllString(s, "\n\n")
	return strings.TrimSpace(s)
}

// Clean removes what must never reach a terminal from text written by
// other people: escape sequences and other control characters (they can
// rewrite the screen, set the clipboard or the window title) and bidi
// overrides (they can make text read differently than it is). Newlines
// and tabs stay.
func Clean(s string) string {
	if s == "" {
		return s
	}
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\t':
			return r
		case r < 0x20 || r == 0x7f || r >= 0x80 && r <= 0x9f:
			return -1
		case r >= 0x202a && r <= 0x202e || r >= 0x2066 && r <= 0x2069:
			return -1
		}
		return r
	}, s)
}

// DecodeHeader decodes RFC 2047 words (=?utf-8?q?...?=).
func DecodeHeader(s string) string {
	d, err := new(stdmime.WordDecoder).DecodeHeader(s)
	if err != nil {
		return Clean(s)
	}
	return Clean(d)
}

// ParseAddress returns name and address; a bare address has no name.
func ParseAddress(s string) (name, addr string) {
	a, err := mail.ParseAddress(s)
	if err != nil {
		s = strings.TrimSpace(s)
		if i := strings.LastIndex(s, "<"); i >= 0 && strings.HasSuffix(s, ">") {
			return strings.Trim(strings.TrimSpace(s[:i]), `"`), s[i+1 : len(s)-1]
		}
		return "", s
	}
	return a.Name, a.Address
}

// ParseList splits an address list ("A <a@x>, b@y") into addresses.
func ParseList(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	list, err := mail.ParseAddressList(s)
	if err != nil {
		var out []string
		for _, p := range strings.Split(s, ",") {
			if _, a := ParseAddress(p); a != "" {
				out = append(out, a)
			}
		}
		return out
	}
	out := make([]string, 0, len(list))
	for _, a := range list {
		out = append(out, a.Address)
	}
	return out
}

// Outgoing message fields.
type Draft struct {
	From       string
	To, Cc     []string
	Bcc        []string
	Subject    string
	Body       string
	InReplyTo  string
	References string
}

// Build returns an RFC 5322 message, base64url-encoded for Gmail's raw
// field.
func Build(d Draft) (string, error) {
	if len(d.To)+len(d.Cc)+len(d.Bcc) == 0 {
		return "", fmt.Errorf("no recipients")
	}
	for _, list := range [][]string{d.To, d.Cc, d.Bcc} {
		for _, a := range list {
			if _, err := mail.ParseAddress(a); err != nil {
				return "", fmt.Errorf("bad address %q", a)
			}
		}
	}
	var b bytes.Buffer
	h := func(k, v string) {
		if v != "" {
			fmt.Fprintf(&b, "%s: %s\r\n", k, v)
		}
	}
	h("From", d.From)
	h("To", strings.Join(d.To, ", "))
	h("Cc", strings.Join(d.Cc, ", "))
	h("Bcc", strings.Join(d.Bcc, ", "))
	h("Subject", stdmime.QEncoding.Encode("utf-8", d.Subject))
	h("Date", time.Now().Format(time.RFC1123Z))
	h("In-Reply-To", d.InReplyTo)
	h("References", d.References)
	h("MIME-Version", "1.0")
	h("Content-Type", `text/plain; charset="UTF-8"`)
	h("Content-Transfer-Encoding", "base64")
	b.WriteString("\r\n")
	enc := base64.StdEncoding.EncodeToString([]byte(strings.ReplaceAll(d.Body, "\n", "\r\n")))
	for len(enc) > 76 {
		b.WriteString(enc[:76] + "\r\n")
		enc = enc[76:]
	}
	b.WriteString(enc + "\r\n")
	return base64.URLEncoding.EncodeToString(b.Bytes()), nil
}

// ReplySubject adds "Re: " unless it is already there.
func ReplySubject(s string) string {
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(s)), "re:") {
		return s
	}
	return "Re: " + s
}

func ForwardSubject(s string) string {
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(s)), "fwd:") {
		return s
	}
	return "Fwd: " + s
}

// Quote prefixes each line with "> ".
func Quote(body string) string {
	lines := strings.Split(body, "\n")
	for i, l := range lines {
		if l == "" {
			lines[i] = ">"
		} else {
			lines[i] = "> " + l
		}
	}
	return strings.Join(lines, "\n")
}
