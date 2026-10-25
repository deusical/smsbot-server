package store

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
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
	Interaction  string `json:"interaction,omitempty"`
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
type backupArchive struct {
	Conversation string
	Payload      string
	Retention    int
}
type backupRevision struct {
	Message  string
	Revision int
	Payload  string
	Observed string
}
type backupMutation struct {
	Job     string
	Target  string
	Payload string
}
type backupSnapshot struct {
	Mutations   []backupMutation   `json:"mutations,omitempty"`
	Archives    []backupArchive    `json:"archives,omitempty"`
	Revisions   []backupRevision   `json:"revisions,omitempty"`
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
		// Thread ancestry is memory, excluded from filtered backups. Restore never
		// replays plans, so preserve job/transaction evidence without this snapshot.
		if v.Plan != "" {
			raw, err := s.Vault.Open("plan:"+v.ID, v.Plan)
			if err != nil {
				rows.Close()
				return nil, err
			}
			var plan Plan
			if err = json.Unmarshal(raw, &plan); err != nil {
				rows.Close()
				return nil, err
			}
			if plan.Thread != nil {
				v.Plan = ""
			}
		}
		b.Jobs = append(b.Jobs, v)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	rows, e = tx.Query("SELECT m.id,m.conversation,m.direction,m.at,m.payload,coalesce(meta.owner_only,0),coalesce(meta.origin,''),coalesce(meta.status,''),coalesce(i.payload,'') FROM messages m LEFT JOIN message_meta meta ON meta.message=m.id LEFT JOIN message_interactions i ON i.message=m.id")
	if e != nil {
		return nil, e
	}
	for rows.Next() {
		var v backupMessage
		if e = rows.Scan(&v.ID, &v.Conversation, &v.Direction, &v.At, &v.Payload, &v.OwnerOnly, &v.Origin, &v.Status, &v.Interaction); e != nil {
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
		a.Payload, e = s.inlineArchiveBlobs(a.Message, a.Payload)
		if e != nil {
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
	if b.Schema >= 5 {
		rows, e = tx.Query("SELECT conversation,payload,retention_days FROM archive_conversations")
		if e != nil {
			return nil, e
		}
		for rows.Next() {
			var a backupArchive
			if e = rows.Scan(&a.Conversation, &a.Payload, &a.Retention); e != nil {
				rows.Close()
				return nil, e
			}
			b.Archives = append(b.Archives, a)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return nil, e
		}
		rows, e = tx.Query("SELECT message,revision,payload,observed FROM message_revisions")
		if e != nil {
			return nil, e
		}
		for rows.Next() {
			var r backupRevision
			if e = rows.Scan(&r.Message, &r.Revision, &r.Payload, &r.Observed); e != nil {
				rows.Close()
				return nil, e
			}
			b.Revisions = append(b.Revisions, r)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return nil, e
		}
	}
	rows, e = tx.Query("SELECT job,target,payload FROM interaction_intents")
	if e != nil {
		return nil, e
	}
	for rows.Next() {
		var m backupMutation
		if e = rows.Scan(&m.Job, &m.Target, &m.Payload); e != nil {
			rows.Close()
			return nil, e
		}
		b.Mutations = append(b.Mutations, m)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
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
	if schema >= 5 {
		if e = tx.QueryRow("SELECT (SELECT count(*) FROM archive_conversations)+(SELECT count(*) FROM message_revisions)+(SELECT count(*) FROM archive_pending)+(SELECT count(*) FROM observed_events)+(SELECT count(*) FROM message_changes)").Scan(&count); e != nil {
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
		case "cancelled", "expired", "failed", "submitted", "delivered", "read", "reaction_confirmed", "unknown_submission":
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
	for _, a := range b.Archives {
		plain, err := s.Vault.Open("archive:"+Hash(a.Conversation), a.Payload)
		var record Archive
		if err != nil || json.Unmarshal(plain, &record) != nil || record.Conversation.ID != a.Conversation || record.Conversation.Group || record.Conversation.ParticipantID == "" || a.Retention < 0 || a.Retention > 3650 {
			return errors.New("invalid backup archive")
		}
		record.State = "cancelled"
		record.Generation++
		record.Cursor = ""
		record.RecentCursor = ""
		record.RecentStarted = ""
		plain, err = json.Marshal(record)
		if err != nil {
			return err
		}
		enc, err := s.Vault.Seal("archive:"+Hash(a.Conversation), plain)
		if err != nil {
			return err
		}
		if _, e = tx.Exec("INSERT INTO archive_conversations VALUES(?,?,?)", a.Conversation, enc, a.Retention); e != nil {
			return e
		}
	}
	for _, r := range b.Revisions {
		if _, e = s.Vault.Open("message:"+r.Message, r.Payload); e != nil {
			return errors.New("invalid backup revision")
		}
		if _, e = tx.Exec("INSERT INTO message_revisions VALUES(?,?,?,?)", r.Message, r.Revision, r.Payload, r.Observed); e != nil {
			return e
		}
	}
	for _, m := range b.Messages {
		if m.Interaction != "" {
			if _, e = s.OpenInteraction(m.ID, m.Interaction); e != nil {
				return e
			}
			if _, e = tx.Exec("INSERT INTO message_interactions VALUES(?,?)", m.ID, m.Interaction); e != nil {
				return e
			}
		}
	}
	for _, m := range b.Mutations {
		if _, e = s.Vault.Open("mutation:"+m.Job, m.Payload); e != nil {
			return e
		}
		if _, e = tx.Exec("INSERT INTO interaction_intents VALUES(?,?,?)", m.Job, m.Target, m.Payload); e != nil {
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

// Keep operational backups self-contained for small saved originals. The
// archive-specific streaming format handles installations beyond this format's
// existing logical JSON ceiling.
func (s *Store) inlineArchiveBlobs(message, payload string) (string, error) {
	files, err := s.openAttachments(message, payload)
	if err != nil {
		return "", err
	}
	changed := false
	for i := range files {
		f := &files[i]
		if f.BlobID == "" {
			continue
		}
		id, err := hex.DecodeString(f.BlobID)
		if err != nil || len(id) != 16 {
			return "", errors.New("invalid archived file")
		}
		if f.Size > MaxBackupBytes/2 {
			return "", errors.New("use streaming archive backup for large originals")
		}
		source, err := os.Open(filepath.Join(filepath.Dir(s.Path), "archive", "blobs", f.BlobID+".enc"))
		if err != nil {
			return "", errors.New("archived original unavailable")
		}
		info, err := source.Stat()
		if err != nil {
			source.Close()
			return "", err
		}
		reader, err := s.Vault.OpenSeekStream(source, info.Size(), f.Size, "blob:"+f.BlobID)
		if err != nil {
			source.Close()
			return "", err
		}
		f.Data, err = io.ReadAll(io.LimitReader(reader, MaxBackupBytes/2+1))
		source.Close()
		if err != nil || len(f.Data) > MaxBackupBytes/2 {
			return "", errors.New("archived original unavailable or too large")
		}
		f.BlobID = ""
		changed = true
	}
	if !changed {
		return payload, nil
	}
	b, err := json.Marshal(files)
	if err != nil {
		return "", err
	}
	return s.Vault.Seal("attachments:"+message, b)
}
