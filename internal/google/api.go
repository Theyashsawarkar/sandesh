package google

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Theyashsawarkar/sandesh/internal/mime"
)

// Client calls Google's REST APIs for one account.
type Client struct {
	HTTP  *http.Client
	gmail string
	chat  string
	ppl   string
	quota *bucket
}

// Gmail allows 15,000 "quota units" a minute per user (threads.get is 10,
// history.list 2, send 100). sandesh spends at most 40 a second (2,400 a
// minute) with a burst of 200, far under it even with several processes.
type bucket struct {
	mu     sync.Mutex
	tokens float64
	last   time.Time
}

const (
	unitsPerSecond = 40
	unitsBurst     = 200
)

func (b *bucket) take(ctx context.Context, n float64) error {
	for {
		b.mu.Lock()
		now := time.Now()
		if b.last.IsZero() {
			b.tokens, b.last = unitsBurst, now
		}
		b.tokens = min(unitsBurst, b.tokens+now.Sub(b.last).Seconds()*unitsPerSecond)
		b.last = now
		if b.tokens >= n {
			b.tokens -= n
			b.mu.Unlock()
			return nil
		}
		wait := time.Duration((n - b.tokens) / unitsPerSecond * float64(time.Second))
		b.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
	}
}

// gmailCost is Gmail's quota cost of a call (from its usage limits page).
func gmailCost(method, u string) float64 {
	switch {
	case strings.Contains(u, "/messages/send") || strings.Contains(u, "/drafts/send"):
		return 100
	case strings.Contains(u, "/history"):
		return 2
	case strings.Contains(u, "/profile") || strings.Contains(u, "/labels"):
		return 1
	}
	return 10
}

func NewClient(h *http.Client) *Client {
	if f := strings.TrimRight(FakeURL(), "/"); f != "" {
		return &Client{HTTP: h, gmail: f + "/gmail/v1", chat: f + "/chat/v1", ppl: f + "/people/v1", quota: &bucket{}}
	}
	return &Client{HTTP: h, gmail: "https://gmail.googleapis.com/gmail/v1", chat: "https://chat.googleapis.com/v1", ppl: "https://people.googleapis.com/v1", quota: &bucket{}}
}

// APIError is a non-2xx answer.
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string {
	switch {
	case strings.Contains(e.Message, "Chat app not found"):
		return "Google Chat isn't set up in your Cloud project: open Google Chat API → Configuration, fill in an app name, avatar URL and description, save, and wait a few minutes"
	case strings.Contains(e.Message, "Quota exceeded") || strings.Contains(e.Message, "ateLimitExceeded"):
		return "Google is rate-limiting sandesh for a minute; it slows down and retries by itself"
	case strings.Contains(e.Message, "has not been used in project") || strings.Contains(e.Message, "is disabled"):
		api := "an API"
		for _, n := range []string{"Gmail API", "People API", "Google Chat API"} {
			if strings.Contains(e.Message, n) {
				api = "the " + n
			}
		}
		return "Enable " + api + " in your Cloud project (APIs & Services → Library), then wait a few minutes"
	}
	switch e.Status {
	case 401:
		return "Google refused the sign-in (" + e.Message + "); run sandesh auth add again"
	case 403:
		return "Google said no: " + e.Message
	}
	return fmt.Sprintf("Google API %d: %s", e.Status, e.Message)
}

// IsChatAppMissing: the Cloud project has no Chat app configured, so Chat
// refuses the calls that change things (read state, sending).
func IsChatAppMissing(err error) bool {
	e, ok := err.(*APIError)
	return ok && strings.Contains(e.Message, "Chat app not found")
}

func IsNotFound(err error) bool {
	e, ok := err.(*APIError)
	return ok && e.Status == 404
}

func (c *Client) do(ctx context.Context, method, u string, body, out any) error {
	var payload []byte
	ctype := ""
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		payload, ctype = b, "application/json"
	}
	data, err := c.raw(ctx, method, u, ctype, payload)
	if err != nil {
		return err
	}
	if out != nil && len(data) > 0 {
		return json.Unmarshal(data, out)
	}
	return nil
}

