// Package fake is a small in-memory Google: just enough Gmail, Chat and
// People API for the tests and for `sandesh demo`. Point sandesh at it
// with SANDESH_GOOGLE_URL.
package fake

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	stdmail "net/mail"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Theyashsawarkar/sandesh/internal/mime"
)

type Message struct {
	ID, ThreadID string
	Labels       []string
	From, To, Cc string
	Subject      string
	Body         string
	HTML         string // when set, the message is multipart/alternative
	Date         time.Time
	HistoryID    int
}

type ChatMessage struct {
	Name, Space, Thread, Sender, Text string
	Created                           time.Time
}

type Space struct {
	Name, DisplayName, Type string
	Members                 []string
	LastRead                time.Time
}

type Server struct {
	mu       sync.Mutex
	Email    string
	Me       string // chat user, users/1
	history  int
	nextID   int
	Messages []*Message
	Drafts   map[string]string
	Spaces   []*Space
	Chat     []*ChatMessage
	People   map[string][2]string // users/N -> name, email
	Sent     []string             // raw messages sent, decoded
	Requests []string
}

func New() *Server {
	return &Server{Email: "you@example.com", Me: "users/1", history: 100, nextID: 1000, Drafts: map[string]string{},
		People: map[string][2]string{"users/1": {"You", "you@example.com"}}}
}

func (s *Server) id() string { s.nextID++; return fmt.Sprintf("%x", s.nextID) }

// AddMail adds a message; thread "" starts a new thread. Returns its id.
func (s *Server) AddMail(m Message) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if m.ID == "" {
		m.ID = s.id()
	}
	if m.ThreadID == "" {
		m.ThreadID = m.ID
	}
	if m.Date.IsZero() {
		m.Date = time.Now()
	}
	s.history++
	m.HistoryID = s.history
	s.Messages = append(s.Messages, &m)
	return m.ID
}

func (s *Server) AddSpace(sp Space) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Spaces = append(s.Spaces, &sp)
}

func (s *Server) AddChat(m ChatMessage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if m.Name == "" {
		m.Name = m.Space + "/messages/" + s.id()
	}
	if m.Thread == "" {
		m.Thread = m.Space + "/threads/" + s.id()
	}
	if m.Created.IsZero() {
		m.Created = time.Now()
	}
	s.Chat = append(s.Chat, &m)
}

func has(l []string, x string) bool {
	for _, y := range l {
		if y == x {
			return true
		}
	}
	return false
}

func b64(s string) string { return base64.URLEncoding.EncodeToString([]byte(s)) }

func (s *Server) gmailMessage(m *Message) map[string]any {
	headers := []map[string]string{{"name": "From", "value": m.From}, {"name": "To", "value": m.To}, {"name": "Subject", "value": m.Subject},
		{"name": "Message-ID", "value": "<" + m.ID + "@fake>"}}
	if m.Cc != "" {
		headers = append(headers, map[string]string{"name": "Cc", "value": m.Cc})
	}
	snippet := m.Body
	if len(snippet) > 90 {
		snippet = snippet[:90]
	}
	return map[string]any{"id": m.ID, "threadId": m.ThreadID, "labelIds": m.Labels, "snippet": strings.ReplaceAll(snippet, "\n", " "),
		"historyId": strconv.Itoa(m.HistoryID), "internalDate": strconv.FormatInt(m.Date.UnixMilli(), 10),
		"payload": payload(m, headers)}
}

func payload(m *Message, headers []map[string]string) map[string]any {
	plain := map[string]any{"mimeType": "text/plain", "body": map[string]any{"data": b64(m.Body), "size": len(m.Body)}}
	if m.HTML == "" {
		plain["headers"] = headers
		return plain
	}
	return map[string]any{"mimeType": "multipart/alternative", "headers": headers, "parts": []any{plain,
		map[string]any{"mimeType": "text/html", "body": map[string]any{"data": b64(m.HTML), "size": len(m.HTML)}}}}
}

func (s *Server) thread(id string) []*Message {
	var out []*Message
	for _, m := range s.Messages {
		if m.ThreadID == id {
			out = append(out, m)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Date.Before(out[j].Date) })
	return out
}

