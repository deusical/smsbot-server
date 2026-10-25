package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

var ErrAIThreadUnavailable = errors.New("ai_thread_unavailable")

// AIThreadPlan is part of the sealed immutable job plan. Jobs own send status.
type AIThreadPlan struct {
	ID            string `json:"id"`
	Parent        string `json:"parent,omitempty"`
	RetentionDays int    `json:"retention_days"`
	ReplyWire     string `json:"reply_wire,omitempty"`
}
type aiThread struct {
	Conversation string `json:"conversation"`
	Participant  string `json:"participant"`
	Command      string `json:"command"`
	Epoch        int64  `json:"epoch"`
}
type aiExchange struct {
	Prompt      string `json:"prompt"`
	Answer      string `json:"answer,omitempty"`
	Owner       bool   `json:"owner,omitempty"`
	Model       string `json:"model,omitempty"`
	SubmittedAt string `json:"submitted_at,omitempty"`
}
type AIParent struct{ Exchange, Thread string }
type aiReader interface{ QueryRow(string, ...any) *sql.Row }

func (s *Store) migrateAIThreads() error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.Exec(`CREATE TABLE IF NOT EXISTS ai_threads(id TEXT PRIMARY KEY,chat_key TEXT NOT NULL,created_at TEXT NOT NULL,last_active_at TEXT NOT NULL,expires_at TEXT NOT NULL,payload TEXT NOT NULL);
 CREATE INDEX IF NOT EXISTS ai_thread_chat ON ai_threads(chat_key);
 CREATE TABLE IF NOT EXISTS ai_exchanges(id TEXT PRIMARY KEY,thread_id TEXT NOT NULL REFERENCES ai_threads(id) ON DELETE CASCADE,parent_id TEXT,created_at TEXT NOT NULL,outbound_key TEXT UNIQUE,payload TEXT NOT NULL,UNIQUE(thread_id,id),FOREIGN KEY(thread_id,parent_id) REFERENCES ai_exchanges(thread_id,id));
 CREATE TABLE IF NOT EXISTS ai_answer_refs(reply_key TEXT PRIMARY KEY,exchange_id TEXT NOT NULL REFERENCES ai_exchanges(id) ON DELETE CASCADE);
 PRAGMA user_version=7;`)
	if err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) sealAI(scope string, v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return s.Vault.Seal(scope, b)
}
func (s *Store) openAI(scope, payload string, v any) error {
	b, err := s.Vault.Open(scope, payload)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}
func aiExpiry(created, active time.Time, hours, days int) time.Time {
	return minTime(active.Add(time.Duration(hours)*time.Hour), created.AddDate(0, 0, days))
}
func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
func aiStamp(at time.Time) string { return at.UTC().Format(time.RFC3339Nano) }

// FindAIParent performs only a keyed local lookup, including when collection is off.
func (s *Store) FindAIParent(conv, wire string) (*AIParent, error) {
	var p AIParent
	err := s.DB.QueryRow("SELECT e.id,e.thread_id FROM ai_answer_refs r JOIN ai_exchanges e ON e.id=r.exchange_id JOIN ai_threads t ON t.id=e.thread_id WHERE r.reply_key=? AND t.chat_key=?", Hash(conv+"\x00"+wire), Hash(conv)).Scan(&p.Exchange, &p.Thread)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}