// raw sends payload (nil for none) and returns the response body, with
// Gmail's quota pacing and retries on rate limits and server errors.
func (c *Client) raw(ctx context.Context, method, u, ctype string, payload []byte) ([]byte, error) {
	if strings.HasPrefix(u, c.gmail) && c.quota != nil || strings.Contains(u, "/upload/gmail/") && c.quota != nil {
		if err := c.quota.take(ctx, gmailCost(method, u)); err != nil {
			return nil, err
		}
	}
	for attempt := 0; ; attempt++ {
		var rd io.Reader
		if payload != nil {
			rd = bytes.NewReader(payload)
		}
		req, err := http.NewRequestWithContext(ctx, method, u, rd)
		if err != nil {
			return nil, err
		}
		if ctype != "" {
			req.Header.Set("Content-Type", ctype)
		}
		t0 := time.Now()
		resp, err := c.HTTP.Do(req)
		if debugHTTP {
			code := 0
			if resp != nil {
				code = resp.StatusCode
			}
			fmt.Fprintf(os.Stderr, "sandesh http: %s %s %d %v\n", method, strings.SplitN(u, "?", 2)[0], code, time.Since(t0).Round(time.Millisecond))
		}
		if err != nil {
			return nil, err
		}
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
		resp.Body.Close()
		limited := resp.StatusCode == 429 || resp.StatusCode == 403 && bytes.Contains(data, []byte("ateLimitExceeded")) ||
			resp.StatusCode == 403 && bytes.Contains(data, []byte("Quota exceeded"))
		if (limited || resp.StatusCode >= 500) && attempt < 5 {
			// back off: 2, 4, 8, 16, 32 s, or what Google asks for
			wait := time.Duration(2<<attempt) * time.Second
			if ra, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && ra > 0 {
				wait = time.Duration(ra) * time.Second
			}
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(wait):
			}
			continue
		}
		if resp.StatusCode/100 != 2 {
			var e struct {
				Error struct {
					Message string `json:"message"`
				} `json:"error"`
			}
			_ = json.Unmarshal(data, &e)
			msg := e.Error.Message
			if msg == "" {
				msg = strings.TrimSpace(string(data))
			}
			return nil, &APIError{Status: resp.StatusCode, Message: msg}
		}
		return data, nil
	}
}

// SANDESH_DEBUG_HTTP=1 logs every request (method, URL without the query,
// time) to stderr: for counting what a sync costs.
var debugHTTP = os.Getenv("SANDESH_DEBUG_HTTP") != ""

// root is the API host the client talks to ("https://gmail.googleapis.com"
// or the fake's address), for the upload endpoints.
func root(base, suffix string) string { return strings.TrimSuffix(base, suffix) }

// ------------------------------------------------------------------ gmail

type GmailMessage struct {
	ID           string    `json:"id"`
	ThreadID     string    `json:"threadId"`
	LabelIDs     []string  `json:"labelIds"`
	Snippet      string    `json:"snippet"`
	HistoryID    string    `json:"historyId"`
	InternalDate string    `json:"internalDate"`
	Payload      mime.Part `json:"payload"`
}

type GmailThread struct {
	ID        string         `json:"id"`
	Snippet   string         `json:"snippet"`
	HistoryID string         `json:"historyId"`
	Messages  []GmailMessage `json:"messages"`
}

func (c *Client) Profile(ctx context.Context) (email, historyID string, err error) {
	var p struct {
		EmailAddress string `json:"emailAddress"`
		HistoryID    string `json:"historyId"`
	}
	err = c.do(ctx, "GET", c.gmail+"/users/me/profile", nil, &p)
	return p.EmailAddress, p.HistoryID, err
}

// Signature is the default send-as address's Gmail signature (HTML).
func (c *Client) Signature(ctx context.Context) (string, error) {
	var r struct {
		SendAs []struct {
			IsDefault bool   `json:"isDefault"`
			Signature string `json:"signature"`
		} `json:"sendAs"`
	}
	if err := c.do(ctx, "GET", c.gmail+"/users/me/settings/sendAs", nil, &r); err != nil {
		return "", err
	}
	for _, s := range r.SendAs {
		if s.IsDefault {
			return s.Signature, nil
		}
	}
	return "", nil
}

