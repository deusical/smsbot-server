package store

import (
	"database/sql"
	"errors"
	"smsbot-server/internal/media"
	"time"
)

// SaveObserved unifies phone IDs with our persisted outbound transactions. It
// never routes commands and does not make private messages visible to clients.
func (s *Store) SaveObserved(conv, upstream, txn, direction, text, origin, status string, at time.Time) error {
	return s.saveObserved(conv, upstream, txn, direction, text, origin, status, at, true, nil)
}

// Legacy explicit imports may create public rows, but cannot expose an existing
// private capture. Both import paths share exact upstream/transaction aliases.
func (s *Store) SaveLegacyImported(conv, upstream, txn, direction, text, status string, at time.Time) error {
	return s.saveObserved(conv, upstream, txn, direction, text, "history", status, at, false, nil)
}
func (s *Store) SaveObservedFiles(conv, upstream, txn, direction, text, origin, status string, at time.Time, files []media.File) error {
	return s.saveObserved(conv, upstream, txn, direction, text, origin, status, at, true, files)
}
func (s *Store) saveObserved(conv, upstream, txn, direction, text, origin, status string, at time.Time, privateNew bool, files []media.File) error {
	alias := Hash(conv + "\x00" + upstream)
	id := alias
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	matchedSend := false
	if direction == "outgoing" && txn != "" {
		var known string
		err = tx.QueryRow("SELECT txn FROM jobs WHERE conversation=? AND txn=?", conv, txn).Scan(&known)
		if err == nil {
			id = known
			matchedSend = true
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
	}
	var previous string
	err = tx.QueryRow("SELECT message FROM message_alias WHERE alias=?", alias).Scan(&previous)
	if err == nil {
		if !matchedSend {
			id = previous
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	enc, err := s.Vault.Seal("message:"+id, []byte(text))
	if err != nil {
		return err
	}
	result, err := tx.Exec("INSERT OR IGNORE INTO messages VALUES(?,?,?,?,?)", id, conv, direction, at.UTC().Format("2006-01-02T15:04:05.000000000Z"), enc)
	if err != nil {
		return err
	}
	// Only private archive observations may revise a message. Existing command
	// plans remain immutable, and legacy imports cannot rewrite private content.
	if privateNew {
		var old string
		if err = tx.QueryRow("SELECT payload FROM messages WHERE id=?", id).Scan(&old); err != nil {
			return err
		}
		plain, openErr := s.Vault.Open("message:"+id, old)
		if openErr != nil {
			return openErr
		}
		if string(plain) != text {
			if _, err = tx.Exec("INSERT INTO message_revisions SELECT ?,coalesce(max(revision),0)+1,?,? FROM message_revisions WHERE message=?", id, old, time.Now().UTC().Format(time.RFC3339Nano), id); err != nil {
				return err
			}
			if _, err = tx.Exec("UPDATE messages SET payload=? WHERE id=?", enc, id); err != nil {
				return err
			}
		}
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	// Existing rows preserve visibility and provenance.
	if _, err = tx.Exec(`INSERT INTO message_meta VALUES(?,?,?,?) ON CONFLICT(message) DO UPDATE SET status=CASE WHEN excluded.status='read' OR (excluded.status='delivered' AND message_meta.status!='read') OR message_meta.status='' THEN excluded.status ELSE message_meta.status END`, id, origin, status, n > 0 && privateNew); err != nil {
		return err
	}
	if len(files) > 0 {
		if err = s.saveAttachments(tx, id, files); err != nil {
			return err
		}
	}
	// Repair old imports only with an explicit send-transaction match. Equal text
	// and nearby timestamps are never enough: separate identical sends stay apart.
	if matchedSend {
		for _, duplicate := range []string{alias, previous} {
			if duplicate == "" || duplicate == id {
				continue
			}
			if _, err = tx.Exec(`UPDATE message_meta SET status=CASE WHEN (SELECT status FROM message_meta WHERE message=?)='read' THEN 'read' WHEN (SELECT status FROM message_meta WHERE message=?)='delivered' AND status!='read' THEN 'delivered' ELSE status END WHERE message=?`, duplicate, duplicate, id); err != nil {
				return err
			}
			if _, err = tx.Exec("UPDATE message_alias SET message=? WHERE message=?", id, duplicate); err != nil {
				return err
			}
			var duplicatePayload string
			attachmentErr := tx.QueryRow("SELECT payload FROM message_attachments WHERE message=?", duplicate).Scan(&duplicatePayload)
			if attachmentErr == nil {
				duplicateFiles, openErr := s.openAttachments(duplicate, duplicatePayload)
				if openErr != nil {
					return openErr
				}
				if err = s.saveAttachments(tx, id, duplicateFiles); err != nil {
					return err
				}
			} else if !errors.Is(attachmentErr, sql.ErrNoRows) {
				return attachmentErr
			}
			if _, err = tx.Exec("DELETE FROM messages WHERE id=? AND conversation=? AND direction='outgoing'", duplicate, conv); err != nil {
				return err
			}
		}
	}
	if _, err = tx.Exec("INSERT INTO message_alias VALUES(?,?) ON CONFLICT(alias) DO UPDATE SET message=excluded.message", alias, id); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) PublishCommandMessage(id string) error {
	_, err := s.DB.Exec("UPDATE message_meta SET owner_only=0 WHERE message=?", id)
	return err
}
func (s *Store) UpdateMessageStatus(conv, id, status string) error {
	_, err := s.DB.Exec(`UPDATE message_meta SET status=CASE WHEN ?='read' OR (?='delivered' AND status!='read') OR status='' THEN ? ELSE status END WHERE message=? AND EXISTS(SELECT 1 FROM messages WHERE id=message AND conversation=?)`, status, status, status, id, conv)
	return err
}
func (s *Store) SetInboxPreferences(b []byte, stopAutomatic bool, retention int) error {
	encoded, err := s.Vault.Seal("inbox-settings", b)
	if err != nil {
		return err
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec("INSERT INTO settings VALUES('inbox_settings',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", encoded); err != nil {
		return err
	}
	if _, err = tx.Exec("UPDATE archive_conversations SET retention_days=?", retention); err != nil {
		return err
	}
	if stopAutomatic {
		if _, err = tx.Exec("UPDATE jobs SET state='cancelled',code='automatic_replies_disabled',plan='' WHERE command!='manual' AND state IN ('queued','generating','awaiting_approval')"); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// FirstObservation uses opaque IDs only. Content edits/status repeats must never
// become a fresh bot command, even when the original text was not a command.
func (s *Store) FirstObservation(conv, upstream string) (bool, error) {
	key := Hash(conv + "\x00" + upstream)
	tx, err := s.DB.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var known int
	if err = tx.QueryRow("SELECT count(*) FROM message_alias WHERE alias=?", key).Scan(&known); err != nil {
		return false, err
	}
	result, err := tx.Exec("INSERT OR IGNORE INTO observed_events VALUES(?)", key)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0 && known == 0, tx.Commit()
}
