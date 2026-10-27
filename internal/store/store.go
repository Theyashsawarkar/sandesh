// Package store is sandesh's local copy of mail and chat: one SQLite
// database (WAL, so the daemon, the screen, the CLI and the MCP server
// can all use it at once) with full-text search over mail.
package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type Store struct {
	db   *sql.DB
	path string
}

const schema = `
CREATE TABLE IF NOT EXISTS accounts (
  name TEXT PRIMARY KEY, email TEXT NOT NULL DEFAULT '', chat INTEGER NOT NULL DEFAULT 1,
  history_id TEXT NOT NULL DEFAULT '', chat_user TEXT NOT NULL DEFAULT '',
  mail_synced INTEGER NOT NULL DEFAULT 0, chat_synced INTEGER NOT NULL DEFAULT 0,
  last_error TEXT NOT NULL DEFAULT '', position INTEGER NOT NULL DEFAULT 0);
CREATE TABLE IF NOT EXISTS threads (
  account TEXT NOT NULL, id TEXT NOT NULL, subject TEXT NOT NULL DEFAULT '', snippet TEXT NOT NULL DEFAULT '',
  last_date INTEGER NOT NULL DEFAULT 0, labels TEXT NOT NULL DEFAULT ',', participants TEXT NOT NULL DEFAULT '',
  last_from TEXT NOT NULL DEFAULT '', msg_count INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (account, id));
CREATE INDEX IF NOT EXISTS threads_date ON threads(account, last_date DESC);
CREATE TABLE IF NOT EXISTS messages (
  account TEXT NOT NULL, id TEXT NOT NULL, thread_id TEXT NOT NULL, from_addr TEXT NOT NULL DEFAULT '',
  from_name TEXT NOT NULL DEFAULT '', to_addrs TEXT NOT NULL DEFAULT '', cc_addrs TEXT NOT NULL DEFAULT '',
  reply_to TEXT NOT NULL DEFAULT '', subject TEXT NOT NULL DEFAULT '', date INTEGER NOT NULL DEFAULT 0,
  labels TEXT NOT NULL DEFAULT ',', body TEXT NOT NULL DEFAULT '', message_id TEXT NOT NULL DEFAULT '',
  refs TEXT NOT NULL DEFAULT '', attachments TEXT NOT NULL DEFAULT '[]',
  PRIMARY KEY (account, id));
CREATE INDEX IF NOT EXISTS messages_thread ON messages(account, thread_id, date);
CREATE VIRTUAL TABLE IF NOT EXISTS messages_fts USING fts5(account UNINDEXED, id UNINDEXED, thread_id UNINDEXED, subject, from_text, body);
CREATE TABLE IF NOT EXISTS section_threads (account TEXT NOT NULL, section TEXT NOT NULL, thread_id TEXT NOT NULL,
  PRIMARY KEY (account, section, thread_id));
CREATE TABLE IF NOT EXISTS contacts (account TEXT NOT NULL, addr TEXT NOT NULL, name TEXT NOT NULL DEFAULT '',
  sent INTEGER NOT NULL DEFAULT 0, received INTEGER NOT NULL DEFAULT 0, last INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (account, addr));
CREATE TABLE IF NOT EXISTS labels (account TEXT NOT NULL, id TEXT NOT NULL, name TEXT NOT NULL, type TEXT NOT NULL DEFAULT 'user',
  PRIMARY KEY (account, id));
CREATE TABLE IF NOT EXISTS spaces (
  account TEXT NOT NULL, name TEXT NOT NULL, display_name TEXT NOT NULL DEFAULT '', type TEXT NOT NULL DEFAULT '',
  last_active INTEGER NOT NULL DEFAULT 0, last_read INTEGER NOT NULL DEFAULT 0, synced_to INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (account, name));
CREATE TABLE IF NOT EXISTS chat_messages (
  account TEXT NOT NULL, name TEXT NOT NULL, space TEXT NOT NULL, thread TEXT NOT NULL DEFAULT '',
  sender TEXT NOT NULL DEFAULT '', sender_name TEXT NOT NULL DEFAULT '', text TEXT NOT NULL DEFAULT '',
  created INTEGER NOT NULL DEFAULT 0, PRIMARY KEY (account, name));
CREATE INDEX IF NOT EXISTS chat_space ON chat_messages(account, space, created);
CREATE TABLE IF NOT EXISTS users (account TEXT NOT NULL, id TEXT NOT NULL, name TEXT NOT NULL DEFAULT '', email TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (account, id));
CREATE TABLE IF NOT EXISTS outbox (
  id INTEGER PRIMARY KEY AUTOINCREMENT, account TEXT NOT NULL, kind TEXT NOT NULL, payload TEXT NOT NULL,
  status TEXT NOT NULL, send_at INTEGER NOT NULL DEFAULT 0, created INTEGER NOT NULL, origin TEXT NOT NULL DEFAULT 'you',
  reason TEXT NOT NULL DEFAULT '', error TEXT NOT NULL DEFAULT '', result TEXT NOT NULL DEFAULT '');
CREATE TABLE IF NOT EXISTS notified (account TEXT NOT NULL, key TEXT NOT NULL, PRIMARY KEY (account, key));
`

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)")
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("opening %s: %w", path, err)
	}
	// the local copy is your mail: readable by you only
	_ = os.Chmod(path, 0o600)
	for _, x := range []string{"-wal", "-shm"} {
		_ = os.Chmod(path+x, 0o600)
	}
	if err := migrate(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("upgrading %s: %w", path, err)
	}
	return &Store{db: db, path: path}, nil
}

// schemaVersion: bump it with a step in migrate.
const schemaVersion = 11