// ThreadsTotal is how many threads the mailbox holds.
func (c *Client) ThreadsTotal(ctx context.Context) (int, error) {
	var p struct {
		ThreadsTotal int `json:"threadsTotal"`
	}
	err := c.do(ctx, "GET", c.gmail+"/users/me/profile", nil, &p)
	return p.ThreadsTotal, err
}

// ThreadsPage returns one page of thread ids (newest first) and the token
// for the next ("" at the end).
func (c *Client) ThreadsPage(ctx context.Context, q, page string, max int, labels ...string) ([]string, string, error) {
	v := url.Values{"maxResults": {fmt.Sprint(max)}}
	if q != "" {
		v.Set("q", q)
	}
	for _, l := range labels {
		v.Add("labelIds", l)
	}
	if page != "" {
		v.Set("pageToken", page)
	}
	var r struct {
		Threads []struct {
			ID string `json:"id"`
		} `json:"threads"`
		NextPageToken string `json:"nextPageToken"`
	}
	if err := c.do(ctx, "GET", c.gmail+"/users/me/threads?"+v.Encode(), nil, &r); err != nil {
		return nil, "", err
	}
	var ids []string
	for _, t := range r.Threads {
		ids = append(ids, t.ID)
	}
	return ids, r.NextPageToken, nil
}

// ListThreads returns thread ids matching a Gmail search, newest first,
// up to max.
func (c *Client) ListThreads(ctx context.Context, q string, labels []string, max int) ([]string, error) {
	var ids []string
	page := ""
	for len(ids) < max {
		v := url.Values{"maxResults": {fmt.Sprint(min(500, max-len(ids)))}}
		if q != "" {
			v.Set("q", q)
		}
		for _, l := range labels {
			v.Add("labelIds", l)
		}
		if page != "" {
			v.Set("pageToken", page)
		}
		var r struct {
			Threads []struct {
				ID string `json:"id"`
			} `json:"threads"`
			NextPageToken string `json:"nextPageToken"`
		}
		if err := c.do(ctx, "GET", c.gmail+"/users/me/threads?"+v.Encode(), nil, &r); err != nil {
			return nil, err
		}
		for _, t := range r.Threads {
			ids = append(ids, t.ID)
		}
		if r.NextPageToken == "" {
			break
		}
		page = r.NextPageToken
	}
	return ids, nil
}

func (c *Client) GetThread(ctx context.Context, id string) (GmailThread, error) {
	var t GmailThread
	err := c.do(ctx, "GET", c.gmail+"/users/me/threads/"+url.PathEscape(id)+"?format=full", nil, &t)
	return t, err
}

// History returns the threads touched since historyID and the new
// history id. A 404 means the id is too old: sync from scratch.
func (c *Client) History(ctx context.Context, historyID string) (threads []string, newID string, err error) {
	seen := map[string]bool{}
	page := ""
	newID = historyID
	for {
		v := url.Values{"startHistoryId": {historyID}, "maxResults": {"500"}}
		if page != "" {
			v.Set("pageToken", page)
		}
		var r struct {
			History []struct {
				Messages []struct {
					ThreadID string `json:"threadId"`
				} `json:"messages"`
			} `json:"history"`
			HistoryID     string `json:"historyId"`
			NextPageToken string `json:"nextPageToken"`
		}
		if err := c.do(ctx, "GET", c.gmail+"/users/me/history?"+v.Encode(), nil, &r); err != nil {
			return nil, "", err
		}
		for _, h := range r.History {
			for _, m := range h.Messages {
				if !seen[m.ThreadID] {
					seen[m.ThreadID] = true
					threads = append(threads, m.ThreadID)
				}
			}
		}
		if r.HistoryID != "" {
			newID = r.HistoryID
		}
		if r.NextPageToken == "" {
			return threads, newID, nil
		}
		page = r.NextPageToken
	}
}

