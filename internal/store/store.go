package store

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	_ "modernc.org/sqlite"
	"smsbot-server/internal/media"
	"smsbot-server/internal/vault"
	"time"
)

type Store struct {
	DB    *sql.DB
	Vault *vault.Vault
	Path  string
}
type Job struct {
	ID                 string   `json:"id"`
	ConversationID     string   `json:"conversation_id"`
	Command            string   `json:"command"`
	State              string   `json:"state"`
	CreatedAt          string   `json:"created_at"`
	TransactionID      string   `json:"transaction_id,omitempty"`
	Code               string   `json:"code,omitempty"`
	Model              string   `json:"model,omitempty"`
	PromptTokens       int      `json:"prompt_tokens,omitempty"`
	CompletionTokens   int      `json:"completion_tokens,omitempty"`
	ReasoningTokens    int      `json:"reasoning_tokens,omitempty"`
	Cost               *float64 `json:"cost,omitempty"`
	RequestedReasoning string   `json:"requested_reasoning,omitempty"`
}
type Plan struct {
	Attachments    []media.File    `json:"attachments,omitempty"`
	Reply          string          `json:"reply"`
	Participant    string          `json:"participant"`
	Input          string          `json:"input,omitempty"`
	Instructions   string          `json:"instructions,omitempty"`
	Model          string          `json:"model,omitempty"`
	Generation     json.RawMessage `json:"generation,omitempty"`
	Memory         bool            `json:"memory,omitempty"`
	MemoryHours    int             `json:"memory_hours,omitempty"`
	MemoryEpoch    int64           `json:"memory_epoch,omitempty"`
	Context        []Turn          `json:"context,omitempty"`
	Approval       bool            `json:"approval,omitempty"`
	Approved       bool            `json:"approved,omitempty"`
	MaxChars       int             `json:"max_chars,omitempty"`
	Manual         bool            `json:"manual,omitempty"`
	InboxOwnerOnly bool            `json:"inbox_owner_only,omitempty"`
}

func ID() string {
	b := make([]byte, 16)
	if _, e := rand.Read(b); e != nil {
		panic(e)
	}
	return hex.EncodeToString(b)
}