func migrate(db *sql.DB) error {
	var v int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		return err
	}
	if v < 2 {
		// v2 keeps each message's HTML for the formatted preview; mail
		// synced before has none, so the next sync fetches it again
		if _, err := db.Exec(`ALTER TABLE messages ADD COLUMN body_html TEXT NOT NULL DEFAULT ''`); err != nil && !strings.Contains(err.Error(), "duplicate column") {
			return err
		}
		if _, err := db.Exec(`UPDATE accounts SET history_id=''`); err != nil {
			return err
		}
	}
	if v < 3 {
		// v3 stops keeping "user 1234" placeholders as names: forget them
		// (and the DM titles built from them) so they are looked up again
		if _, err := db.Exec(`DELETE FROM users WHERE name LIKE 'user %' OR name=''`); err != nil {
			return err
		}
		if _, err := db.Exec(`UPDATE spaces SET display_name='' WHERE type<>'SPACE' AND display_name LIKE '%user %'`); err != nil {
			return err
		}
		if _, err := db.Exec(`UPDATE chat_messages SET text='(a message with no text)' WHERE text=''`); err != nil {
			return err
		}
	}
	if v < 4 {
		// v4: when each thread was fetched, so an interrupted first sync
		// resumes instead of starting over
		if _, err := db.Exec(`ALTER TABLE threads ADD COLUMN fetched INTEGER NOT NULL DEFAULT 0`); err != nil && !strings.Contains(err.Error(), "duplicate column") {
			return err
		}
	}
	if v < 5 {
		// v5: messages stored empty (attachments, cards) are fetched again
		if _, err := db.Exec(`UPDATE spaces SET synced_to=0 WHERE name IN (SELECT DISTINCT space FROM chat_messages WHERE text='(a message with no text)')`); err != nil {
			return err
		}
		if _, err := db.Exec(`DELETE FROM chat_messages WHERE text='(a message with no text)'`); err != nil {
			return err
		}
	}
	if v < 6 {
		// v6: chat messages keep their files, so they can be downloaded;
		// spaces with files in them are fetched again to fill it in
		if _, err := db.Exec(`ALTER TABLE chat_messages ADD COLUMN files TEXT NOT NULL DEFAULT '[]'`); err != nil && !strings.Contains(err.Error(), "duplicate column") {
			return err
		}
		if _, err := db.Exec(`UPDATE spaces SET synced_to=0 WHERE name IN (SELECT DISTINCT space FROM chat_messages WHERE text LIKE '%📎%')`); err != nil {
			return err
		}
	}
	if v < 7 {
		// v7: lookups that failed (a person's name, a DM's title), so they
		// aren't retried on every sync
		if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS tries (account TEXT NOT NULL, key TEXT NOT NULL, at INTEGER NOT NULL, PRIMARY KEY (account, key))`); err != nil {
			return err
		}
	}
	if v < 8 {
		// v8: the backfill of older mail, page by page
		if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS backfill (account TEXT PRIMARY KEY, page TEXT NOT NULL DEFAULT '',
		  done INTEGER NOT NULL DEFAULT 0, total INTEGER NOT NULL DEFAULT 0, query TEXT NOT NULL DEFAULT '')`); err != nil {
			return err
		}
	}
	if v < 9 {
		// v9: your own name for a chat (only on this machine)
		if _, err := db.Exec(`ALTER TABLE spaces ADD COLUMN alias TEXT NOT NULL DEFAULT ''`); err != nil && !strings.Contains(err.Error(), "duplicate column") {
			return err
		}
	}
	if v < 10 {
		// v10: quote-replies and whether a space uses threads; recent
		// conversations are fetched again to fill them in
		for _, q := range []string{
			`ALTER TABLE chat_messages ADD COLUMN quoted TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE spaces ADD COLUMN threaded INTEGER NOT NULL DEFAULT 0`,
		} {
			if _, err := db.Exec(q); err != nil && !strings.Contains(err.Error(), "duplicate column") {
				return err
			}
		}
		if _, err := db.Exec(`UPDATE spaces SET synced_to=0 WHERE last_active > ?`, time.Now().AddDate(0, 0, -14).UnixMilli()); err != nil {
			return err
		}
	}
	if v < 11 {
		// v11: reactions and edits on chat messages; spaces you pinned
		for _, q := range []string{
			`ALTER TABLE chat_messages ADD COLUMN reactions TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE chat_messages ADD COLUMN edited INTEGER NOT NULL DEFAULT 0`,
			`ALTER TABLE spaces ADD COLUMN pinned INTEGER NOT NULL DEFAULT 0`,
		} {
			if _, err := db.Exec(q); err != nil && !strings.Contains(err.Error(), "duplicate column") {
				return err
			}
		}
		if _, err := db.Exec(`UPDATE spaces SET synced_to=0 WHERE last_active > ?`, time.Now().AddDate(0, 0, -7).UnixMilli()); err != nil {
			return err
		}
	}
	_, err := db.Exec(fmt.Sprintf(`PRAGMA user_version=%d`, schemaVersion))
	return err
}

func (s *Store) Close() error { return s.db.Close() }

func now() int64 { return time.Now().UnixMilli() }

// ---------------------------------------------------------------- accounts

type Account struct {
	Name       string `json:"name"`
	Email      string `json:"email"`
	Chat       bool   `json:"chat"`
	HistoryID  string `json:"-"`
	ChatUser   string `json:"-"`
	MailSynced int64  `json:"mail_synced"`
	ChatSynced int64  `json:"chat_synced"`
	LastError  string `json:"last_error,omitempty"`
}

