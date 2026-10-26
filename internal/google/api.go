package google

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Theyashsawarkar/sandesh/internal/mime"
)

// Client calls Google's REST APIs for one account.
type Client struct {
	HTTP  *http.Client
	gmail string
	chat  string
	ppl   string
}

func NewClient(h *http.Client) *Client {
	if f := strings.TrimRight(FakeURL(), "/"); f != "" {
		return &Client{HTTP: h, gmail: f + "/gmail/v1", chat: f + "/chat/v1", ppl: f + "/people/v1"}
	}
	return &Client{HTTP: h, gmail: "https://gmail.googleapis.com/gmail/v1", chat: "https://chat.googleapis.com/v1", ppl: "https://people.googleapis.com/v1"}
}

// APIError is a non-2xx answer.
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string {
	switch e.Status {
	case 401:
		return "Google refused the sign-in (" + e.Message + "); run sandesh auth add again"
	case 403:
		return "Google said no: " + e.Message
	}
	return fmt.Sprintf("Google API %d: %s", e.Status, e.Message)
}

func IsNotFound(err error) bool {
	e, ok := err.(*APIError)
	return ok && e.Status == 404
}

func (c *Client) do(ctx context.Context, method, u string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, method, u, rd)
		if err != nil {
			return err
		}
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := c.HTTP.Do(req)
		if err != nil {
			return err
		}
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
		resp.Body.Close()
		if (resp.StatusCode == 429 || resp.StatusCode >= 500) && attempt < 3 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(1<<attempt) * time.Second):
			}
			if body != nil {
				b, _ := json.Marshal(body)
				rd = bytes.NewReader(b)
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
			return &APIError{Status: resp.StatusCode, Message: msg}
		}
		if out != nil && len(data) > 0 {
			return json.Unmarshal(data, out)
		}
		return nil
	}
}

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
func (c *Client) PostMessage(ctx context.Context, space, text, thread string) (string, error) {
	body := map[string]any{"text": text}
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