func (c *Client) ModifyThread(ctx context.Context, id string, add, remove []string) error {
	body := map[string][]string{"addLabelIds": add, "removeLabelIds": remove}
	return c.do(ctx, "POST", c.gmail+"/users/me/threads/"+url.PathEscape(id)+"/modify", body, nil)
}

func (c *Client) TrashThread(ctx context.Context, id string) error {
	return c.do(ctx, "POST", c.gmail+"/users/me/threads/"+url.PathEscape(id)+"/trash", nil, nil)
}

func (c *Client) UntrashThread(ctx context.Context, id string) error {
	return c.do(ctx, "POST", c.gmail+"/users/me/threads/"+url.PathEscape(id)+"/untrash", nil, nil)
}

// Send sends a raw message (from mime.Build), in a thread when threadID
// is set. Returns the new message's id.
func (c *Client) Send(ctx context.Context, raw, threadID string) (string, error) {
	if len(raw) > 4<<20 {
		return c.sendUpload(ctx, raw, threadID)
	}
	body := map[string]string{"raw": raw}
	if threadID != "" {
		body["threadId"] = threadID
	}
	var r struct {
		ID string `json:"id"`
	}
	err := c.do(ctx, "POST", c.gmail+"/users/me/messages/send", body, &r)
	return r.ID, err
}

// sendUpload sends a message too big for a JSON body (Gmail takes up to
// 35 MB this way): a multipart/related upload of the metadata and the
// RFC 822 message.
func (c *Client) sendUpload(ctx context.Context, raw, threadID string) (string, error) {
	msg, err := base64.URLEncoding.DecodeString(raw)
	if err != nil {
		return "", err
	}
	meta, _ := json.Marshal(map[string]string{"threadId": threadID})
	if threadID == "" {
		meta = []byte("{}")
	}
	const boundary = "sandesh-upload-boundary"
	var b bytes.Buffer
	b.WriteString("--" + boundary + "\r\nContent-Type: application/json; charset=UTF-8\r\n\r\n")
	b.Write(meta)
	b.WriteString("\r\n--" + boundary + "\r\nContent-Type: message/rfc822\r\n\r\n")
	b.Write(msg)
	b.WriteString("\r\n--" + boundary + "--\r\n")
	u := root(c.gmail, "/gmail/v1") + "/upload/gmail/v1/users/me/messages/send?uploadType=multipart"
	data, err := c.raw(ctx, "POST", u, "multipart/related; boundary="+boundary, b.Bytes())
	if err != nil {
		return "", err
	}
	var r struct {
		ID string `json:"id"`
	}
	err = json.Unmarshal(data, &r)
	return r.ID, err
}

// GetMessage fetches one message in full (to find an attachment's part).
func (c *Client) GetMessage(ctx context.Context, id string) (GmailMessage, error) {
	var m GmailMessage
	err := c.do(ctx, "GET", c.gmail+"/users/me/messages/"+url.PathEscape(id)+"?format=full", nil, &m)
	return m, err
}

// AttachmentData downloads one attachment of a message.
func (c *Client) AttachmentData(ctx context.Context, msgID, attID string) ([]byte, error) {
	var r struct {
		Data string `json:"data"`
	}
	if err := c.do(ctx, "GET", c.gmail+"/users/me/messages/"+url.PathEscape(msgID)+"/attachments/"+url.PathEscape(attID), nil, &r); err != nil {
		return nil, err
	}
	return decodeB64(r.Data)
}

func decodeB64(s string) ([]byte, error) {
	s = strings.TrimRight(s, "=")
	if b, err := base64.RawURLEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	return base64.RawStdEncoding.DecodeString(s)
}

// CreateDraft saves a Gmail draft (visible in Gmail everywhere).
func (c *Client) CreateDraft(ctx context.Context, raw, threadID string) (string, error) {
	msg := map[string]string{"raw": raw}
	if threadID != "" {
		msg["threadId"] = threadID
	}
	var r struct {
		ID string `json:"id"`
	}
	err := c.do(ctx, "POST", c.gmail+"/users/me/drafts", map[string]any{"message": msg}, &r)
	return r.ID, err
}

