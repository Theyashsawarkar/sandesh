package store

import (
	"database/sql"
	"encoding/json"
	"time"
)

// Outbox statuses. Every send goes through here: "approval" waits for
// you, "queued" waits out the undo window, then one process claims it
// ("sending") and delivers it.
const (
	StatusApproval  = "approval"
	StatusQueued    = "queued"
	StatusSending   = "sending"
	StatusSent      = "sent"
	StatusCancelled = "cancelled"
	StatusDeclined  = "declined"
	StatusFailed    = "failed"
)

// Outgoing is what is sent: a mail or a chat message.
type Outgoing struct {
	// mail
	To         []string `json:"to,omitempty"`
	Cc         []string `json:"cc,omitempty"`
	Bcc        []string `json:"bcc,omitempty"`
	Subject    string   `json:"subject,omitempty"`
	ThreadID   string   `json:"thread_id,omitempty"`
	InReplyTo  string   `json:"in_reply_to,omitempty"`
	References string   `json:"references,omitempty"`
	DraftID    string   `json:"draft_id,omitempty"`
	// chat
	Space     string `json:"space,omitempty"`
	SpaceName string `json:"space_name,omitempty"`
	Thread    string `json:"thread,omitempty"`
	Quote     string `json:"quote,omitempty"` // the message this one replies to (quoting it)
	// both
	Body  string `json:"body"`
	HTML  string `json:"html,omitempty"` // mail: sent beside Body when set
	Files []File `json:"files,omitempty"`
	// ScheduledFor (ms) holds it until then instead of the undo window
	ScheduledFor int64 `json:"scheduled_for,omitempty"`
}

// File is an attachment to send: a private copy made when it was queued
// (so what was approved is what goes out).
type File struct {
	Name string `json:"name"`
	Type string `json:"type"`
	Size int64  `json:"size"`
	Path string `json:"path"` // the private copy; "" until it is made
	// Source is where it came from, as typed (shown in the editor)
	Source string `json:"source,omitempty"`
	// Ref is a Gmail attachment to fetch instead of a local file
	// ("account/messageID/index"): a forwarded mail's attachments
	Ref string `json:"ref,omitempty"`
}

type OutboxItem struct {
	ID      int64    `json:"id"`
	Account string   `json:"account"`
	Kind    string   `json:"kind"` // mail | chat
	Out     Outgoing `json:"message"`
	Status  string   `json:"status"`
	SendAt  int64    `json:"send_at"`
	Created int64    `json:"created"`
	Origin  string   `json:"origin"` // you | agent
	Reason  string   `json:"reason,omitempty"`
	Error   string   `json:"error,omitempty"`
	Result  string   `json:"result,omitempty"` // the sent message's id
}

func (s *Store) AddOutbox(it OutboxItem) (int64, error) {
	payload, _ := json.Marshal(it.Out)
	res, err := s.db.Exec(`INSERT INTO outbox(account,kind,payload,status,send_at,created,origin,reason) VALUES(?,?,?,?,?,?,?,?)`,
		it.Account, it.Kind, string(payload), it.Status, it.SendAt, now(), it.Origin, it.Reason)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

const outboxCols = `id,account,kind,payload,status,send_at,created,origin,reason,error,result`

func scanOutbox(rows *sql.Rows) ([]OutboxItem, error) {
	defer rows.Close()
	var out []OutboxItem
	for rows.Next() {
		var it OutboxItem
		var payload string
		if err := rows.Scan(&it.ID, &it.Account, &it.Kind, &payload, &it.Status, &it.SendAt, &it.Created, &it.Origin, &it.Reason, &it.Error, &it.Result); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(payload), &it.Out)
		out = append(out, it)
	}
	return out, rows.Err()
}

func (s *Store) OutboxItem(id int64) (OutboxItem, error) {
	rows, err := s.db.Query(`SELECT `+outboxCols+` FROM outbox WHERE id=?`, id)
	if err != nil {
		return OutboxItem{}, err
	}
	items, err := scanOutbox(rows)
	if err != nil {
		return OutboxItem{}, err
	}
	if len(items) == 0 {
		return OutboxItem{}, ErrNotFound
	}
	return items[0], nil
}

// Outbox lists items with one of the statuses (none: every item from the
// last week), newest first.
func (s *Store) Outbox(statuses ...string) ([]OutboxItem, error) {
	q := `SELECT ` + outboxCols + ` FROM outbox`
	var args []any
	if len(statuses) > 0 {
		q += ` WHERE status IN (`
		for i, st := range statuses {
			if i > 0 {
				q += ","
			}
			q += "?"
			args = append(args, st)
		}
		q += `)`
	} else {
		q += ` WHERE created > ?`
		args = append(args, time.Now().Add(-7*24*time.Hour).UnixMilli())
	}
	q += ` ORDER BY id DESC`
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	return scanOutbox(rows)
}

// Transition moves an item from one status to another; false when it was
// no longer in `from` (someone else got there first).
func (s *Store) Transition(id int64, from, to string, sendAt int64) (bool, error) {
	res, err := s.db.Exec(`UPDATE outbox SET status=?, send_at=CASE WHEN ?>0 THEN ? ELSE send_at END WHERE id=? AND status=?`, to, sendAt, sendAt, id, from)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

func (s *Store) UpdateOutgoing(id int64, out Outgoing) error {
	payload, _ := json.Marshal(out)
	_, err := s.db.Exec(`UPDATE outbox SET payload=? WHERE id=?`, string(payload), id)
	return err
}

func (s *Store) FinishOutbox(id int64, status, errMsg, result string) error {
	_, err := s.db.Exec(`UPDATE outbox SET status=?, error=?, result=? WHERE id=?`, status, errMsg, result, id)
	return err
}

// DueOutbox: queued items whose undo window is over.
func (s *Store) DueOutbox() ([]OutboxItem, error) {
	rows, err := s.db.Query(`SELECT `+outboxCols+` FROM outbox WHERE status=? AND send_at<=?`, StatusQueued, now())
	if err != nil {
		return nil, err
	}
	return scanOutbox(rows)
}

// RecoverSending puts items stuck in "sending" (a process died mid-send)
// older than a minute back to failed, so they are visible, not lost.
func (s *Store) RecoverSending() error {
	_, err := s.db.Exec(`UPDATE outbox SET status=?, error='interrupted while sending; check Sent before retrying' WHERE status=? AND send_at < ?`,
		StatusFailed, StatusSending, time.Now().Add(-time.Minute).UnixMilli())
	return err
}

// Reschedule moves a waiting send to another time (and its payload too).
func (s *Store) Reschedule(id int64, out Outgoing, at int64) (bool, error) {
	payload, _ := json.Marshal(out)
	res, err := s.db.Exec(`UPDATE outbox SET send_at=?, payload=? WHERE id=? AND status=?`, at, string(payload), id, StatusQueued)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}
