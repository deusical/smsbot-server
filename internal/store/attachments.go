package store

import (
	"database/sql"
	"encoding/json"
	"smsbot-server/internal/media"
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
		for _, f := range existing {
			if len(f.Data) > 0 {
				return nil
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
