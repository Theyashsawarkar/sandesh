package mime

import (
	"encoding/base64"
	"io"
	stdmime "mime"
	"mime/multipart"
	"net/mail"
	"strings"
	"testing"
)

// parts returns the decoded text/plain and text/html bodies of a built message.
func parts(t *testing.T, raw string) (plain, htm string, files int) {
	t.Helper()
	b, err := base64.URLEncoding.DecodeString(raw)
	if err != nil {
		t.Fatal(err)
	}
	m, err := mail.ReadMessage(strings.NewReader(string(b)))
	if err != nil {
		t.Fatal(err)
	}
	var walk func(ct string, body io.Reader)
	walk = func(ct string, body io.Reader) {
		mt, params, _ := stdmime.ParseMediaType(ct)
		if strings.HasPrefix(mt, "multipart/") {
			r := multipart.NewReader(body, params["boundary"])
			for {
				p, err := r.NextPart()
				if err != nil {
					return
				}
				if p.FileName() != "" {
					files++
					continue
				}
				var r io.Reader = p
				if strings.EqualFold(p.Header.Get("Content-Transfer-Encoding"), "base64") {
					r = base64.NewDecoder(base64.StdEncoding, p)
				}
				walk(p.Header.Get("Content-Type"), r)
			}
		}
		data, _ := io.ReadAll(body)
		switch mt {
		case "text/plain":
			plain = string(data)
		case "text/html":
			htm = string(data)
		}
	}
	ct := m.Header.Get("Content-Type")
	var body io.Reader = m.Body
	if !strings.HasPrefix(ct, "multipart/") {
		body = base64.NewDecoder(base64.StdEncoding, m.Body)
	}
	walk(ct, body)
	return
}

func TestBuildHTML(t *testing.T) {
	d := Draft{To: []string{"a@x.com"}, Subject: "DSR", Body: "Time Tasks", HTML: "<table><tr><td>Time</td></tr></table>"}
	raw, err := Build(d)
	if err != nil {
		t.Fatal(err)
	}
	plain, htm, _ := parts(t, raw)
	if plain != "Time Tasks" || htm != d.HTML {
		t.Fatalf("plain=%q html=%q", plain, htm)
	}

	d.Files = []File{{Name: "a.txt", Type: "text/plain", Data: []byte("x")}}
	raw, err = Build(d)
	if err != nil {
		t.Fatal(err)
	}
	plain, htm, files := parts(t, raw)
	if plain != "Time Tasks" || htm != d.HTML || files != 1 {
		t.Fatalf("with a file: plain=%q html=%q files=%d", plain, htm, files)
	}
}

func TestQuoteHTML(t *testing.T) {
	got := QuoteHTML("<p>hi</p>", "On Mon, A wrote:\n> <b>x</b>\n")
	if !strings.HasPrefix(got, "<p>hi</p>") || !strings.Contains(got, "&gt; &lt;b&gt;x&lt;/b&gt;") || !strings.Contains(got, "<br>") {
		t.Fatalf("got %q", got)
	}
	if QuoteHTML("<p>hi</p>", "") != "<p>hi</p>" {
		t.Fatal("no quote should leave the body alone")
	}
}