func (s *Store) Accounts() ([]Account, error) {
	rows, err := s.db.Query(`SELECT name,email,chat,history_id,chat_user,mail_synced,chat_synced,last_error FROM accounts ORDER BY position, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Account
	for rows.Next() {
		var a Account
		if err := rows.Scan(&a.Name, &a.Email, &a.Chat, &a.HistoryID, &a.ChatUser, &a.MailSynced, &a.ChatSynced, &a.LastError); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) Account(name string) (Account, error) {
	all, err := s.Accounts()
	if err != nil {
		return Account{}, err
	}
	for _, a := range all {
		if a.Name == name {
			return a, nil
		}
	}
	return Account{}, fmt.Errorf("no account %q (sandesh auth list)", name)
}

func (s *Store) AddAccount(a Account) error {
	_, err := s.db.Exec(`INSERT INTO accounts(name,email,chat,position) VALUES(?,?,?,(SELECT COALESCE(MAX(position),0)+1 FROM accounts))
	  ON CONFLICT(name) DO UPDATE SET email=excluded.email, chat=excluded.chat`, a.Name, a.Email, a.Chat)
	return err
}

func (s *Store) RemoveAccount(name string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, t := range []string{"accounts WHERE name=?", "threads WHERE account=?", "messages WHERE account=?", "messages_fts WHERE account=?",
		"section_threads WHERE account=?", "contacts WHERE account=?", "labels WHERE account=?", "spaces WHERE account=?",
		"chat_messages WHERE account=?", "users WHERE account=?", "notified WHERE account=?"} {
		if _, err := tx.Exec("DELETE FROM "+t, name); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) SetHistory(account, historyID string) error {
	_, err := s.db.Exec(`UPDATE accounts SET history_id=?, mail_synced=?, last_error='' WHERE name=?`, historyID, now(), account)
	return err
}

func (s *Store) SetChatSynced(account, chatUser string) error {
	_, err := s.db.Exec(`UPDATE accounts SET chat_synced=?, chat_user=CASE WHEN ?='' THEN chat_user ELSE ? END WHERE name=?`, now(), chatUser, chatUser, account)
	return err
}

func (s *Store) SetError(account, msg string) error {
	_, err := s.db.Exec(`UPDATE accounts SET last_error=? WHERE name=?`, msg, account)
	return err
}

// ------------------------------------------------------------------- mail

type Attachment struct {
	Name string `json:"name"`
	Type string `json:"type"`
	Size int    `json:"size"`
}

type Message struct {
	Account     string       `json:"account"`
	ID          string       `json:"id"`
	ThreadID    string       `json:"thread_id"`
	FromAddr    string       `json:"from"`
	FromName    string       `json:"from_name"`
	To          string       `json:"to"`
	Cc          string       `json:"cc,omitempty"`
	ReplyTo     string       `json:"reply_to,omitempty"`
	Subject     string       `json:"subject"`
	Date        int64        `json:"date"`
	Labels      []string     `json:"labels"`
	Body        string       `json:"body"`
	HTML        string       `json:"-"`
	MessageID   string       `json:"-"`
	References  string       `json:"-"`
	Attachments []Attachment `json:"attachments,omitempty"`
}

type Thread struct {
	Account      string   `json:"account"`
	ID           string   `json:"id"`
	Subject      string   `json:"subject"`
	Snippet      string   `json:"snippet"`
	LastDate     int64    `json:"last_date"`
	Labels       []string `json:"labels"`
	Participants string   `json:"participants"`
	LastFrom     string   `json:"last_from"`
	Count        int      `json:"messages"`
}

func (t Thread) Has(label string) bool {
	for _, l := range t.Labels {
		if l == label {
			return true
		}
	}
	return false
}
func (t Thread) Unread() bool  { return t.Has("UNREAD") }
func (t Thread) Starred() bool { return t.Has("STARRED") }

func joinLabels(l []string) string { return "," + strings.Join(l, ",") + "," }
func splitLabels(s string) []string {
	s = strings.Trim(s, ",")
	if s == "" {
		return nil
	}
	return strings.Split(s, ",")
}

// PutThread replaces a thread and all its messages.
func (s *Store) PutThread(account string, t Thread, msgs []Message) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := deleteThreadTx(tx, account, t.ID); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO threads(account,id,subject,snippet,last_date,labels,participants,last_from,msg_count,fetched) VALUES(?,?,?,?,?,?,?,?,?,?)`,
		account, t.ID, t.Subject, t.Snippet, t.LastDate, joinLabels(t.Labels), t.Participants, t.LastFrom, len(msgs), now()); err != nil {
		return err
	}
	for _, m := range msgs {
		att, _ := json.Marshal(m.Attachments)
		if _, err := tx.Exec(`INSERT INTO messages(account,id,thread_id,from_addr,from_name,to_addrs,cc_addrs,reply_to,subject,date,labels,body,message_id,refs,attachments,body_html)
		  VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, account, m.ID, t.ID, m.FromAddr, m.FromName, m.To, m.Cc, m.ReplyTo, m.Subject, m.Date,
			joinLabels(m.Labels), m.Body, m.MessageID, m.References, string(att), m.HTML); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO messages_fts(account,id,thread_id,subject,from_text,body) VALUES(?,?,?,?,?,?)`,
			account, m.ID, t.ID, m.Subject, m.FromName+" "+m.FromAddr, m.Body); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func deleteThreadTx(tx *sql.Tx, account, id string) error {
	for _, q := range []string{`DELETE FROM threads WHERE account=? AND id=?`, `DELETE FROM messages WHERE account=? AND thread_id=?`,
		`DELETE FROM messages_fts WHERE account=? AND thread_id=?`} {
		if _, err := tx.Exec(q, account, id); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) DeleteThread(account, id string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := deleteThreadTx(tx, account, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM section_threads WHERE account=? AND thread_id=?`, account, id); err != nil {
		return err
	}
	return tx.Commit()
}

// SetThreadLabels changes labels locally, before (or instead of) the next
// sync, so the screen reacts at once.
func (s *Store) SetThreadLabels(account, id string, add, remove []string) error {
	t, err := s.Thread(account, id)
	if err != nil {
		return err
	}
	set := map[string]bool{}
	for _, l := range t.Labels {
		set[l] = true
	}
	for _, l := range add {
		set[l] = true
	}
	for _, l := range remove {
		delete(set, l)
	}
	var labels []string
	for l := range set {
		labels = append(labels, l)
	}
	_, err = s.db.Exec(`UPDATE threads SET labels=? WHERE account=? AND id=?`, joinLabels(labels), account, id)
	return err
}

const threadCols = `account,id,subject,snippet,last_date,labels,participants,last_from,msg_count`

func scanThreads(rows *sql.Rows) ([]Thread, error) {
	defer rows.Close()
	var out []Thread
	for rows.Next() {
		var t Thread
		var labels string
		if err := rows.Scan(&t.Account, &t.ID, &t.Subject, &t.Snippet, &t.LastDate, &labels, &t.Participants, &t.LastFrom, &t.Count); err != nil {
			return nil, err
		}
		t.Labels = splitLabels(labels)
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) Thread(account, id string) (Thread, error) {
	rows, err := s.db.Query(`SELECT `+threadCols+` FROM threads WHERE account=? AND id=?`, account, id)
	if err != nil {
		return Thread{}, err
	}
	ts, err := scanThreads(rows)
	if err != nil {
		return Thread{}, err
	}
	if len(ts) == 0 {
		return Thread{}, ErrNotFound
	}
	return ts[0], nil
}

// FindThread looks a thread id up across accounts (ids are unique enough).
func (s *Store) FindThread(id string) (Thread, error) {
	rows, err := s.db.Query(`SELECT `+threadCols+` FROM threads WHERE id=? LIMIT 1`, id)
	if err != nil {
		return Thread{}, err
	}
	ts, err := scanThreads(rows)
	if err != nil {
		return Thread{}, err
	}
	if len(ts) == 0 {
		return Thread{}, fmt.Errorf("no thread %q in the local copy (try sandesh sync)", id)
	}
	return ts[0], nil
}

var ErrNotFound = errors.New("not found")

type ThreadQuery struct {
	Accounts []string // empty: all
	Label    string
	Section  string // section name with a server query
	Since    int64  // last_date >= Since (ms)
	Unread   bool
	Text     string // local full-text search
	Limit    int
}

func (s *Store) Threads(q ThreadQuery) ([]Thread, error) {
	var where []string
	var args []any
	if len(q.Accounts) > 0 {
		where = append(where, "t.account IN ("+strings.TrimSuffix(strings.Repeat("?,", len(q.Accounts)), ",")+")")
		for _, a := range q.Accounts {
			args = append(args, a)
		}
	}
	if q.Label != "" {
		where = append(where, "t.labels LIKE ?")
		args = append(args, "%,"+q.Label+",%")
	}
	if q.Section != "" {
		where = append(where, "EXISTS (SELECT 1 FROM section_threads st WHERE st.account=t.account AND st.thread_id=t.id AND st.section=?)")
		args = append(args, q.Section)
	}
	if q.Since > 0 {
		where = append(where, "t.last_date >= ?")
		args = append(args, q.Since)
	}
	if q.Unread {
		where = append(where, "t.labels LIKE '%,UNREAD,%'")
	}
	if q.Text != "" {
		// one lookup in the index, not one per thread (that took seconds)
		where = append(where, "(t.account, t.id) IN (SELECT account, thread_id FROM messages_fts WHERE messages_fts MATCH ?)")
		args = append(args, ftsQuery(q.Text))
	}
	sqlq := `SELECT ` + strings.ReplaceAll("t."+strings.ReplaceAll(threadCols, ",", ",t."), " ", "") + ` FROM threads t`
	if len(where) > 0 {
		sqlq += " WHERE " + strings.Join(where, " AND ")
	}
	sqlq += " ORDER BY t.last_date DESC"
	if q.Limit > 0 {
		sqlq += fmt.Sprintf(" LIMIT %d", q.Limit)
	}
	rows, err := s.db.Query(sqlq, args...)
	if err != nil {
		return nil, err
	}
	return scanThreads(rows)
}

// ftsQuery turns free words into an FTS5 query: every word must match,
// as a prefix, so "relea priya" finds "release" from Priya.
func ftsQuery(text string) string {
	var parts []string
	for _, w := range strings.Fields(text) {
		w = strings.Map(func(r rune) rune {
			if r == '"' {
				return -1
			}
			return r
		}, w)
		if w != "" {
			parts = append(parts, `"`+w+`"*`)
		}
	}
	return strings.Join(parts, " ")
}

func (s *Store) Messages(account, threadID string) ([]Message, error) {
	rows, err := s.db.Query(`SELECT account,id,thread_id,from_addr,from_name,to_addrs,cc_addrs,reply_to,subject,date,labels,body,message_id,refs,attachments,body_html
	  FROM messages WHERE account=? AND thread_id=? ORDER BY date`, account, threadID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Message
	for rows.Next() {
		var m Message
		var labels, att string
		if err := rows.Scan(&m.Account, &m.ID, &m.ThreadID, &m.FromAddr, &m.FromName, &m.To, &m.Cc, &m.ReplyTo, &m.Subject, &m.Date, &labels,
			&m.Body, &m.MessageID, &m.References, &att, &m.HTML); err != nil {
			return nil, err
		}
		m.Labels = splitLabels(labels)
		_ = json.Unmarshal([]byte(att), &m.Attachments)
		out = append(out, m)
	}
	return out, rows.Err()
}

// AddSectionThreads adds threads to a search section without replacing it.
func (s *Store) AddSectionThreads(account, section string, ids []string) error {
	for _, id := range ids {
		if _, err := s.db.Exec(`INSERT OR IGNORE INTO section_threads(account,section,thread_id) VALUES(?,?,?)`, account, section, id); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) SetSectionThreads(account, section string, ids []string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM section_threads WHERE account=? AND section=?`, account, section); err != nil {
		return err
	}
	for _, id := range ids {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO section_threads(account,section,thread_id) VALUES(?,?,?)`, account, section, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// FetchedSince reports whether the thread was fetched after `since` (ms).
func (s *Store) FetchedSince(account, id string, since int64) bool {
	var n int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM threads WHERE account=? AND id=? AND fetched>?`, account, id, since).Scan(&n)
	return n > 0
}

// Backfill is how far the download of older mail has come.
type Backfill struct {
	Page  string // the next page of thread ids to look at
	Done  bool
	Total int    // threads in the mailbox (Gmail's count)
	Query string // what it covers ("" = everything); a new one restarts it
	Have  int    // threads in the local copy
}

func (s *Store) Backfill(account string) Backfill {
	var b Backfill
	_ = s.db.QueryRow(`SELECT page,done,total,query FROM backfill WHERE account=?`, account).Scan(&b.Page, &b.Done, &b.Total, &b.Query)
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM threads WHERE account=?`, account).Scan(&b.Have)
	return b
}

func (s *Store) SetBackfill(account string, b Backfill) error {
	_, err := s.db.Exec(`INSERT INTO backfill(account,page,done,total,query) VALUES(?,?,?,?,?)
	  ON CONFLICT(account) DO UPDATE SET page=excluded.page, done=excluded.done, total=excluded.total, query=excluded.query`,
		account, b.Page, b.Done, b.Total, b.Query)
	return err
}

// SentStatus is what happened after your last message in a thread.
type SentStatus struct {
	To        string // who your last message went to
	SentAt    int64
	ReplyFrom string // the first answer after it ("" = none yet)
	ReplyAt   int64
}

// SentStatuses looks up, for each thread, your last message and the first
// one from someone else after it: one query, no bodies.
func (s *Store) SentStatuses(account string, ids []string) map[string]SentStatus {
	out := map[string]SentStatus{}
	if len(ids) == 0 {
		return out
	}
	args := []any{account}
	ph := make([]string, len(ids))
	for i, id := range ids {
		ph[i] = "?"
		args = append(args, id)
	}
	rows, err := s.db.Query(`WITH mine AS (
	    SELECT thread_id, MAX(date) AS at FROM messages WHERE account=?1 AND labels LIKE '%,SENT,%' AND thread_id IN (`+strings.Join(ph, ",")+`)
	    GROUP BY thread_id)
	  SELECT m.thread_id, m.at,
	    (SELECT to_addrs FROM messages x WHERE x.account=?1 AND x.thread_id=m.thread_id AND x.date=m.at LIMIT 1),
	    COALESCE((SELECT CASE WHEN from_name<>'' THEN from_name ELSE from_addr END || char(9) || date FROM messages y
	      WHERE y.account=?1 AND y.thread_id=m.thread_id AND y.date>m.at AND y.labels NOT LIKE '%,SENT,%' ORDER BY y.date LIMIT 1), '')
	  FROM mine m`, args...)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var id, to, reply string
		var st SentStatus
		if rows.Scan(&id, &st.SentAt, &to, &reply) != nil {
			continue
		}
		st.To = to
		if name, at, ok := strings.Cut(reply, "\t"); ok {
			st.ReplyFrom = name
			fmt.Sscan(at, &st.ReplyAt)
		}
		out[id] = st
	}
	return out
}

// HasThread reports whether the thread is cached with this many messages.
func (s *Store) HasThread(account, id string) bool {
	var n int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM threads WHERE account=? AND id=?`, account, id).Scan(&n)
	return n > 0
}

// --------------------------------------------------------------- contacts

type Contact struct {
	Addr     string `json:"email"`
	Name     string `json:"name"`
	Sent     int    `json:"times_you_wrote"`
	Received int    `json:"times_they_wrote"`
	Last     int64  `json:"last"`
}

// NoteContact counts an address seen on mail. sent=true: you wrote to it.
func (s *Store) NoteContact(account, addr, name string, sent bool, when int64) error {
	addr = strings.ToLower(strings.TrimSpace(addr))
	if addr == "" {
		return nil
	}
	sentN, recvN := 0, 1
	if sent {
		sentN, recvN = 1, 0
	}
	_, err := s.db.Exec(`INSERT INTO contacts(account,addr,name,sent,received,last) VALUES(?,?,?,?,?,?)
	  ON CONFLICT(account,addr) DO UPDATE SET name=CASE WHEN excluded.name<>'' THEN excluded.name ELSE name END,
	  sent=sent+excluded.sent, received=received+excluded.received, last=MAX(last,excluded.last)`, account, addr, name, sentN, recvN, when)
	return err
}

func (s *Store) Contacts(account, query string, limit int) ([]Contact, error) {
	if limit <= 0 {
		limit = 10
	}
	like := "%" + strings.ToLower(query) + "%"
	rows, err := s.db.Query(`SELECT addr,name,sent,received,last FROM contacts WHERE account=? AND (addr LIKE ? OR LOWER(name) LIKE ?)
	  ORDER BY sent DESC, received DESC, last DESC LIMIT ?`, account, like, like, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Contact
	for rows.Next() {
		var c Contact
		if err := rows.Scan(&c.Addr, &c.Name, &c.Sent, &c.Received, &c.Last); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// SuggestContacts ranks contacts for autocomplete: a name word or the
// address starting with what was typed beats a match inside it; then
// people you write to, then people who write to you, then recent ones.
func (s *Store) SuggestContacts(account, query string, limit int, skip map[string]bool) ([]Contact, error) {
	q := strings.ToLower(strings.TrimSpace(query))
	all, err := s.Contacts(account, q, 300)
	if err != nil {
		return nil, err
	}
	type scored struct {
		c Contact
		s float64
	}
	var list []scored
	for _, c := range all {
		if skip[strings.ToLower(c.Addr)] || strings.HasPrefix(strings.ToLower(c.Addr), "noreply") || strings.Contains(strings.ToLower(c.Addr), "no-reply") {
			continue
		}
		sc := 0.0
		name, addr := strings.ToLower(c.Name), strings.ToLower(c.Addr)
		switch {
		case q == "":
		case strings.HasPrefix(name, q) || strings.HasPrefix(addr, q):
			sc += 30
		case strings.Contains(" "+name, " "+q) || strings.Contains(addr, "."+q):
			sc += 20
		default:
			sc += 5
		}
		sc += 8*math.Log1p(float64(c.Sent)) + 2*math.Log1p(float64(c.Received))
		if days := time.Since(time.UnixMilli(c.Last)).Hours() / 24; c.Last > 0 && days < 90 {
			sc += 4 * (1 - days/90)
		}
		list = append(list, scored{c, sc})
	}
	sort.SliceStable(list, func(i, j int) bool { return list[i].s > list[j].s })
	var out []Contact
	for _, x := range list {
		if len(out) == limit {
			break
		}
		out = append(out, x.c)
	}
	return out, nil
}

// PutContact remembers someone from the directory (no mail counted).
func (s *Store) PutContact(account, addr, name string) error {
	addr = strings.ToLower(strings.TrimSpace(addr))
	_, err := s.db.Exec(`INSERT INTO contacts(account,addr,name) VALUES(?,?,?)
	  ON CONFLICT(account,addr) DO UPDATE SET name=CASE WHEN contacts.name='' THEN excluded.name ELSE contacts.name END`, account, addr, name)
	return err
}

// KnownContact: you have written to this address before.
func (s *Store) KnownContact(account, addr string) bool {
	var n int
	_ = s.db.QueryRow(`SELECT sent FROM contacts WHERE account=? AND addr=?`, account, strings.ToLower(strings.TrimSpace(addr))).Scan(&n)
	return n > 0
}

func (s *Store) ResetContacts(account string) error {
	_, err := s.db.Exec(`DELETE FROM contacts WHERE account=?`, account)
	return err
}

// ----------------------------------------------------------------- labels

type Label struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"`
}

func (s *Store) PutLabels(account string, labels []Label) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM labels WHERE account=?`, account); err != nil {
		return err
	}
	for _, l := range labels {
		if _, err := tx.Exec(`INSERT INTO labels(account,id,name,type) VALUES(?,?,?,?)`, account, l.ID, l.Name, l.Type); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) Labels(account string) ([]Label, error) {
	rows, err := s.db.Query(`SELECT id,name,type FROM labels WHERE account=? ORDER BY type DESC, name`, account)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Label
	for rows.Next() {
		var l Label
		if err := rows.Scan(&l.ID, &l.Name, &l.Type); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// ------------------------------------------------------------------- chat

type Space struct {
	Account     string `json:"account"`
	Name        string `json:"id"`
	DisplayName string `json:"name"`
	Alias       string `json:"alias,omitempty"`    // your own name for it, on this machine
	Threaded    bool   `json:"threaded,omitempty"` // replies go in threads (else inline)
	Pinned      bool   `json:"pinned,omitempty"`   // kept on top of the list (on this machine)
	Type        string `json:"type"`               // SPACE, GROUP_CHAT, DIRECT_MESSAGE
	LastActive  int64  `json:"last_active"`
	LastRead    int64  `json:"-"`
	SyncedTo    int64  `json:"-"`
	Unread      int    `json:"unread"`
	LastText    string `json:"last_message"`
	LastSender  string `json:"last_sender"`
}

type ChatMessage struct {
	Account    string     `json:"-"`
	Name       string     `json:"id"`
	Space      string     `json:"space"`
	Thread     string     `json:"thread,omitempty"`
	Sender     string     `json:"-"`
	SenderName string     `json:"from"`
	Text       string     `json:"text"`
	Created    int64      `json:"time"`
	Mine       bool       `json:"mine,omitempty"`
	Files      []ChatFile `json:"files,omitempty"`
	Quoted     string     `json:"quoted,omitempty"`    // the message this one quotes (a reply)
	Reactions  string     `json:"reactions,omitempty"` // "👍 2 · 😂 1"
	Edited     bool       `json:"edited,omitempty"`
}

// ChatFile is a file in a chat message: Ref is Chat's media name (it can
// be downloaded), Drive a Drive file id (opened in the browser).
type ChatFile struct {
	Name  string `json:"name"`
	Type  string `json:"type,omitempty"`
	Ref   string `json:"ref,omitempty"`
	Drive string `json:"drive,omitempty"`
}

func (s *Store) PutSpace(sp Space) error {
	_, err := s.db.Exec(`INSERT INTO spaces(account,name,display_name,type,last_active,threaded) VALUES(?,?,?,?,?,?)
	  ON CONFLICT(account,name) DO UPDATE SET display_name=CASE WHEN excluded.display_name<>'' THEN excluded.display_name ELSE display_name END,
	  type=excluded.type, last_active=MAX(last_active,excluded.last_active), threaded=excluded.threaded`, sp.Account, sp.Name, sp.DisplayName, sp.Type, sp.LastActive, sp.Threaded)
	return err
}

// ChatHit is a chat message found by a search, with its space's title.
type ChatHit struct {
	ChatMessage
	SpaceTitle string
	SpaceType  string
}

// SearchChat finds chat messages whose text or sender contains every word
// of the query, newest first.
func (s *Store) SearchChat(accounts []string, query string, limit int, space string) ([]ChatHit, error) {
	words := strings.Fields(strings.ToLower(query))
	if len(words) == 0 {
		return nil, nil
	}
	var where []string
	var args []any
	for _, w := range words {
		where = append(where, "(LOWER(c.text) LIKE ? OR LOWER(c.sender_name) LIKE ?)")
		args = append(args, "%"+w+"%", "%"+w+"%")
	}
	if space != "" {
		where = append(where, "c.space=?")
		args = append(args, space)
	}
	if len(accounts) > 0 {
		where = append(where, "c.account IN (?"+strings.Repeat(",?", len(accounts)-1)+")")
		for _, a := range accounts {
			args = append(args, a)
		}
	}
	args = append(args, limit)
	rows, err := s.db.Query(`SELECT c.account,c.name,c.space,c.thread,c.sender,c.sender_name,c.text,c.created, c.sender=a.chat_user, sp.display_name, sp.type
	  FROM chat_messages c JOIN accounts a ON a.name=c.account LEFT JOIN spaces sp ON sp.account=c.account AND sp.name=c.space
	  WHERE `+strings.Join(where, " AND ")+` ORDER BY c.created DESC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ChatHit
	for rows.Next() {
		var h ChatHit
		var title, typ sql.NullString
		if err := rows.Scan(&h.Account, &h.Name, &h.Space, &h.Thread, &h.Sender, &h.SenderName, &h.Text, &h.Created, &h.Mine, &title, &typ); err != nil {
			return nil, err
		}
		h.SpaceTitle, h.SpaceType = title.String, typ.String
		out = append(out, h)
	}
	return out, rows.Err()
}

// ChatMessage returns one stored chat message.
func (s *Store) ChatMessage(account, name string) (ChatMessage, error) {
	var m ChatMessage
	var files string
	err := s.db.QueryRow(`SELECT account,name,space,thread,sender,sender_name,text,created,files,quoted,reactions,edited FROM chat_messages WHERE account=? AND name=?`, account, name).
		Scan(&m.Account, &m.Name, &m.Space, &m.Thread, &m.Sender, &m.SenderName, &m.Text, &m.Created, &files, &m.Quoted, &m.Reactions, &m.Edited)
	if err != nil {
		return m, err
	}
	_ = json.Unmarshal([]byte(files), &m.Files)
	return m, nil
}

// ResetDMTitles forgets DM and group titles so they are worked out again.
func (s *Store) ResetDMTitles(account string) error {
	_, err := s.db.Exec(`UPDATE spaces SET display_name='' WHERE account=? AND type<>'SPACE'`, account)
	return err
}

// MentionCounts: per chat, unread messages that mention you (@name).
func (s *Store) MentionCounts(account, name string) map[string]int {
	out := map[string]int{}
	name = strings.TrimSpace(name)
	if name == "" {
		return out
	}
	rows, err := s.db.Query(`SELECT c.space, COUNT(*) FROM chat_messages c JOIN spaces s ON s.account=c.account AND s.name=c.space
	  JOIN accounts a ON a.name=c.account
	  WHERE c.account=? AND c.created>s.last_read AND c.sender<>a.chat_user AND LOWER(c.text) LIKE ? GROUP BY c.space`,
		account, "%@"+strings.ToLower(name)+"%")
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var sp string
		var n int
		if rows.Scan(&sp, &n) == nil {
			out[sp] = n
		}
	}
	return out
}

// OldestChat is when the oldest stored message of a chat was sent.
func (s *Store) OldestChat(account, space string) (int64, error) {
	var t sql.NullInt64
	err := s.db.QueryRow(`SELECT MIN(created) FROM chat_messages WHERE account=? AND space=?`, account, space).Scan(&t)
	return t.Int64, err
}

// SetPinned keeps a chat on top of the list (on this machine).
func (s *Store) SetPinned(account, space string, on bool) error {
	_, err := s.db.Exec(`UPDATE spaces SET pinned=? WHERE account=? AND name=?`, on, account, space)
	return err
}

// EditChatText changes a stored message's text (after an edit).
func (s *Store) EditChatText(account, name, text string) error {
	_, err := s.db.Exec(`UPDATE chat_messages SET text=?, edited=1 WHERE account=? AND name=?`, text, account, name)
	return err
}

// DeleteChatMessage drops a deleted message from the local copy.
func (s *Store) DeleteChatMessage(account, name string) error {
	_, err := s.db.Exec(`DELETE FROM chat_messages WHERE account=? AND name=?`, account, name)
	return err
}

// SetReactions stores a message's reaction summary.
func (s *Store) SetReactions(account, name, reactions string) error {
	_, err := s.db.Exec(`UPDATE chat_messages SET reactions=? WHERE account=? AND name=?`, reactions, account, name)
	return err
}

// SetAlias names a chat on this machine only ("" removes the name).
func (s *Store) SetAlias(account, space, alias string) error {
	_, err := s.db.Exec(`UPDATE spaces SET alias=? WHERE account=? AND name=?`, alias, account, space)
	return err
}

// RenameSpace sets a DM or group's title (they have none of their own).
func (s *Store) RenameSpace(account, space, name string) error {
	_, err := s.db.Exec(`UPDATE spaces SET display_name=? WHERE account=? AND name=?`, name, account, space)
	return err
}

func (s *Store) SetSpaceRead(account, space string, when int64) error {
	_, err := s.db.Exec(`UPDATE spaces SET last_read=MAX(last_read,?) WHERE account=? AND name=?`, when, account, space)
	return err
}

// ForceSpaceRead sets the read time even backwards (marking unread).
// Stamp changes whenever any process writes to the database (the WAL
// file's size and time move on every commit): a cheap "anything new?".
func (s *Store) Stamp() string {
	st, err := os.Stat(s.path + "-wal")
	if err != nil {
		if st, err = os.Stat(s.path); err != nil {
			return ""
		}
	}
	return fmt.Sprint(st.Size(), st.ModTime().UnixNano())
}

// Tried notes a lookup that failed just now.
func (s *Store) Tried(account, key string) error {
	_, err := s.db.Exec(`INSERT INTO tries(account,key,at) VALUES(?,?,?) ON CONFLICT(account,key) DO UPDATE SET at=excluded.at`, account, key, now())
	return err
}

// TryAgain: the lookup never failed, or failed longer than `after` ago.
func (s *Store) TryAgain(account, key string, after time.Duration) bool {
	var at int64
	if err := s.db.QueryRow(`SELECT at FROM tries WHERE account=? AND key=?`, account, key).Scan(&at); err != nil {
		return true
	}
	return time.Since(time.UnixMilli(at)) > after
}

// LastOtherMessage is when someone else last wrote in the space (0: never).
func (s *Store) LastOtherMessage(account, space string) int64 {
	var t sql.NullInt64
	_ = s.db.QueryRow(`SELECT MAX(c.created) FROM chat_messages c JOIN accounts a ON a.name=c.account
	  WHERE c.account=? AND c.space=? AND c.sender<>a.chat_user`, account, space).Scan(&t)
	return t.Int64
}

func (s *Store) ForceSpaceRead(account, space string, when int64) error {
	_, err := s.db.Exec(`UPDATE spaces SET last_read=? WHERE account=? AND name=?`, when, account, space)
	return err
}

func (s *Store) SetSpaceSynced(account, space string, when int64) error {
	_, err := s.db.Exec(`UPDATE spaces SET synced_to=MAX(synced_to,?) WHERE account=? AND name=?`, when, account, space)
	return err
}

func (s *Store) PutChatMessages(msgs []ChatMessage) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, m := range msgs {
		files, _ := json.Marshal(m.Files)
		if m.Files == nil {
			files = []byte("[]")
		}
		if _, err := tx.Exec(`INSERT INTO chat_messages(account,name,space,thread,sender,sender_name,text,created,files,quoted,reactions,edited) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)
		  ON CONFLICT(account,name) DO UPDATE SET text=excluded.text, files=excluded.files, quoted=excluded.quoted, reactions=excluded.reactions, edited=excluded.edited,
		    sender_name=CASE WHEN excluded.sender_name<>'' THEN excluded.sender_name ELSE sender_name END`,
			m.Account, m.Name, m.Space, m.Thread, m.Sender, m.SenderName, m.Text, m.Created, string(files), m.Quoted, m.Reactions, m.Edited); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Spaces lists spaces, most recently active first, with unread counts
// (messages from others after the space's read time).
func (s *Store) Spaces(accounts []string, me map[string]string) ([]Space, error) {
	q := `SELECT s.account,s.name,s.display_name,s.alias,s.threaded,s.pinned,s.type,s.last_active,s.last_read,s.synced_to,
	  (SELECT COUNT(*) FROM chat_messages c JOIN accounts a ON a.name=c.account WHERE c.account=s.account AND c.space=s.name AND c.created>s.last_read AND c.sender<>a.chat_user),
	  COALESCE((SELECT c.text FROM chat_messages c WHERE c.account=s.account AND c.space=s.name ORDER BY c.created DESC LIMIT 1),''),
	  COALESCE((SELECT CASE WHEN c.sender<>'' AND c.sender=(SELECT chat_user FROM accounts WHERE name=s.account) THEN 'You' ELSE c.sender_name END
	    FROM chat_messages c WHERE c.account=s.account AND c.space=s.name ORDER BY c.created DESC LIMIT 1),'')
	  FROM spaces s`
	var args []any
	if len(accounts) > 0 {
		q += " WHERE s.account IN (" + strings.TrimSuffix(strings.Repeat("?,", len(accounts)), ",") + ")"
		for _, a := range accounts {
			args = append(args, a)
		}
	}
	q += " ORDER BY s.last_active DESC"
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Space
	for rows.Next() {
		var sp Space
		if err := rows.Scan(&sp.Account, &sp.Name, &sp.DisplayName, &sp.Alias, &sp.Threaded, &sp.Pinned, &sp.Type, &sp.LastActive, &sp.LastRead, &sp.SyncedTo, &sp.Unread, &sp.LastText, &sp.LastSender); err != nil {
			return nil, err
		}
		out = append(out, sp)
	}
	return out, rows.Err()
}

func (s *Store) Space(account, name string) (Space, error) {
	all, err := s.Spaces([]string{account}, nil)
	if err != nil {
		return Space{}, err
	}
	for _, sp := range all {
		if sp.Name == name {
			return sp, nil
		}
	}
	return Space{}, ErrNotFound
}

func (s *Store) ChatMessages(account, space string, limit int) ([]ChatMessage, error) {
	if limit <= 0 {
		limit = 200
	}
	rows, err := s.db.Query(`SELECT c.account,c.name,c.space,c.thread,c.sender,c.sender_name,c.text,c.created, c.sender=a.chat_user, c.files, c.quoted, c.reactions, c.edited
	  FROM chat_messages c JOIN accounts a ON a.name=c.account WHERE c.account=? AND c.space=? ORDER BY c.created DESC LIMIT ?`, account, space, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ChatMessage
	for rows.Next() {
		var m ChatMessage
		var files string
		if err := rows.Scan(&m.Account, &m.Name, &m.Space, &m.Thread, &m.Sender, &m.SenderName, &m.Text, &m.Created, &m.Mine, &files, &m.Quoted, &m.Reactions, &m.Edited); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(files), &m.Files)
		out = append(out, m)
	}
	// oldest first
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, rows.Err()
}

// ChatUser is someone you've seen in chat.
type ChatUser struct{ ID, Name, Email string }

// ChatUsers finds people from chat by name or address.
func (s *Store) ChatUsers(account, query string, limit int) ([]ChatUser, error) {
	like := "%" + strings.ToLower(strings.TrimSpace(query)) + "%"
	rows, err := s.db.Query(`SELECT u.id,u.name,u.email FROM users u JOIN accounts a ON a.name=u.account
	  WHERE u.account=? AND u.id<>a.chat_user AND u.name<>'' AND (LOWER(u.name) LIKE ? OR LOWER(u.email) LIKE ?) ORDER BY u.name LIMIT ?`,
		account, like, like, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ChatUser
	for rows.Next() {
		var u ChatUser
		if err := rows.Scan(&u.ID, &u.Name, &u.Email); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func (s *Store) User(account, id string) (name, email string, ok bool) {
	err := s.db.QueryRow(`SELECT name,email FROM users WHERE account=? AND id=?`, account, id).Scan(&name, &email)
	return name, email, err == nil
}

func (s *Store) PutUser(account, id, name, email string) error {
	_, err := s.db.Exec(`INSERT INTO users(account,id,name,email) VALUES(?,?,?,?) ON CONFLICT(account,id) DO UPDATE SET name=excluded.name, email=excluded.email`,
		account, id, name, email)
	return err
}

// FixSenderNames fills sender names learned after their messages arrived.
func (s *Store) FixSenderNames(account string) error {
	_, err := s.db.Exec(`UPDATE chat_messages SET sender_name=(SELECT name FROM users u WHERE u.account=chat_messages.account AND u.id=chat_messages.sender)
	  WHERE account=? AND EXISTS (SELECT 1 FROM users u WHERE u.account=chat_messages.account AND u.id=chat_messages.sender AND u.name<>'')`, account)
	return err
}

// ------------------------------------------------------------------ notify

// FirstNotify records key and reports whether it was new.
func (s *Store) FirstNotify(account, key string) bool {
	res, err := s.db.Exec(`INSERT OR IGNORE INTO notified(account,key) VALUES(?,?)`, account, key)
	if err != nil {
		return false
	}
	n, _ := res.RowsAffected()
	return n == 1
}
