package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/deusical/smsbot-server/internal/media"
	"time"
)

func (s *Store) openAttachments(id, payload string) ([]media.File, error) {
	b, err := s.Vault.Open("attachments:"+id, payload)
	if err != nil {
		return nil, err
	}
	var files []media.File
	err = json.Unmarshal(b, &files)
	return files, err
}

func (s *Store) saveAttachments(tx *sql.Tx, id string, files []media.File) error {
	var previous string
	if err := tx.QueryRow("SELECT payload FROM message_attachments WHERE message=?", id).Scan(&previous); err == nil {
		existing, err := s.openAttachments(id, previous)
		if err != nil {
			return err
		}
		previousJSON, _ := json.Marshal(existing)
		newJSON, _ := json.Marshal(files)
		if string(previousJSON) == string(newJSON) {
			return nil
		}
		// Locally submitted originals remain authoritative after normalization
		// removes inline bytes. Exact transaction/alias matching selected this
		// message; phone echoes may assign entirely different part IDs.
		var localSend bool
		if err := tx.QueryRow("SELECT EXISTS(SELECT 1 FROM messages m JOIN message_meta meta ON meta.message=m.id WHERE m.id=? AND m.direction='outgoing' AND meta.origin='web')", id).Scan(&localSend); err != nil {
			return err
		}
		for _, f := range existing {
			if localSend && (len(f.Data) > 0 || f.BlobID != "") {
				return nil
			}
		}
		for i := range files {
			for _, old := range existing {
				// Repeated observations preserve both timed failures and policy
				// blocks (which deliberately have no retry deadline).
				// A changed source remains immediately eligible.
				if SameFileSource(old, files[i]) && (old.RetryAt != "" || old.ArchiveState == "policy_limited") && files[i].RetryAt == "" {
					files[i].RetryAt = old.RetryAt
					files[i].Failures = old.Failures
					files[i].ArchiveState = old.ArchiveState
					files[i].ArchiveError = old.ArchiveError
					files[i].RetryPolicy = old.RetryPolicy
					files[i].RetryBackoffVersion = old.RetryBackoffVersion
				}
				if old.MessageID != "" && !old.SizeUnverified && old.Size > 0 && SameFileSource(old, files[i]) {
					// A completed original read measured these bytes; an upstream
					// snapshot may still carry an earlier, smaller reported size.
					files[i].Size = old.Size
					files[i].SizeUnverified = false
				}
				if old.BlobID != "" && (SameFileSource(old, files[i]) || (old.RemoteID != "" && old.RemoteID == files[i].RemoteID && bytes.Equal(old.Key, files[i].Key))) {
					files[i] = old
					break
				}
			}
		}
	} else if err != sql.ErrNoRows {
		return err
	}
	b, err := json.Marshal(files)
	if err != nil {
		return err
	}
	enc, err := s.Vault.Seal("attachments:"+id, b)
	if err != nil {
		return err
	}
	// Keep local originals when the phone echoes an uploaded attachment.
	_, err = tx.Exec("INSERT INTO message_attachments VALUES(?,?) ON CONFLICT(message) DO UPDATE SET payload=excluded.payload", id, enc)
	return err
}

func (s *Store) SaveSentFiles(id, conv, text string, files []media.File) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	enc, err := s.Vault.Seal("message:"+id, []byte(text))
	if err != nil {
		return err
	}
	_, err = tx.Exec("INSERT OR IGNORE INTO messages VALUES(?,?,'outgoing',?,?)", id, conv, time.Now().UTC().Format("2006-01-02T15:04:05.000000000Z"), enc)
	if err != nil {
		return err
	}
	if _, err = tx.Exec("INSERT INTO message_meta VALUES(?,'web','submitted',1) ON CONFLICT(message) DO UPDATE SET origin='web',owner_only=1", id); err != nil {
		return err
	}
	if err = s.saveAttachments(tx, id, files); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) Attachment(conv, message, attachment string) (media.File, error) {
	return s.AttachmentSince(conv, message, attachment, time.Time{})
}