func (s *Store) validAIThread(q aiReader, id, conv, peer, command string, epoch int64, now time.Time, hours, days int) (aiThread, error) {
	var data aiThread
	var payload, created, active, expires string
	err := q.QueryRow("SELECT payload,created_at,last_active_at,expires_at FROM ai_threads WHERE id=? AND chat_key=?", id, Hash(conv)).Scan(&payload, &created, &active, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return data, ErrAIThreadUnavailable
	}
	if err != nil {
		return data, err
	}
	if err = s.openAI("ai-thread:"+id, payload, &data); err != nil {
		return data, err
	}
	var current int64
	err = q.QueryRow("SELECT coalesce((SELECT CAST(value AS INTEGER) FROM settings WHERE key=?),0)", "memory_epoch:"+Hash(conv)).Scan(&current)
	if err != nil {
		return data, err
	}
	c, ce := time.Parse(time.RFC3339Nano, created)
	a, ae := time.Parse(time.RFC3339Nano, active)
	x, xe := time.Parse(time.RFC3339Nano, expires)
	if ce != nil || ae != nil || xe != nil {
		return data, ErrAIThreadUnavailable
	}
	if data.Conversation != conv || data.Participant != peer || data.Command != command || data.Epoch != epoch || epoch != current || hours <= 0 || days <= 0 || !now.Before(minTime(x, aiExpiry(c, a, hours, days))) {
		return data, ErrAIThreadUnavailable
	}
	return data, nil
}
func (s *Store) CheckAIThread(conv, peer, command string, p Plan, now time.Time, hours, days int) error {
	if p.Thread == nil {
		return nil
	}
	_, err := s.validAIThread(s.DB, p.Thread.ID, conv, peer, command, p.MemoryEpoch, now, hours, days)
	return err
}

// AIHistory returns complete exchanges on this parent path, newest ones first
// selected within a conservative UTF-8-byte token upper estimate, then ordered.
func (s *Store) AIHistory(parent AIParent, conv, peer, command string, epoch int64, now time.Time, hours, days, budget int) ([]Turn, bool, error) {
	if _, err := s.validAIThread(s.DB, parent.Thread, conv, peer, command, epoch, now, hours, days); err != nil {
		return nil, false, err
	}
	pairs := [][2]Turn{}
	id := parent.Exchange
	omitted := false
	for id != "" {
		var payload string
		var next sql.NullString
		if err := s.DB.QueryRow("SELECT payload,parent_id FROM ai_exchanges WHERE id=? AND thread_id=?", id, parent.Thread).Scan(&payload, &next); err != nil {
			return nil, false, err
		}
		var x aiExchange
		if err := s.openAI("ai-exchange:"+id, payload, &x); err != nil {
			return nil, false, err
		}
		if x.Answer == "" {
			return nil, false, ErrAIThreadUnavailable
		}
		cost := len(x.Prompt) + len(x.Answer) + 32
		if cost > budget {
			omitted = true
			break
		}
		budget -= cost
		pairs = append(pairs, [2]Turn{{Role: "user", Content: x.Prompt}, {Role: "assistant", Content: x.Answer}})
		id = next.String
	}
	turns := make([]Turn, 0, len(pairs)*2)
	for i := len(pairs) - 1; i >= 0; i-- {
		turns = append(turns, pairs[i][:]...)
	}
	return turns, omitted, nil
}
func (s *Store) enqueueAIExchange(tx *sql.Tx, id, conv, command string, p Plan, now time.Time) error {
	if p.Thread.Parent == "" {
		var epoch int64
		if err := tx.QueryRow("SELECT coalesce((SELECT CAST(value AS INTEGER) FROM settings WHERE key=?),0)", "memory_epoch:"+Hash(conv)).Scan(&epoch); err != nil {
			return err
		}
		if epoch != p.MemoryEpoch {
			return ErrAIThreadUnavailable
		}
		payload, err := s.sealAI("ai-thread:"+p.Thread.ID, aiThread{conv, p.Participant, command, epoch})
		if err != nil {
			return err
		}
		if _, err = tx.Exec("INSERT INTO ai_threads VALUES(?,?,?,?,?,?)", p.Thread.ID, Hash(conv), aiStamp(now), aiStamp(now), aiStamp(aiExpiry(now, now, p.MemoryHours, p.Thread.RetentionDays)), payload); err != nil {
			return err
		}
	} else {
		if _, err := s.validAIThread(tx, p.Thread.ID, conv, p.Participant, command, p.MemoryEpoch, now, p.MemoryHours, p.Thread.RetentionDays); err != nil {
			return err
		}
		var found int
		if err := tx.QueryRow("SELECT count(*) FROM ai_exchanges e WHERE e.id=? AND e.thread_id=? AND EXISTS(SELECT 1 FROM ai_answer_refs r WHERE r.exchange_id=e.id)", p.Thread.Parent, p.Thread.ID).Scan(&found); err != nil {
			return err
		}
		if found != 1 {
			return ErrAIThreadUnavailable
		}
	}
	payload, err := s.sealAI("ai-exchange:"+id, aiExchange{Prompt: p.Input, Owner: p.Owner})
	if err != nil {
		return err
	}
	var parent any
	if p.Thread.Parent != "" {
		parent = p.Thread.Parent
	}
	_, err = tx.Exec("INSERT INTO ai_exchanges(id,thread_id,parent_id,created_at,payload) VALUES(?,?,?,?,?)", id, p.Thread.ID, parent, aiStamp(now), payload)
	return err
}
func (s *Store) prepareAIAnswer(tx *sql.Tx, id, txn string, p Plan, now time.Time) error {
	var conv, command string
	if err := tx.QueryRow("SELECT conversation,command FROM jobs WHERE id=?", id).Scan(&conv, &command); err != nil {
		return err
	}
	if _, err := s.validAIThread(tx, p.Thread.ID, conv, p.Participant, command, p.MemoryEpoch, now, p.MemoryHours, p.Thread.RetentionDays); err != nil {
		return err
	}
	var payload string
	if err := tx.QueryRow("SELECT payload FROM ai_exchanges WHERE id=? AND thread_id=?", id, p.Thread.ID).Scan(&payload); err != nil {
		return err
	}
	var x aiExchange
	if err := s.openAI("ai-exchange:"+id, payload, &x); err != nil {
		return err
	}
	x.Answer = p.Reply
	x.Model = p.Model
	sealed, err := s.sealAI("ai-exchange:"+id, x)
	if err != nil {
		return err
	}
	_, err = tx.Exec("UPDATE ai_exchanges SET outbound_key=?,payload=? WHERE id=?", Hash(conv+"\x00"+txn), sealed, id)
	return err
}