func (c *Client) UpdateDraft(ctx context.Context, id, raw, threadID string) error {
	msg := map[string]string{"raw": raw}
	if threadID != "" {
		msg["threadId"] = threadID
	}
	return c.do(ctx, "PUT", c.gmail+"/users/me/drafts/"+url.PathEscape(id), map[string]any{"id": id, "message": msg}, nil)
}

func (c *Client) DeleteDraft(ctx context.Context, id string) error {
	return c.do(ctx, "DELETE", c.gmail+"/users/me/drafts/"+url.PathEscape(id), nil, nil)
}

type GmailLabel struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"`
}

func (c *Client) Labels(ctx context.Context) ([]GmailLabel, error) {
	var r struct {
		Labels []GmailLabel `json:"labels"`
	}
	err := c.do(ctx, "GET", c.gmail+"/users/me/labels", nil, &r)
	return r.Labels, err
}

// ------------------------------------------------------------------- chat

type ChatSpace struct {
	Name             string `json:"name"`
	DisplayName      string `json:"displayName"`
	SpaceType        string `json:"spaceType"`
	LastActiveTime   string `json:"lastActiveTime"`
	SingleUserBotDM  bool   `json:"singleUserBotDm"`
	SpaceThreadState string `json:"spaceThreadingState"`
}

func (c *Client) Spaces(ctx context.Context) ([]ChatSpace, error) {
	var all []ChatSpace
	page := ""
	for {
		v := url.Values{"pageSize": {"1000"}}
		if page != "" {
			v.Set("pageToken", page)
		}
		var r struct {
			Spaces        []ChatSpace `json:"spaces"`
			NextPageToken string      `json:"nextPageToken"`
		}
		if err := c.do(ctx, "GET", c.chat+"/spaces?"+v.Encode(), nil, &r); err != nil {
			return nil, err
		}
		all = append(all, r.Spaces...)
		if r.NextPageToken == "" {
			return all, nil
		}
		page = r.NextPageToken
	}
}

type ChatMsg struct {
	Name       string `json:"name"`
	Text       string `json:"text"`
	CreateTime string `json:"createTime"`
	Sender     struct {
		Name        string `json:"name"`
		DisplayName string `json:"displayName"`
		Type        string `json:"type"`
	} `json:"sender"`
	Thread struct {
		Name string `json:"name"`
	} `json:"thread"`
	Attachment []ChatAttachment  `json:"attachment"`
	CardsV2    []json.RawMessage `json:"cardsV2"`
	// a reply that quotes another message
	QuotedMessageMetadata *struct {
		Name           string `json:"name"`
		LastUpdateTime string `json:"lastUpdateTime"`
	} `json:"quotedMessageMetadata,omitempty"`
	LastUpdateTime string `json:"lastUpdateTime,omitempty"`
	// emoji reactions, summed per emoji
	EmojiReactionSummaries []struct {
		Emoji struct {
			Unicode string `json:"unicode"`
		} `json:"emoji"`
		ReactionCount int `json:"reactionCount"`
	} `json:"emojiReactionSummaries,omitempty"`
}

// OlderMessages returns up to max messages created before t, oldest first.
func (c *Client) OlderMessages(ctx context.Context, space string, before time.Time, max int) ([]ChatMsg, error) {
	v := url.Values{"pageSize": {fmt.Sprint(min(1000, max))}, "orderBy": {"createTime desc"},
		"filter": {fmt.Sprintf(`createTime < "%s"`, before.UTC().Format(time.RFC3339Nano))}}
	var r struct {
		Messages []ChatMsg `json:"messages"`
	}
	if err := c.do(ctx, "GET", c.chat+"/"+space+"/messages?"+v.Encode(), nil, &r); err != nil {
		return nil, err
	}
	out := r.Messages
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}

// React adds an emoji reaction to a message.
func (c *Client) React(ctx context.Context, msg, emoji string) error {
	return c.do(ctx, "POST", c.chat+"/"+escapePath(msg)+"/reactions", map[string]any{"emoji": map[string]string{"unicode": emoji}}, nil)
}