func write(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, code int, msg string) {
	w.WriteHeader(code)
	write(w, map[string]any{"error": map[string]any{"code": code, "message": msg}})
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Requests = append(s.Requests, r.Method+" "+r.URL.Path)
	p := r.URL.Path
	var body map[string]any
	if r.Body != nil {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
	}
	q := r.URL.Query()
	switch {
	case p == "/gmail/v1/users/me/profile":
		write(w, map[string]any{"emailAddress": s.Email, "historyId": strconv.Itoa(s.history)})
	case p == "/gmail/v1/users/me/labels":
		write(w, map[string]any{"labels": []map[string]string{{"id": "INBOX", "name": "INBOX", "type": "system"},
			{"id": "STARRED", "name": "STARRED", "type": "system"}, {"id": "Label_1", "name": "work", "type": "user"}}})
	case p == "/gmail/v1/users/me/threads":
		seen := map[string]bool{}
		var ids []string
		msgs := append([]*Message{}, s.Messages...)
		sort.Slice(msgs, func(i, j int) bool { return msgs[i].Date.After(msgs[j].Date) })
		query := q.Get("q")
		for _, m := range msgs {
			if seen[m.ThreadID] || has(m.Labels, "TRASH") {
				continue
			}
			if f, ok := strings.CutPrefix(query, "from:"); ok && !strings.Contains(strings.ToLower(m.From), strings.ToLower(strings.Fields(f)[0])) {
				continue
			}
			if query != "" && !strings.HasPrefix(query, "from:") && !strings.HasPrefix(query, "newer_than:") &&
				!strings.Contains(strings.ToLower(m.Subject+" "+m.Body), strings.ToLower(query)) {
				continue
			}
			seen[m.ThreadID] = true
			ids = append(ids, m.ThreadID)
		}
		var ts []map[string]string
		for _, id := range ids {
			ts = append(ts, map[string]string{"id": id})
		}
		write(w, map[string]any{"threads": ts})
	case strings.HasPrefix(p, "/gmail/v1/users/me/threads/"):
		rest := strings.TrimPrefix(p, "/gmail/v1/users/me/threads/")
		id, action, _ := strings.Cut(rest, "/")
		msgs := s.thread(id)
		if len(msgs) == 0 {
			fail(w, 404, "Requested entity was not found.")
			return
		}
		switch action {
		case "":
			var out []any
			for _, m := range msgs {
				out = append(out, s.gmailMessage(m))
			}
			write(w, map[string]any{"id": id, "messages": out})
		case "modify", "trash", "untrash":
			add, remove := strs(body["addLabelIds"]), strs(body["removeLabelIds"])
			if action == "trash" {
				add, remove = []string{"TRASH"}, []string{"INBOX"}
			}
			if action == "untrash" {
				add, remove = []string{"INBOX"}, []string{"TRASH"}
			}
			s.history++
			for _, m := range msgs {
				for _, l := range remove {
					m.Labels = without(m.Labels, l)
				}
				for _, l := range add {
					if !has(m.Labels, l) {
						m.Labels = append(m.Labels, l)
					}
				}
				m.HistoryID = s.history
			}
			write(w, map[string]any{"id": id})
		}
	case p == "/gmail/v1/users/me/history":
		start, _ := strconv.Atoi(q.Get("startHistoryId"))
		if start < 50 {
			fail(w, 404, "history too old")
			return
		}
		var hs []any
		for _, m := range s.Messages {
			if m.HistoryID > start {
				hs = append(hs, map[string]any{"messages": []map[string]string{{"id": m.ID, "threadId": m.ThreadID}}})
			}
		}
		write(w, map[string]any{"history": hs, "historyId": strconv.Itoa(s.history)})
	case p == "/gmail/v1/users/me/messages/send":
		raw, _ := body["raw"].(string)
		dec, _ := base64.URLEncoding.DecodeString(raw)
		s.Sent = append(s.Sent, string(dec))
		msg, err := stdmail.ReadMessage(strings.NewReader(string(dec)))
		if err != nil {
			fail(w, 400, "bad message")
			return
		}
		b, _ := io.ReadAll(msg.Body)
		text, _ := base64.StdEncoding.DecodeString(strings.ReplaceAll(strings.ReplaceAll(string(b), "\r\n", ""), "\n", ""))
		thread, _ := body["threadId"].(string)
		m := &Message{ID: s.id(), ThreadID: thread, Labels: []string{"SENT"}, From: s.Email, To: msg.Header.Get("To"), Cc: msg.Header.Get("Cc"),
			Subject: mime.DecodeHeader(msg.Header.Get("Subject")), Body: strings.ReplaceAll(string(text), "\r\n", "\n"), Date: time.Now()}
		if m.ThreadID == "" {
			m.ThreadID = m.ID
		}
		s.history++
		m.HistoryID = s.history
		s.Messages = append(s.Messages, m)
		write(w, map[string]string{"id": m.ID, "threadId": m.ThreadID})
	case p == "/gmail/v1/users/me/drafts":
		id := "d" + s.id()
		msg, _ := body["message"].(map[string]any)
		raw, _ := msg["raw"].(string)
		s.Drafts[id] = raw
		write(w, map[string]string{"id": id})
	case strings.HasPrefix(p, "/gmail/v1/users/me/drafts/"):
		id := strings.TrimPrefix(p, "/gmail/v1/users/me/drafts/")
		if r.Method == "DELETE" {
			delete(s.Drafts, id)
		} else {
			msg, _ := body["message"].(map[string]any)
			raw, _ := msg["raw"].(string)
			s.Drafts[id] = raw
		}
		write(w, map[string]string{"id": id})

	case p == "/chat/v1/spaces":
		var out []any
		for _, sp := range s.Spaces {
			out = append(out, map[string]any{"name": sp.Name, "displayName": sp.DisplayName, "spaceType": sp.Type,
				"lastActiveTime": s.lastActive(sp.Name).UTC().Format(time.RFC3339Nano)})
		}
		write(w, map[string]any{"spaces": out})
	case strings.HasPrefix(p, "/chat/v1/users/me/spaces/") && strings.HasSuffix(p, "/spaceReadState"):
		name := strings.TrimSuffix(strings.TrimPrefix(p, "/chat/v1/users/me/"), "/spaceReadState")
		sp := s.space(name)
		if sp == nil {
			fail(w, 404, "no space")
			return
		}
		if r.Method == "PATCH" {
			sp.LastRead, _ = time.Parse(time.RFC3339Nano, fmt.Sprint(body["lastReadTime"]))
		}
		write(w, map[string]string{"lastReadTime": sp.LastRead.UTC().Format(time.RFC3339Nano)})
	case strings.HasPrefix(p, "/chat/v1/spaces/") && strings.HasSuffix(p, "/members"):
		sp := s.space(strings.TrimSuffix(strings.TrimPrefix(p, "/chat/v1/"), "/members"))
		if sp == nil {
			fail(w, 404, "no space")
			return
		}
		var out []any
		for _, m := range sp.Members {
			out = append(out, map[string]any{"member": map[string]string{"name": m, "type": "HUMAN"}})
		}
		write(w, map[string]any{"memberships": out})
	case strings.HasPrefix(p, "/chat/v1/spaces/") && strings.HasSuffix(p, "/messages"):
		name := strings.TrimSuffix(strings.TrimPrefix(p, "/chat/v1/"), "/messages")
		if s.space(name) == nil {
			fail(w, 404, "no space")
			return
		}
		if r.Method == "POST" {
			m := &ChatMessage{Name: name + "/messages/" + s.id(), Space: name, Sender: s.Me, Text: fmt.Sprint(body["text"]), Created: time.Now()}
			if th, ok := body["thread"].(map[string]any); ok {
				m.Thread = fmt.Sprint(th["name"])
			} else {
				m.Thread = name + "/threads/" + s.id()
			}
			s.Chat = append(s.Chat, m)
			write(w, map[string]string{"name": m.Name})
			return
		}
		var after time.Time
		if f := q.Get("filter"); f != "" {
			if i := strings.Index(f, `"`); i >= 0 {
				after, _ = time.Parse(time.RFC3339Nano, strings.Trim(f[i:], `"`))
			}
		}
		var msgs []*ChatMessage
		for _, m := range s.Chat {
			if m.Space == name && m.Created.After(after) {
				msgs = append(msgs, m)
			}
		}
		sort.Slice(msgs, func(i, j int) bool { return msgs[i].Created.After(msgs[j].Created) })
		var out []any
		for _, m := range msgs {
			out = append(out, map[string]any{"name": m.Name, "text": m.Text, "createTime": m.Created.UTC().Format(time.RFC3339Nano),
				"sender": map[string]string{"name": m.Sender, "type": "HUMAN"}, "thread": map[string]string{"name": m.Thread}})
		}
		write(w, map[string]any{"messages": out})
	case strings.HasPrefix(p, "/people/v1/people/"):
		id := strings.TrimPrefix(p, "/people/v1/people/")
		user := "users/" + id
		if id == "me" {
			user = s.Me
		}
		pp, ok := s.People[user]
		if !ok {
			fail(w, 404, "no person")
			return
		}
		write(w, map[string]any{"resourceName": "people/" + strings.TrimPrefix(user, "users/"),
			"names": []map[string]string{{"displayName": pp[0]}}, "emailAddresses": []map[string]string{{"value": pp[1]}}})
	default:
		fail(w, 404, "fake google has no "+r.Method+" "+p)
	}
}