// AttachmentSince enforces retention with a keyed read, without pruning tables.
func (s *Store) AttachmentSince(conv, message, attachment string, since time.Time) (media.File, error) {
	cutoff := ""
	if !since.IsZero() {
		cutoff = since.UTC().Format(time.RFC3339Nano)
	}

	var enc string
	err := s.DB.QueryRow("SELECT a.payload FROM message_attachments a JOIN messages m ON m.id=a.message WHERE m.conversation=? AND m.id=? AND (?='' OR julianday(m.at)>=julianday(?))", conv, message, cutoff, cutoff).Scan(&enc)
	if err != nil {
		return media.File{}, err
	}
	files, err := s.openAttachments(message, enc)
	if err != nil {
		return media.File{}, err
	}
	for _, file := range files {
		if file.ID == attachment {
			return file, nil
		}
	}
	return media.File{}, sql.ErrNoRows
}

// AttachmentBatch pages metadata, never an entire installation's file bytes.
type AttachmentRecord struct {
	Message      string
	Conversation string
	Files        []media.File
}

func (s *Store) AttachmentBatch(ctx context.Context, after string, limit int) ([]AttachmentRecord, error) {
	return s.attachmentRecords(ctx, "SELECT m.id,m.conversation,a.payload FROM messages m JOIN message_attachments a ON a.message=m.id JOIN archive_conversations c ON c.conversation=m.conversation WHERE m.id>? ORDER BY m.id LIMIT ?", after, limit)
}
func (s *Store) AttachmentRangeBatch(ctx context.Context, conv, from, to, after string, limit int) ([]AttachmentRecord, error) {
	since, err := time.Parse(time.RFC3339Nano, from)
	if err != nil {
		return nil, err
	}
	until, err := time.Parse(time.RFC3339Nano, to)
	if err != nil {
		return nil, err
	}
	const stamp = "2006-01-02T15:04:05.000000000Z"
	return s.attachmentRecords(ctx, `SELECT m.id,m.conversation,a.payload FROM messages m JOIN message_attachments a ON a.message=m.id JOIN archive_conversations c ON c.conversation=m.conversation WHERE m.conversation=? AND m.at>=? AND m.at<=? AND m.id>? ORDER BY m.id LIMIT ?`, conv, since.UTC().Format(stamp), until.UTC().Format(stamp), after, limit)
}
func (s *Store) attachmentRecords(ctx context.Context, query string, args ...any) ([]AttachmentRecord, error) {
	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AttachmentRecord
	for rows.Next() {
		var r AttachmentRecord
		var enc string
		if err = rows.Scan(&r.Message, &r.Conversation, &enc); err != nil {
			return nil, err
		}
		r.Files, err = s.openAttachments(r.Message, enc)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
func SameFileSource(a, b media.File) bool {
	return a.ID == b.ID && a.RemoteID == b.RemoteID && bytes.Equal(a.Key, b.Key) && bytes.Equal(a.Data, b.Data) && a.MessageID == b.MessageID && a.ActionID == b.ActionID && a.ConversationID == b.ConversationID && a.Timestamp == b.Timestamp && bytes.Equal(a.PreviewData, b.PreviewData)
}

// UpdateArchivedFile uses compare-and-replace so a concurrent source update or
// retention deletion cannot attach downloaded bytes to a different file.
func (s *Store) UpdateArchivedFile(ctx context.Context, message string, expected, updated media.File) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var enc string
	if err = tx.QueryRowContext(ctx, "SELECT payload FROM message_attachments WHERE message=?", message).Scan(&enc); err != nil {
		return err
	}
	files, err := s.openAttachments(message, enc)
	if err != nil {
		return err
	}
	found := false
	for i := range files {
		if SameFileSource(files[i], expected) {
			// A foreground measurement must not discard an original that the
			// archive committed concurrently from the same exact source.
			if files[i].BlobID != "" && updated.BlobID == "" && SameFileSource(updated, expected) {
				updated = files[i]
			}
			files[i] = updated
			found = true
			break
		}
	}
	if !found {
		return errors.New("archive_file_changed")
	}
	b, err := json.Marshal(files)
	if err != nil {
		return err
	}
	enc, err = s.Vault.Seal("attachments:"+message, b)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE message_attachments SET payload=? WHERE message=?", enc, message); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) OpenAttachmentPayload(message, payload string) ([]media.File, error) {
	return s.openAttachments(message, payload)
}