// EditMessage changes the text of a message you sent.
func (c *Client) EditMessage(ctx context.Context, msg, text string) error {
	return c.do(ctx, "PATCH", c.chat+"/"+escapePath(msg)+"?updateMask=text", map[string]string{"text": text}, nil)
}

// DeleteMessage deletes a message you sent.
func (c *Client) DeleteMessage(ctx context.Context, msg string) error {
	return c.do(ctx, "DELETE", c.chat+"/"+escapePath(msg), nil, nil)
}

// GetChatMessage fetches one message (for its last update time, which a
// quote needs).
func (c *Client) GetChatMessage(ctx context.Context, name string) (ChatMsg, error) {
	var m ChatMsg
	err := c.do(ctx, "GET", c.chat+"/"+escapePath(name), nil, &m)
	return m, err
}

// ChatAttachment is a file in a chat message: uploaded to Chat (a data
// ref, downloadable here) or a Drive file (opened in the browser).
type ChatAttachment struct {
	Name              string `json:"name,omitempty"`
	ContentName       string `json:"contentName"`
	ContentType       string `json:"contentType"`
	Source            string `json:"source,omitempty"`
	AttachmentDataRef *struct {
		ResourceName string `json:"resourceName"`
	} `json:"attachmentDataRef,omitempty"`
	DriveDataRef *struct {
		DriveFileID string `json:"driveFileId"`
	} `json:"driveDataRef,omitempty"`
	DownloadURI string `json:"downloadUri,omitempty"`
}

// Display is the message's text, or what it holds when it has none.
func (m ChatMsg) Display() string {
	t := m.Text
	for _, a := range m.Attachment {
		if t != "" {
			t += "\n"
		}
		t += "📎 " + a.ContentName
	}
	if t == "" && len(m.CardsV2) > 0 {
		t = "▤ a card (open it in Chat with b)"
	}
	if t == "" {
		t = "(a message with no text)"
	}
	return t
}

// Messages lists a space's messages created after `after` (zero: the
// newest `max`), oldest first.
func (c *Client) Messages(ctx context.Context, space string, after time.Time, max int) ([]ChatMsg, error) {
	v := url.Values{"pageSize": {fmt.Sprint(min(1000, max))}, "orderBy": {"createTime desc"}}
	if !after.IsZero() {
		v.Set("filter", fmt.Sprintf(`createTime > "%s"`, after.UTC().Format(time.RFC3339Nano)))
	}
	var r struct {
		Messages []ChatMsg `json:"messages"`
	}
	if err := c.do(ctx, "GET", c.chat+"/"+space+"/messages?"+v.Encode(), nil, &r); err != nil {
		return nil, err
	}
	out := r.Messages
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}

// PostMessage sends text to a space; thread (a thread name) replies in it.
// FindDM finds your direct message with a person: users/{id} or, signed in
// as yourself, users/{email}. A 404 means there is none yet.
func (c *Client) FindDM(ctx context.Context, user string) (ChatSpace, error) {
	var sp ChatSpace
	err := c.do(ctx, "GET", c.chat+"/spaces:findDirectMessage?name="+url.QueryEscape(user), nil, &sp)
	return sp, err
}

// StartConversation creates a DM with one person, or a group chat with
// several (users/{id} or users/{email}).
func (c *Client) StartConversation(ctx context.Context, users []string, name string) (ChatSpace, error) {
	typ := "DIRECT_MESSAGE"
	if len(users) > 1 {
		typ = "GROUP_CHAT"
	}
	space := map[string]string{"spaceType": typ}
	if name != "" && len(users) > 1 {
		// a named group is a space (group chats can't have names)
		space = map[string]string{"spaceType": "SPACE", "displayName": name}
	}
	var members []map[string]any
	for _, u := range users {
		members = append(members, map[string]any{"member": map[string]string{"name": u, "type": "HUMAN"}})
	}
	var sp ChatSpace
	err := c.do(ctx, "POST", c.chat+"/spaces:setup", map[string]any{"space": space, "memberships": members}, &sp)
	return sp, err
}

