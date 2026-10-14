package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"strings"
	"time"
)

const MaxBackupBytes = 256 << 20

type backupEnrollment struct {
	Conversation string `json:"conversation"`
	Participant  string `json:"participant"`
}
type backupJob struct {
	Job
	Plan string `json:"plan"`
}
type backupMessage struct {
	ID           string `json:"id"`
	Conversation string `json:"conversation"`
	Direction    string `json:"direction"`
	At           string `json:"at"`
	Payload      string `json:"payload"`
	OwnerOnly    bool   `json:"owner_only,omitempty"`
	Origin       string `json:"origin,omitempty"`
	Status       string `json:"status,omitempty"`
}
type backupAttachment struct {
	Message string `json:"message"`
	Payload string `json:"payload"`
}
type backupSnapshot struct {
	Attachments []backupAttachment `json:"attachments,omitempty"`
	Format      int                `json:"format"`
	Schema      int                `json:"schema"`
	Settings    map[string]string  `json:"settings"`
	Enrollment  []backupEnrollment `json:"enrollment"`
	Jobs        []backupJob        `json:"jobs"`
	Messages    []backupMessage    `json:"messages"`
	Usage       []backupUsage      `json:"usage,omitempty"`
	Aliases     []backupAlias      `json:"aliases,omitempty"`
}
type backupAlias struct {
	Alias   string `json:"alias"`
	Message string `json:"message"`
}
type backupUsage struct {
	Job        string   `json:"job"`
	Model      string   `json:"model"`
	Prompt     int      `json:"prompt_tokens"`
	Completion int      `json:"completion_tokens"`
	Reasoning  int      `json:"reasoning_tokens"`
	Cost       *float64 `json:"cost"`
}

func safeBackupSetting(key string) bool {
	return key == "paused" || key == "retention_days" || key == "bot_config" || key == "model_catalog" || reasoningSetting.MatchString(key)
}

var reasoningSetting = regexp.MustCompile(`^requested_reasoning:[a-f0-9]{64}$`)