func (s *Server) space(name string) *Space {
	for _, sp := range s.Spaces {
		if sp.Name == name {
			return sp
		}
	}
	return nil
}

func (s *Server) lastActive(space string) time.Time {
	t := time.Now().Add(-90 * 24 * time.Hour)
	for _, m := range s.Chat {
		if m.Space == space && m.Created.After(t) {
			t = m.Created
		}
	}
	return t
}

func strs(v any) []string {
	l, _ := v.([]any)
	var out []string
	for _, x := range l {
		out = append(out, fmt.Sprint(x))
	}
	return out
}

func without(l []string, x string) []string {
	var out []string
	for _, y := range l {
		if y != x {
			out = append(out, y)
		}
	}
	return out
}

// Seed fills the server with the example mail and chat used by the tests
// and `sandesh demo`. The people are made up.
func (s *Server) Seed() {
	now := time.Now()
	// today's examples at their clock times once the day is past 10:00;
	// earlier (or in CI, in any timezone) squeezed into the part of today
	// that has passed, in the same order, so they're never in the future
	y, mo, d := now.Date()
	midnight := time.Date(y, mo, d, 0, 0, 0, 0, time.Local)
	today := func(h, m int) time.Time {
		t := midnight.Add(time.Duration(h)*time.Hour + time.Duration(m)*time.Minute)
		if now.Sub(midnight) >= 10*time.Hour {
			return t
		}
		return midnight.Add(time.Duration(float64(now.Sub(midnight)) * float64(h*60+m) / (10 * 60)))
	}
	s.People["users/2"] = [2]string{"Priya Kulkarni", "priya.k@example.com"}
	s.People["users/3"] = [2]string{"Arjun Mehta", "arjun.m@example.com"}
	s.People["users/4"] = [2]string{"Neha Rao", "neha.r@example.com"}

	s.AddMail(Message{ThreadID: "t-rohan", ID: "t-rohan", Labels: []string{"INBOX"}, From: "Rohan Desai <rohan.d@example.com>", To: s.Email,
		Subject: "Lunch Friday?", Body: "There's a new place near the office. Friday 1pm?", Date: now.Add(-50 * time.Hour)})
	s.AddMail(Message{ThreadID: "t-rohan", ID: "t-rohan-2", Labels: []string{"SENT"}, From: "You <" + s.Email + ">", To: "rohan.d@example.com",
		Subject: "Re: Lunch Friday?", Body: "Sounds good, see you there.", Date: now.Add(-49 * time.Hour)})
	s.AddMail(Message{ThreadID: "t-ci", ID: "t-ci", Labels: []string{"INBOX"}, From: "CI <ci@example.com>", To: s.Email,
		Subject: "Build #1932 passed on main", Body: "main @ 4f1c2e9 · 6m 12s · 412 tests passed", Date: now.Add(-26 * time.Hour)})
	s.AddMail(Message{ThreadID: "t-hr", ID: "t-hr", Labels: []string{"INBOX"}, From: "HR Team <hr@example.com>", To: "all@example.com",
		Subject: "Holiday list for 2027", Body: "Dear all,\n\nPlease find the 2027 holiday calendar on the intranet.\n\nRegards,\nHR", Date: now.Add(-25 * time.Hour)})
	s.AddMail(Message{ThreadID: "t-arjun", ID: "t-arjun", Labels: []string{"INBOX", "UNREAD"}, From: "Arjun Mehta <arjun.m@example.com>", To: s.Email,
		Subject: "Re: staging DB credentials", Body: "Rotated them, new ones are in the vault under staging/db.\nOld ones stop working at 18:00 today.\n\nA", Date: today(8, 55)})
	s.AddMail(Message{ThreadID: "t-gh", ID: "t-gh", Labels: []string{"INBOX", "UNREAD"}, From: "GitHub <notifications@github.com>", To: s.Email,
		Subject: "[acme/portal] Arjun requested your review on #412", Body: "Arjun Mehta requested your review on acme/portal#412\n\n  Move session refresh to the edge worker\n\n  +184 −61 · 7 files", Date: today(9, 10)})
	s.AddMail(Message{ThreadID: "t-priya", ID: "t-priya", Labels: []string{"INBOX", "UNREAD", "STARRED"}, From: "Priya Kulkarni <priya.k@example.com>", To: s.Email,
		Cc: "Arjun Mehta <arjun.m@example.com>", Subject: "Release cut on Thursday?",
		Body: "Hi,\n\nQA signed off on the 3.8 branch last night. Are we good to cut\nthe release on Thursday, or do you want to wait for the cache fix?\n\nNeed a yes/no by end of day so I can tell the client.\n\nThanks,\nPriya", Date: today(9, 42),
		HTML: `<div dir="ltr"><p>Hi,</p><p>QA signed off on the <b>3.8 branch</b> last night. Are we good to cut the release on <b>Thursday</b>, or do you want to wait for the cache fix?</p>
<h3>What's in 3.8</h3><ul><li>Session refresh on the edge worker</li><li>New billing export (CSV + XLSX)</li><li>Faster search on the orders page</li></ul>
<table><tr><th>Check</th><th>Status</th></tr><tr><td>Unit tests</td><td>412 passed</td></tr><tr><td>Staging smoke</td><td>green</td></tr><tr><td>Cache fix</td><td>in review</td></tr></table>
<p>Release notes draft: <a href="https://docs.example.com/acme/release-3.8-notes">release notes</a>. Need a yes/no by <b>end of day</b> so I can tell the client.</p>
<p><a href="https://tracker.example.com/acme/checklists/3.8" style="background:#1a73e8;color:#fff;padding:8px 16px">Open the release checklist</a></p>
<p>Thanks,<br>Priya</p>
<div class="gmail_quote">On Mon, 26 Oct, Arjun Mehta wrote:<blockquote>Cache fix is still in review, should land today.<br>If it slips, ship 3.8 without it and do 3.8.1 next week.</blockquote></div></div>`})

	s.AddSpace(Space{Name: "spaces/dm-priya", Type: "DIRECT_MESSAGE", Members: []string{"users/1", "users/2"}, LastRead: now.Add(-48 * time.Hour)})
	s.AddSpace(Space{Name: "spaces/portal-dev", DisplayName: "portal-dev", Type: "SPACE", Members: []string{"users/1", "users/3", "users/4"}, LastRead: now.Add(-48 * time.Hour)})
	s.AddSpace(Space{Name: "spaces/all", DisplayName: "everyone", Type: "SPACE", Members: []string{"users/1", "users/4"}, LastRead: now})
	s.AddChat(ChatMessage{Space: "spaces/all", Sender: "users/4", Text: "standup moved to 10:30 today", Created: today(8, 15)})
	s.AddChat(ChatMessage{Space: "spaces/portal-dev", Sender: "users/3", Text: "deploy to staging done", Created: today(9, 28)})
	s.AddChat(ChatMessage{Space: "spaces/portal-dev", Sender: "users/4", Text: "smoke tests green", Created: today(9, 29)})
	s.AddChat(ChatMessage{Space: "spaces/portal-dev", Sender: "users/3", Text: "@You can you look at #412 before lunch?", Created: today(9, 30)})
	s.AddChat(ChatMessage{Space: "spaces/dm-priya", Sender: "users/2", Text: "can you check my mail? need the release answer today", Created: today(9, 44)})
}