// RenameSpace changes a named space's title on Google Chat (you must be
// allowed to: usually a space manager).
func (c *Client) RenameSpace(ctx context.Context, space, name string) error {
	return c.do(ctx, "PATCH", c.chat+"/"+space+"?updateMask=displayName", map[string]string{"displayName": name}, nil)
}

// NameGroup gives a group chat a name, which makes it a space (members and
// messages stay); Google can't name a group chat otherwise.
func (c *Client) NameGroup(ctx context.Context, space, name string) error {
	return c.do(ctx, "PATCH", c.chat+"/"+space+"?updateMask=spaceType,displayName",
		map[string]string{"spaceType": "SPACE", "displayName": name}, nil)
}

// IsMissingScope: the sign-in predates a permission sandesh now asks for.
func IsMissingScope(err error) bool {
	e, ok := err.(*APIError)
	return ok && (strings.Contains(e.Message, "insufficient authentication scopes") || strings.Contains(e.Message, "ACCESS_TOKEN_SCOPE_INSUFFICIENT"))
}

// ChatMedia downloads a file uploaded to a chat message.
func (c *Client) ChatMedia(ctx context.Context, resourceName string) ([]byte, error) {
	u := root(c.chat, "/v1") + "/v1/media/" + escapePath(resourceName) + "?alt=media"
	return c.raw(ctx, "GET", u, "", nil)
}

// UploadChatFile uploads a file to a space; the returned reference goes
// into the message that carries it.
func (c *Client) UploadChatFile(ctx context.Context, space, name, ctype string, data []byte) (json.RawMessage, error) {
	const boundary = "sandesh-upload-boundary"
	meta, _ := json.Marshal(map[string]string{"filename": name})
	var b bytes.Buffer
	b.WriteString("--" + boundary + "\r\nContent-Type: application/json; charset=UTF-8\r\n\r\n")
	b.Write(meta)
	b.WriteString("\r\n--" + boundary + "\r\nContent-Type: " + ctype + "\r\n\r\n")
	b.Write(data)
	b.WriteString("\r\n--" + boundary + "--\r\n")
	u := root(c.chat, "/v1") + "/upload/v1/" + space + "/attachments:upload?uploadType=multipart"
	out, err := c.raw(ctx, "POST", u, "multipart/related; boundary="+boundary, b.Bytes())
	if err != nil {
		return nil, err
	}
	var r struct {
		AttachmentDataRef json.RawMessage `json:"attachmentDataRef"`
	}
	if err := json.Unmarshal(out, &r); err != nil {
		return nil, err
	}
	return r.AttachmentDataRef, nil
}

func escapePath(p string) string {
	parts := strings.Split(p, "/")
	for i, x := range parts {
		parts[i] = url.PathEscape(x)
	}
	return strings.Join(parts, "/")
}

// PostMessage posts text (and files) in a space: in a thread when thread
// is set, quoting another message when quote is (its name).
func (c *Client) PostMessage(ctx context.Context, space, text, thread, quote string, files ...json.RawMessage) (string, error) {
	body := map[string]any{"text": text}
	if quote != "" {
		q, err := c.GetChatMessage(ctx, quote)
		if err != nil {
			return "", fmt.Errorf("the message you reply to: %w", err)
		}
		at := q.LastUpdateTime
		if at == "" {
			at = q.CreateTime
		}
		body["quotedMessageMetadata"] = map[string]string{"name": quote, "lastUpdateTime": at}
	}
	if len(files) > 0 {
		var atts []map[string]any
		for _, f := range files {
			atts = append(atts, map[string]any{"attachmentDataRef": f})
		}
		body["attachment"] = atts
	}
	u := c.chat + "/" + space + "/messages"
	if thread != "" {
		body["thread"] = map[string]string{"name": thread}
		u += "?messageReplyOption=REPLY_MESSAGE_FALLBACK_TO_NEW_THREAD"
	}
	var r struct {
		Name string `json:"name"`
	}
	err := c.do(ctx, "POST", u, body, &r)
	return r.Name, err
}