func TransactionID() string {
	id := ID()
	return id[:8] + "-" + id[8:12] + "-4" + id[13:16] + "-8" + id[17:20] + "-" + id[20:]
}
func Hash(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
func Open(path string, v *vault.Vault) (*Store, error) {
	db, e := sql.Open("sqlite", path)
	if e != nil {
		return nil, e
	}
	db.SetMaxOpenConns(1)
	s := &Store{DB: db, Vault: v, Path: path}
	if e = s.migrate(); e != nil {
		db.Close()
		return nil, e
	}
	// Bearer client credentials are no longer accepted; drop any saved by older versions.
	if _, e = db.Exec("DELETE FROM credentials WHERE kind='client'"); e != nil {
		db.Close()
		return nil, e
	}
	return s, nil
}
func (s *Store) migrate() error {
	if _, e := s.DB.Exec(`PRAGMA journal_mode=WAL; PRAGMA busy_timeout=5000; PRAGMA foreign_keys=ON; PRAGMA synchronous=FULL;`); e != nil {
		return e
	}
	var version int
	if e := s.DB.QueryRow("PRAGMA user_version").Scan(&version); e != nil {
		return e
	}
	if version > 5 {
		return errors.New("database is newer than this service")
	}
	if version == 5 {
		return nil
	}
	if version == 4 {
		tx, err := s.DB.Begin()
		if err != nil {
			return err
		}
		defer tx.Rollback()
		_, err = tx.Exec(`
CREATE TABLE IF NOT EXISTS observed_events(id TEXT PRIMARY KEY);
CREATE TABLE IF NOT EXISTS archive_pending(conversation TEXT PRIMARY KEY);
INSERT OR IGNORE INTO archive_pending SELECT DISTINCT m.conversation FROM messages m JOIN message_meta meta ON meta.message=m.id WHERE meta.owner_only=1;
CREATE TABLE IF NOT EXISTS archive_conversations(conversation TEXT PRIMARY KEY,payload TEXT NOT NULL,retention_days INTEGER NOT NULL DEFAULT 0);
CREATE TABLE IF NOT EXISTS message_revisions(message TEXT NOT NULL REFERENCES messages(id) ON DELETE CASCADE,revision INTEGER NOT NULL,payload TEXT NOT NULL,observed TEXT NOT NULL,PRIMARY KEY(message,revision));
CREATE TABLE IF NOT EXISTS message_changes(seq INTEGER PRIMARY KEY AUTOINCREMENT,conversation TEXT NOT NULL,message TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS messages_conversation_time ON messages(conversation,at DESC,id DESC);
CREATE INDEX IF NOT EXISTS changes_conversation_seq ON message_changes(conversation,seq);
CREATE TRIGGER IF NOT EXISTS message_insert_change AFTER INSERT ON messages BEGIN INSERT INTO message_changes(conversation,message) VALUES(new.conversation,new.id); END;
CREATE TRIGGER IF NOT EXISTS message_delete_change AFTER DELETE ON messages BEGIN INSERT INTO message_changes(conversation,message) VALUES(old.conversation,old.id); END;
CREATE TRIGGER IF NOT EXISTS message_update_change AFTER UPDATE ON messages WHEN old.payload!=new.payload BEGIN INSERT INTO message_changes(conversation,message) VALUES(new.conversation,new.id); END;
CREATE TRIGGER IF NOT EXISTS message_status_change AFTER UPDATE ON message_meta WHEN old.status!=new.status BEGIN INSERT INTO message_changes(conversation,message) SELECT conversation,id FROM messages WHERE id=new.message; END;
CREATE TRIGGER IF NOT EXISTS message_files_change AFTER INSERT ON message_attachments BEGIN INSERT INTO message_changes(conversation,message) SELECT conversation,id FROM messages WHERE id=new.message; END;
CREATE TRIGGER IF NOT EXISTS message_files_update AFTER UPDATE ON message_attachments WHEN old.payload!=new.payload BEGIN INSERT INTO message_changes(conversation,message) SELECT conversation,id FROM messages WHERE id=new.message; END;
PRAGMA user_version=5;`)
		if err != nil {
			return err
		}
		return tx.Commit()
	}
	if version == 3 {
		tx, err := s.DB.Begin()
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if _, err = tx.Exec(`CREATE TABLE IF NOT EXISTS message_attachments(message TEXT PRIMARY KEY REFERENCES messages(id) ON DELETE CASCADE,payload TEXT NOT NULL); PRAGMA user_version=4;`); err != nil {
			return err
		}
		if err = tx.Commit(); err != nil {
			return err
		}
		return s.migrate()
	}
	if version == 2 {
		tx, e := s.DB.Begin()
		if e != nil {
			return e
		}
		defer tx.Rollback()
		_, e = tx.Exec(`CREATE TABLE IF NOT EXISTS message_meta(message TEXT PRIMARY KEY REFERENCES messages(id) ON DELETE CASCADE,origin TEXT NOT NULL,status TEXT NOT NULL,owner_only INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS message_alias(alias TEXT PRIMARY KEY,message TEXT NOT NULL REFERENCES messages(id) ON DELETE CASCADE);
PRAGMA user_version=3;`)
		if e != nil {
			return e
		}
		if e = tx.Commit(); e != nil {
			return e
		}
		return s.migrate()
	}
	tx, e := s.DB.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if version == 1 {
		rows, err := tx.Query("SELECT id,at FROM messages")
		if err != nil {
			return err
		}
		type stamp struct{ id, at string }
		var stamps []stamp
		for rows.Next() {
			var id, at string
			if err = rows.Scan(&id, &at); err != nil {
				rows.Close()
				return err
			}
			parsed, err := time.Parse(time.RFC3339Nano, at)
			if err != nil {
				rows.Close()
				return errors.New("invalid saved message timestamp")
			}
			stamps = append(stamps, stamp{id, parsed.UTC().Format("2006-01-02T15:04:05.000000000Z")})
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		for _, v := range stamps {
			if _, err = tx.Exec("UPDATE messages SET at=? WHERE id=?", v.at, v.id); err != nil {
				return err
			}
		}
		if _, e = tx.Exec(`CREATE TABLE usage(job TEXT PRIMARY KEY,model TEXT NOT NULL,prompt_tokens INTEGER NOT NULL,completion_tokens INTEGER NOT NULL,reasoning_tokens INTEGER NOT NULL,cost REAL); CREATE TABLE memory(scope TEXT PRIMARY KEY,payload TEXT NOT NULL,expires TEXT NOT NULL); PRAGMA user_version=2;`); e != nil {
			return e
		}
		if e = tx.Commit(); e != nil {
			return e
		}
		return s.migrate()
	}
	_, e = tx.Exec(`
 CREATE TABLE settings(key TEXT PRIMARY KEY,value TEXT NOT NULL);
 INSERT INTO settings VALUES('paused','true');
 CREATE TABLE enrollment(conversation TEXT PRIMARY KEY,participant TEXT NOT NULL);
 CREATE TABLE jobs(id TEXT PRIMARY KEY,conversation TEXT NOT NULL,command TEXT NOT NULL,state TEXT NOT NULL,created TEXT NOT NULL,txn TEXT NOT NULL DEFAULT '',code TEXT NOT NULL DEFAULT '',plan TEXT NOT NULL);
 CREATE TABLE credentials(hash TEXT PRIMARY KEY,label TEXT NOT NULL,kind TEXT NOT NULL,expires INTEGER NOT NULL);
 CREATE TABLE messages(id TEXT PRIMARY KEY,conversation TEXT NOT NULL,direction TEXT NOT NULL,at TEXT NOT NULL,payload TEXT NOT NULL);
 CREATE TABLE usage(job TEXT PRIMARY KEY,model TEXT NOT NULL,prompt_tokens INTEGER NOT NULL,completion_tokens INTEGER NOT NULL,reasoning_tokens INTEGER NOT NULL,cost REAL);
 CREATE TABLE memory(scope TEXT PRIMARY KEY,payload TEXT NOT NULL,expires TEXT NOT NULL);
 PRAGMA user_version=2;`)
	if e != nil {
		return e
	}
	if e = tx.Commit(); e != nil {
		return e
	}
	return s.migrate()
}
func (s *Store) Paused() (bool, error) {
	var value string
	e := s.DB.QueryRow("SELECT value FROM settings WHERE key='paused'").Scan(&value)
	return value != "false", e
}
func (s *Store) Pause(value bool) error {
	tx, e := s.DB.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if _, e = tx.Exec("UPDATE settings SET value=? WHERE key='paused'", fmt.Sprint(value)); e != nil {
		return e
	}
	if value {
		if _, e = tx.Exec("UPDATE jobs SET state='cancelled',code='paused',plan='' WHERE state IN ('queued','generating','awaiting_approval')"); e != nil {
			return e
		}
	}
	return tx.Commit()
}
func (s *Store) Enroll(conv, peer string) error {
	_, e := s.DB.Exec("INSERT INTO enrollment VALUES(?,?) ON CONFLICT(conversation) DO UPDATE SET participant=excluded.participant", conv, peer)
	return e
}
func (s *Store) Unenroll(conv string) error {
	tx, e := s.DB.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if _, e = tx.Exec("DELETE FROM enrollment WHERE conversation=?", conv); e != nil {
		return e
	}
	if _, e = tx.Exec("UPDATE jobs SET state='cancelled',code='unenrolled',plan='' WHERE conversation=? AND state IN ('queued','generating','awaiting_approval')", conv); e != nil {
		return e
	}
	return tx.Commit()
}
func (s *Store) Enrolled(conv, peer string) bool {
	var actual string
	return s.DB.QueryRow("SELECT participant FROM enrollment WHERE conversation=?", conv).Scan(&actual) == nil && actual == peer
}
func (s *Store) Enqueue(id, conv, command string, p Plan) (bool, error) {
	b, e := json.Marshal(p)
	if e != nil {
		return false, e
	}
	enc, e := s.Vault.Seal("plan:"+id, b)
	if e != nil {
		return false, e
	}
	state := "queued"
	if p.Approval && !p.Approved {
		state = "awaiting_approval"
	}
	tx, e := s.DB.Begin()
	if e != nil {
		return false, e
	}
	defer tx.Rollback()
	r, e := tx.Exec("INSERT OR IGNORE INTO jobs(id,conversation,command,state,created,plan) VALUES(?,?,?,?,?,?)", id, conv, command, state, time.Now().UTC().Format(time.RFC3339Nano), enc)
	if e != nil {
		return false, e
	}
	n, e := r.RowsAffected()
	if e != nil {
		return false, e
	}
	if n == 1 && p.InboxOwnerOnly {
		digest, err := s.Vault.Seal("compose-intent:"+id, []byte(media.Intent(p.Reply, p.Attachments)))
		if err != nil {
			return false, err
		}
		if _, err = tx.Exec("INSERT INTO settings VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", "compose_intent:"+id, digest); err != nil {
			return false, err
		}
	}
	return n == 1, tx.Commit()
}
func (s *Store) Next() (Job, Plan, error) {
	var j Job
	var enc string
	e := s.DB.QueryRow("SELECT id,conversation,command,state,created,txn,code,plan FROM jobs WHERE state='queued' ORDER BY created LIMIT 1").Scan(&j.ID, &j.ConversationID, &j.Command, &j.State, &j.CreatedAt, &j.TransactionID, &j.Code, &enc)
	if e != nil {
		return j, Plan{}, e
	}
	b, e := s.Vault.Open("plan:"+j.ID, enc)
	if e != nil {
		return j, Plan{}, e
	}
	var p Plan
	e = json.Unmarshal(b, &p)
	return j, p, e
}
func (s *Store) Submitting(id, txn string) error {
	r, e := s.DB.Exec("UPDATE jobs SET state='submitting',txn=? WHERE id=? AND state='queued'", txn, id)
	if e != nil {
		return e
	}
	n, e := r.RowsAffected()
	if n != 1 {
		return errors.New("job is no longer queued")
	}
	return e
}
func (s *Store) Complete(id, state, code string) error {
	_, e := s.DB.Exec("UPDATE jobs SET state=?,code=?,plan='' WHERE id=?", state, code, id)
	return e
}
func (s *Store) Recover() error {
	_, e := s.DB.Exec("UPDATE jobs SET state=CASE WHEN state='submitting' THEN 'unknown_submission' ELSE 'cancelled' END,code='restart_interrupted',plan='' WHERE state IN ('submitting','generating','awaiting_approval')")
	return e
}
func (s *Store) Jobs() ([]Job, error) {
	rows, e := s.DB.Query("SELECT j.id,j.conversation,j.command,j.state,j.created,j.txn,j.code,coalesce(u.model,''),coalesce(u.prompt_tokens,0),coalesce(u.completion_tokens,0),coalesce(u.reasoning_tokens,0),u.cost,coalesce((SELECT json_extract(value,'$') FROM settings WHERE key='requested_reasoning:'||j.id),'') FROM jobs j LEFT JOIN usage u ON u.job=j.id ORDER BY j.created DESC LIMIT 200")
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	list := []Job{}
	for rows.Next() {
		var j Job
		if e = rows.Scan(&j.ID, &j.ConversationID, &j.Command, &j.State, &j.CreatedAt, &j.TransactionID, &j.Code, &j.Model, &j.PromptTokens, &j.CompletionTokens, &j.ReasoningTokens, &j.Cost, &j.RequestedReasoning); e != nil {
			return nil, e
		}
		list = append(list, j)
	}
	return list, rows.Err()
}
func (s *Store) AddCredential(token, label, kind string, expires int64) error {
	_, e := s.DB.Exec("INSERT INTO credentials VALUES(?,?,?,?)", Hash(token), label, kind, expires)
	return e
}
func (s *Store) Credential(token, kind string) bool {
	var expires int64
	e := s.DB.QueryRow("SELECT expires FROM credentials WHERE hash=? AND kind=?", Hash(token), kind).Scan(&expires)
	return e == nil && (expires == 0 || expires > time.Now().Unix())
}

// SessionExpiry reports a live browser session's expiry; 0 means it never expires.
func (s *Store) SessionExpiry(token string) (int64, bool) {
	var expires int64
	e := s.DB.QueryRow("SELECT expires FROM credentials WHERE hash=? AND kind='session'", Hash(token)).Scan(&expires)
	return expires, e == nil && (expires == 0 || expires > time.Now().Unix())
}
func (s *Store) RevokeSessions() error {
	_, e := s.DB.Exec("DELETE FROM credentials WHERE kind='session'")
	return e
}
func (s *Store) SaveMessage(id, conv, direction, text string) error {
	return s.SaveMessageAt(id, conv, direction, text, time.Now())
}
func (s *Store) SaveMessageAt(id, conv, direction, text string, at time.Time) error {
	enc, e := s.Vault.Seal("message:"+id, []byte(text))
	if e != nil {
		return e
	}
	_, e = s.DB.Exec("INSERT OR IGNORE INTO messages VALUES(?,?,?,?,?)", id, conv, direction, at.UTC().Format("2006-01-02T15:04:05.000000000Z"), enc)
	return e
}

type Message struct {
	Revision    int          `json:"revision,omitempty"`
	Attachments []media.Info `json:"attachments,omitempty"`
	ID          string       `json:"id"`
	Direction   string       `json:"direction"`
	At          string       `json:"at"`
	Text        string       `json:"text"`
	Origin      string       `json:"origin,omitempty"`
	Status      string       `json:"status,omitempty"`
}

func (s *Store) Messages(conv string) ([]Message, error) {
	rows, e := s.DB.Query("SELECT id,direction,at,payload FROM messages WHERE conversation=? AND NOT EXISTS(SELECT 1 FROM message_meta WHERE message=id AND owner_only=1) ORDER BY at DESC LIMIT 100", conv)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Message{}
	for rows.Next() {
		var m Message
		var enc string
		if e = rows.Scan(&m.ID, &m.Direction, &m.At, &enc); e != nil {
			return nil, e
		}
		b, e := s.Vault.Open("message:"+m.ID, enc)
		if e != nil {
			return nil, e
		}
		m.Text = string(b)
		out = append(out, m)
	}
	return out, rows.Err()
}