// RecordAIAnswer consumes exact outgoing transaction evidence, never matching text
// or assuming a client transaction ID is an upstream message ID.
func (s *Store) RecordAIAnswer(conv, txn, wire string, now time.Time, hours, days int) error {
	if txn == "" || wire == "" {
		return nil
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var id, thread, payload, state, command, peer string
	err = tx.QueryRow("SELECT e.id,e.thread_id,e.payload,j.state,j.command,n.participant FROM ai_exchanges e JOIN ai_threads t ON t.id=e.thread_id JOIN jobs j ON j.id=e.id JOIN enrollment n ON n.conversation=j.conversation WHERE e.outbound_key=? AND j.conversation=? AND j.txn=?", Hash(conv+"\x00"+txn), conv, txn).Scan(&id, &thread, &payload, &state, &command, &peer)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if state != "submitting" && state != "unknown_submission" && state != "submitted" && state != "delivered" && state != "read" {
		return nil
	}
	var epoch int64
	if err = tx.QueryRow("SELECT coalesce((SELECT CAST(value AS INTEGER) FROM settings WHERE key=?),0)", "memory_epoch:"+Hash(conv)).Scan(&epoch); err != nil {
		return err
	}
	if _, err = s.validAIThread(tx, thread, conv, peer, command, epoch, now, hours, days); errors.Is(err, ErrAIThreadUnavailable) {
		return nil
	} else if err != nil {
		return err
	}
	var x aiExchange
	if err = s.openAI("ai-exchange:"+id, payload, &x); err != nil {
		return err
	}
	if x.Answer == "" {
		return nil
	}
	key := Hash(conv + "\x00" + wire)
	if _, err = tx.Exec("INSERT OR IGNORE INTO ai_answer_refs VALUES(?,?)", key, id); err != nil {
		return err
	}
	var actual string
	if err = tx.QueryRow("SELECT exchange_id FROM ai_answer_refs WHERE reply_key=?", key).Scan(&actual); err != nil {
		return err
	}
	if actual != id {
		return errors.New("ai_answer_conflict")
	}
	if _, err = tx.Exec("UPDATE jobs SET state='submitted',plan='' WHERE id=? AND state IN ('submitting','unknown_submission')", id); err != nil {
		return err
	}
	if err = s.markAIExchangeSubmitted(tx, id, thread, now, hours, days); err != nil {
		return err
	}
	return tx.Commit()
}

// A later delivery/read echo must not extend conversation lifetime again.
func (s *Store) markAIExchangeSubmitted(tx *sql.Tx, id, thread string, now time.Time, hours, days int) error {
	var payload string
	if err := tx.QueryRow("SELECT payload FROM ai_exchanges WHERE id=?", id).Scan(&payload); err != nil {
		return err
	}
	var x aiExchange
	if err := s.openAI("ai-exchange:"+id, payload, &x); err != nil {
		return err
	}
	if x.SubmittedAt != "" {
		return nil
	}
	x.SubmittedAt = aiStamp(now)
	sealed, err := s.sealAI("ai-exchange:"+id, x)
	if err != nil {
		return err
	}
	if _, err = tx.Exec("UPDATE ai_exchanges SET payload=? WHERE id=?", sealed, id); err != nil {
		return err
	}
	return s.touchAIThread(tx, thread, now, hours, days)
}
func (s *Store) touchAIThread(tx *sql.Tx, thread string, now time.Time, hours, days int) error {
	var created string
	if err := tx.QueryRow("SELECT created_at FROM ai_threads WHERE id=?", thread).Scan(&created); err != nil {
		return err
	}
	at, err := time.Parse(time.RFC3339Nano, created)
	if err != nil {
		return err
	}
	_, err = tx.Exec("UPDATE ai_threads SET last_active_at=?,expires_at=? WHERE id=? AND julianday(last_active_at)<=julianday(?)", aiStamp(now), aiStamp(aiExpiry(at, now, hours, days)), thread, aiStamp(now))
	return err
}

// TouchAIThread marks a successful submission, without assuming an upstream alias.
func (s *Store) TouchAIThread(id string, now time.Time, hours, days int) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var thread string
	err = tx.QueryRow("SELECT thread_id FROM ai_exchanges WHERE id=?", id).Scan(&thread)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if err = s.markAIExchangeSubmitted(tx, id, thread, now, hours, days); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) PruneAIThreads(now time.Time, hours, days int) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	expired := "julianday(expires_at)<=julianday(?) OR julianday(last_active_at)+?/24.0<=julianday(?) OR julianday(created_at)+?<=julianday(?)"
	args := []any{aiStamp(now), hours, aiStamp(now), days, aiStamp(now)}
	// Queued plans contain context snapshots too. Expiry must remove those even
	// if the bot remains paused and never takes the job out of its queue.
	if _, err = tx.Exec("UPDATE jobs SET state='cancelled',code='thread_expired',plan='' WHERE state IN ('queued','generating','awaiting_approval') AND id IN (SELECT id FROM ai_exchanges WHERE thread_id IN (SELECT id FROM ai_threads WHERE "+expired+"))", args...); err != nil {
		return err
	}
	if _, err = tx.Exec("DELETE FROM ai_threads WHERE "+expired, args...); err != nil {
		return err
	}
	return tx.Commit()
}
func clearAIThreads(tx *sql.Tx, chat string) error {
	where := ""
	args := []any{}
	if chat != "" {
		where = " WHERE chat_key=?"
		args = append(args, Hash(chat))
	}
	if _, err := tx.Exec("UPDATE jobs SET state='cancelled',code='memory_cleared',plan='' WHERE state IN ('queued','generating','awaiting_approval') AND id IN (SELECT id FROM ai_exchanges WHERE thread_id IN (SELECT id FROM ai_threads"+where+"))", args...); err != nil {
		return err
	}
	_, err := tx.Exec("DELETE FROM ai_threads"+where, args...)
	return err
}