// ReadTime is when you last read the space (Chat's own read state, the
// same one the web and phone apps use).
func (c *Client) ReadTime(ctx context.Context, space string) (time.Time, error) {
	var r struct {
		LastReadTime string `json:"lastReadTime"`
	}
	if err := c.do(ctx, "GET", c.chat+"/users/me/"+space+"/spaceReadState", nil, &r); err != nil {
		return time.Time{}, err
	}
	return parseTime(r.LastReadTime), nil
}

func (c *Client) SetReadTime(ctx context.Context, space string, t time.Time) error {
	body := map[string]string{"lastReadTime": t.UTC().Format(time.RFC3339Nano)}
	return c.do(ctx, "PATCH", c.chat+"/users/me/"+space+"/spaceReadState?updateMask=lastReadTime", body, nil)
}

// Members returns the human members' user names (users/123) of a space.
func (c *Client) Members(ctx context.Context, space string) ([]string, error) {
	var r struct {
		Memberships []struct {
			Member struct {
				Name string `json:"name"`
				Type string `json:"type"`
			} `json:"member"`
		} `json:"memberships"`
	}
	if err := c.do(ctx, "GET", c.chat+"/"+space+"/members?pageSize=100", nil, &r); err != nil {
		return nil, err
	}
	var out []string
	for _, m := range r.Memberships {
		if m.Member.Type == "HUMAN" || m.Member.Type == "" {
			out = append(out, m.Member.Name)
		}
	}
	return out, nil
}

// ----------------------------------------------------------------- people

// Person resolves "users/123" (or "me") to a name and email through the
// People API; Chat's user ids are People ids.
// Person is someone from the Workspace directory.
type DirPerson struct{ Name, Email string }

// SearchDirectory finds colleagues in your Google Workspace directory by
// name or address (People API; the directory.readonly scope).
func (c *Client) SearchDirectory(ctx context.Context, query string) ([]DirPerson, error) {
	v := url.Values{"query": {query}, "readMask": {"names,emailAddresses"}, "pageSize": {"10"},
		"sources": {"DIRECTORY_SOURCE_TYPE_DOMAIN_PROFILE", "DIRECTORY_SOURCE_TYPE_DOMAIN_CONTACT"}}
	var r struct {
		People []struct {
			Names []struct {
				DisplayName string `json:"displayName"`
			} `json:"names"`
			EmailAddresses []struct {
				Value string `json:"value"`
			} `json:"emailAddresses"`
		} `json:"people"`
	}
	if err := c.do(ctx, "GET", c.ppl+"/people:searchDirectoryPeople?"+v.Encode(), nil, &r); err != nil {
		return nil, err
	}
	var out []DirPerson
	for _, p := range r.People {
		if len(p.EmailAddresses) == 0 {
			continue
		}
		d := DirPerson{Email: p.EmailAddresses[0].Value}
		if len(p.Names) > 0 {
			d.Name = p.Names[0].DisplayName
		}
		out = append(out, d)
	}
	return out, nil
}

func (c *Client) Person(ctx context.Context, id string) (resource, name, email string, err error) {
	id = strings.TrimPrefix(id, "users/")
	var r struct {
		ResourceName string `json:"resourceName"`
		Names        []struct {
			DisplayName string `json:"displayName"`
		} `json:"names"`
		EmailAddresses []struct {
			Value string `json:"value"`
		} `json:"emailAddresses"`
	}
	v := url.Values{"personFields": {"names,emailAddresses"}}
	if id != "me" {
		v.Set("sources", "READ_SOURCE_TYPE_DOMAIN_PROFILE")
		v.Add("sources", "READ_SOURCE_TYPE_PROFILE")
		v.Add("sources", "READ_SOURCE_TYPE_CONTACT")
	}
	if err = c.do(ctx, "GET", c.ppl+"/people/"+url.PathEscape(id)+"?"+v.Encode(), nil, &r); err != nil {
		return
	}
	resource = r.ResourceName
	if len(r.Names) > 0 {
		name = r.Names[0].DisplayName
	}
	if len(r.EmailAddresses) > 0 {
		email = r.EmailAddresses[0].Value
	}
	return
}

func parseTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

// ParseTime parses Google's RFC 3339 timestamps (zero on failure).
func ParseTime(s string) time.Time { return parseTime(s) }