// ExportBackup returns authenticated ciphertext, readable only with this vault's
// key. Credentials, provider keys, browser sessions and Google pairing are absent.
// Call from the offline CLI under the service state lock.
func (s *Store) ExportBackup() ([]byte, error) {
	tx, e := s.DB.Begin()
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	b := backupSnapshot{Format: 1, Settings: map[string]string{}}
	if e = tx.QueryRow("PRAGMA user_version").Scan(&b.Schema); e != nil {
		return nil, e
	}
	rows, e := tx.Query("SELECT key,value FROM settings WHERE key IN ('paused','retention_days','bot_config','model_catalog') OR key LIKE 'requested_reasoning:%'")
	if e != nil {
		return nil, e
	}
	for rows.Next() {
		var k, v string
		if e = rows.Scan(&k, &v); e != nil {
			rows.Close()
			return nil, e
		}
		if safeBackupSetting(k) {
			b.Settings[k] = v
		}
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	rows, e = tx.Query("SELECT conversation,participant FROM enrollment")
	if e != nil {
		return nil, e
	}
	for rows.Next() {
		var v backupEnrollment
		if e = rows.Scan(&v.Conversation, &v.Participant); e != nil {
			rows.Close()
			return nil, e
		}
		b.Enrollment = append(b.Enrollment, v)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	rows, e = tx.Query("SELECT id,conversation,command,state,created,txn,code,plan FROM jobs")
	if e != nil {
		return nil, e
	}
	for rows.Next() {
		var v backupJob
		if e = rows.Scan(&v.ID, &v.ConversationID, &v.Command, &v.State, &v.CreatedAt, &v.TransactionID, &v.Code, &v.Plan); e != nil {
			rows.Close()
			return nil, e
		}
		b.Jobs = append(b.Jobs, v)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	rows, e = tx.Query("SELECT m.id,m.conversation,m.direction,m.at,m.payload,coalesce(meta.owner_only,0),coalesce(meta.origin,''),coalesce(meta.status,'') FROM messages m LEFT JOIN message_meta meta ON meta.message=m.id")
	if e != nil {
		return nil, e
	}
	for rows.Next() {
		var v backupMessage
		if e = rows.Scan(&v.ID, &v.Conversation, &v.Direction, &v.At, &v.Payload, &v.OwnerOnly, &v.Origin, &v.Status); e != nil {
			rows.Close()
			return nil, e
		}
		b.Messages = append(b.Messages, v)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	rows, e = tx.Query("SELECT alias,message FROM message_alias")
	if e != nil {
		return nil, e
	}
	for rows.Next() {
		var a backupAlias
		if e = rows.Scan(&a.Alias, &a.Message); e != nil {
			rows.Close()
			return nil, e
		}
		b.Aliases = append(b.Aliases, a)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	rows, e = tx.Query("SELECT message,payload FROM message_attachments")
	if e != nil {
		return nil, e
	}
	for rows.Next() {
		var a backupAttachment
		if e = rows.Scan(&a.Message, &a.Payload); e != nil {
			rows.Close()
			return nil, e
		}
		b.Attachments = append(b.Attachments, a)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	if b.Schema >= 2 {
		rows, e = tx.Query("SELECT job,model,prompt_tokens,completion_tokens,reasoning_tokens,cost FROM usage")
		if e != nil {
			return nil, e
		}
		for rows.Next() {
			var v backupUsage
			if e = rows.Scan(&v.Job, &v.Model, &v.Prompt, &v.Completion, &v.Reasoning, &v.Cost); e != nil {
				rows.Close()
				return nil, e
			}
			b.Usage = append(b.Usage, v)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return nil, e
		}
	}
	plain, e := json.Marshal(b)
	if e != nil {
		return nil, e
	}
	if len(plain) > MaxBackupBytes/2 {
		return nil, errors.New("backup exceeds size limit")
	}
	sealed, e := s.Vault.Seal("backup", plain)
	return []byte(sealed), e
}

// RestoreBackup is atomic and refuses a destination with existing bot records.
// Existing destination credentials stay local. Restored work never resumes.
func (s *Store) RestoreBackup(data []byte) error {
	if len(data) > MaxBackupBytes {
		return errors.New("backup exceeds size limit")
	}
	plain, e := s.Vault.Open("backup", string(data))
	if e != nil {
		return errors.New("backup cannot be authenticated with this vault")
	}
	var b backupSnapshot
	decoder := json.NewDecoder(bytes.NewReader(plain))
	decoder.DisallowUnknownFields()
	if e = decoder.Decode(&b); e != nil {
		return errors.New("invalid backup")
	}
	if e = decoder.Decode(new(any)); e != io.EOF {
		return errors.New("invalid backup")
	}
	tx, e := s.DB.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	var schema, count int
	if e = tx.QueryRow("PRAGMA user_version").Scan(&schema); e != nil {
		return e
	}
	if b.Format != 1 || b.Schema < 1 || b.Schema > schema {
		return errors.New("unsupported backup version")
	}
	if e = tx.QueryRow("SELECT (SELECT count(*) FROM enrollment)+(SELECT count(*) FROM jobs)+(SELECT count(*) FROM messages)").Scan(&count); e != nil {
		return e
	}
	if count != 0 {
		return errors.New("restore requires fresh bot state")
	}
	if schema >= 2 {
		if e = tx.QueryRow("SELECT (SELECT count(*) FROM usage)+(SELECT count(*) FROM memory)").Scan(&count); e != nil {
			return e
		}
		if count != 0 {
			return errors.New("restore requires fresh bot state")
		}
	}
	for k, v := range b.Settings {
		if !safeBackupSetting(k) {
			return errors.New("backup contains unsupported setting")
		}
		if k == "paused" {
			continue
		}
		if reasoningSetting.MatchString(k) {
			var effort string
			if json.Unmarshal([]byte(v), &effort) != nil || !strings.Contains("|default|off|minimal|low|medium|high|xhigh|max|", "|"+effort+"|") {
				return errors.New("invalid requested reasoning metadata")
			}
		}
		if k == "bot_config" {
			plain, err := s.Vault.Open("config", v)
			if err != nil || !json.Valid(plain) {
				return errors.New("invalid backup configuration")
			}
		}
		if _, e = tx.Exec("INSERT INTO settings(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", k, v); e != nil {
			return e
		}
	}
	for _, v := range b.Enrollment {
		if _, e = tx.Exec("INSERT INTO enrollment VALUES(?,?)", v.Conversation, v.Participant); e != nil {
			return e
		}
	}
	for _, v := range b.Jobs {
		created, parseErr := time.Parse(time.RFC3339Nano, v.CreatedAt)
		if parseErr != nil || v.ID == "" || v.ConversationID == "" {
			return errors.New("invalid backup job")
		}
		v.CreatedAt = created.UTC().Format("2006-01-02T15:04:05.000000000Z")
		switch v.State {
		case "queued", "generating", "processing", "awaiting_approval":
			v.State = "cancelled"
			v.Code = "restored_paused"
		case "submitting":
			v.State = "unknown_submission"
			v.Code = "restored_during_submission"
		case "cancelled", "expired", "failed", "submitted", "delivered", "read", "unknown_submission":
		default:
			return errors.New("invalid backup job state")
		}
		if _, e = tx.Exec("INSERT INTO jobs(id,conversation,command,state,created,txn,code,plan) VALUES(?,?,?,?,?,?,?,'')", v.ID, v.ConversationID, v.Command, v.State, v.CreatedAt, v.TransactionID, v.Code); e != nil {
			return e
		}
	}
	for _, v := range b.Messages {
		at, parseErr := time.Parse(time.RFC3339Nano, v.At)
		if parseErr != nil || v.ID == "" || v.Conversation == "" || (v.Direction != "incoming" && v.Direction != "outgoing") {
			return errors.New("invalid backup message")
		}
		v.At = at.UTC().Format("2006-01-02T15:04:05.000000000Z")
		if _, e = s.Vault.Open("message:"+v.ID, v.Payload); e != nil {
			return errors.New("invalid backup message")
		}
		if _, e = tx.Exec("INSERT INTO messages VALUES(?,?,?,?,?)", v.ID, v.Conversation, v.Direction, v.At, v.Payload); e != nil {
			return e
		}
		if _, e = tx.Exec("INSERT INTO message_meta VALUES(?,?,?,?)", v.ID, v.Origin, v.Status, v.OwnerOnly); e != nil {
			return e
		}
	}
	for _, a := range b.Attachments {
		if _, e = s.openAttachments(a.Message, a.Payload); e != nil {
			return errors.New("invalid backup attachment")
		}
		if _, e = tx.Exec("INSERT INTO message_attachments VALUES(?,?)", a.Message, a.Payload); e != nil {
			return e
		}
	}
	for _, a := range b.Aliases {
		if a.Alias == "" || a.Message == "" {
			return errors.New("invalid backup alias")
		}
		if _, e = tx.Exec("INSERT INTO message_alias VALUES(?,?)", a.Alias, a.Message); e != nil {
			return e
		}
	}
	for _, v := range b.Usage {
		if schema < 2 || v.Job == "" || v.Prompt < 0 || v.Completion < 0 || v.Reasoning < 0 || (v.Cost != nil && *v.Cost < 0) {
			return errors.New("invalid backup usage")
		}
		var exists int
		if e = tx.QueryRow("SELECT count(*) FROM jobs WHERE id=?", v.Job).Scan(&exists); e != nil || exists != 1 {
			return errors.New("backup usage has no corresponding job")
		}
		if _, e = tx.Exec("INSERT INTO usage VALUES(?,?,?,?,?,?)", v.Job, v.Model, v.Prompt, v.Completion, v.Reasoning, v.Cost); e != nil {
			return e
		}
	}
	if _, e = tx.Exec("INSERT INTO settings(key,value) VALUES('paused','true') ON CONFLICT(key) DO UPDATE SET value='true'"); e != nil {
		return e
	}
	return tx.Commit()
}
