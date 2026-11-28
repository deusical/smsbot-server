package store

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"github.com/deusical/smsbot-server/internal/media"
	"strings"
	"time"
)

// InboxPage contains locally retained messages, not complete provider history.
// Search scans one bounded page and advances even when there are no matches.
type InboxPage struct {
	Messages     []Message `json:"messages"`
	NextCursor   string    `json:"next_cursor"`
	ChangeCursor int64     `json:"change_cursor"`
}
type inboxCursor struct {
	At string `json:"at"`
	ID string `json:"id"`
}

func (s *Store) MessagePage(conv, before, search string, limit int) (InboxPage, error) {
	return s.messagePage(conv, before, search, limit, false)
}
func (s *Store) OwnerMessagePage(conv, before, search string, limit int) (InboxPage, error) {
	return s.messagePage(conv, before, search, limit, true)
}
func (s *Store) messagePage(conv, before, search string, limit int, owner bool) (InboxPage, error) {
	out := InboxPage{Messages: []Message{}}
	if err := s.DB.QueryRow("SELECT coalesce(max(seq),0) FROM message_changes WHERE conversation=?", conv).Scan(&out.ChangeCursor); err != nil {
		return out, err
	}
	if limit < 1 || limit > 100 {
		return out, errors.New("message limit must be between 1 and 100")
	}
	if len(search) > 512 || len(before) > 2048 {
		return out, errors.New("invalid inbox query")
	}
	var cursor inboxCursor
	if before != "" {
		b, e := base64.RawURLEncoding.DecodeString(before)
		if e != nil || json.Unmarshal(b, &cursor) != nil || cursor.ID == "" {
			return out, errors.New("invalid message cursor")
		}
		if _, e = time.Parse(time.RFC3339Nano, cursor.At); e != nil {
			return out, errors.New("invalid message cursor")
		}
	}
	rows, e := s.DB.Query(`SELECT m.id,m.direction,m.at,m.payload,coalesce(meta.origin,'bot'),coalesce(meta.status,''),coalesce(a.payload,''),coalesce((SELECT max(revision) FROM message_revisions WHERE message=m.id),0) FROM messages m LEFT JOIN message_meta meta ON meta.message=m.id LEFT JOIN message_attachments a ON a.message=m.id WHERE m.conversation=? AND (? OR coalesce(meta.owner_only,0)=0) AND (?='' OR m.at<? OR (m.at=? AND m.id<?)) ORDER BY m.at DESC,m.id DESC LIMIT ?`, conv, owner, before, cursor.At, cursor.At, cursor.ID, limit+1)
	if e != nil {
		return out, e
	}
	defer rows.Close()
	count := 0
	var last inboxCursor
	for rows.Next() {
		var m Message
		var enc, attachments string
		if e = rows.Scan(&m.ID, &m.Direction, &m.At, &enc, &m.Origin, &m.Status, &attachments, &m.Revision); e != nil {
			return out, e
		}
		if count == limit {
			b, _ := json.Marshal(last)
			out.NextCursor = base64.RawURLEncoding.EncodeToString(b)
			break
		}
		count++
		last = inboxCursor{m.At, m.ID}
		plain, e := s.Vault.Open("message:"+m.ID, enc)
		if e != nil {
			return out, e
		}
		m.Text = string(plain)
		if owner && attachments != "" {
			files, err := s.openAttachments(m.ID, attachments)
			if err != nil {
				return out, err
			}
			m.Attachments = media.Infos(files)
		}
		searchText := m.Text
		for _, attachment := range m.Attachments {
			searchText += "\n" + attachment.Name
		}
		if search == "" || strings.Contains(strings.ToLower(searchText), strings.ToLower(search)) {
			out.Messages = append(out.Messages, m)
		}
	}
	err := rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	if owner {
		err = s.EnrichMessages(conv, out.Messages)
	}
	return out, err
}

// Retention removes old sensitive payloads without deleting durable deduplication
// or uncertain-send evidence. Zero disables expiry. Active plans are retained.
func (s *Store) Retention(days int) (int64, error) {
	if days < 0 || days > 3650 {
		return 0, errors.New("retention must be between 0 and 3650 days")
	}
	if days == 0 {
		return 0, nil
	}
	cutoff := time.Now().UTC().AddDate(0, 0, -days).Format(time.RFC3339Nano)

	// 1. Batch delete messages in chunks with LIMIT per transaction
	var totalDeleted int64
	for {
		rows, err := s.DB.Query("SELECT id FROM messages WHERE julianday(at)<julianday(?) AND conversation NOT IN (SELECT conversation FROM archive_conversations UNION SELECT conversation FROM archive_pending) LIMIT 500", cutoff)
		if err != nil {
			return totalDeleted, err
		}
		var ids []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return totalDeleted, err
			}
			ids = append(ids, id)
		}
		rows.Close()
		if len(ids) == 0 {
			break
		}
		tx, err := s.DB.Begin()
		if err != nil {
			return totalDeleted, err
		}
		query := "DELETE FROM messages WHERE id IN (" + placeholders(len(ids)) + ")"
		args := make([]any, len(ids))
		for i, id := range ids {
			args[i] = id
		}
		res, err := tx.Exec(query, args...)
		if err != nil {
			tx.Rollback()
			return totalDeleted, err
		}
		n, _ := res.RowsAffected()
		if err := tx.Commit(); err != nil {
			return totalDeleted, err
		}
		totalDeleted += n
	}

	// 2. Compute pruned jobs set once
	rows, err := s.DB.Query("SELECT id FROM jobs WHERE julianday(created)<julianday(?) AND state NOT IN ('queued','generating','processing','awaiting_approval','submitting') AND id NOT IN (SELECT id FROM jobs ORDER BY created DESC LIMIT 200)", cutoff)
	if err != nil {
		return totalDeleted, err
	}
	var prunedJobs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return totalDeleted, err
		}
		prunedJobs = append(prunedJobs, id)
	}
	rows.Close()

	if len(prunedJobs) > 0 {
		const batchSize = 100
		for i := 0; i < len(prunedJobs); i += batchSize {
			end := min(i+batchSize, len(prunedJobs))
			batch := prunedJobs[i:end]
			ph := placeholders(len(batch))
			args := make([]any, len(batch))
			for idx, id := range batch {
				args[idx] = id
			}

			tx, err := s.DB.Begin()
			if err != nil {
				return totalDeleted, err
			}
			if _, err = tx.Exec("UPDATE jobs SET plan='' WHERE plan!='' AND id IN ("+ph+")", args...); err != nil {
				tx.Rollback()
				return totalDeleted, err
			}
			if _, err = tx.Exec("DELETE FROM job_work WHERE job IN ("+ph+") AND (cancel_sent!=0 OR stage IN ('','complete','receiving','waiting_upload','uploading','ready','failed','expired','cancelled'))", args...); err != nil {
				tx.Rollback()
				return totalDeleted, err
			}
			if _, err = tx.Exec("DELETE FROM job_steps WHERE job IN ("+ph+")", args...); err != nil {
				tx.Rollback()
				return totalDeleted, err
			}
			if _, err = tx.Exec("DELETE FROM usage_steps WHERE job IN ("+ph+")", args...); err != nil {
				tx.Rollback()
				return totalDeleted, err
			}
			if _, err = tx.Exec("DELETE FROM settings WHERE (key LIKE 'used_tools:%' OR key LIKE 'requested_reasoning:%' OR key LIKE 'send_reason:%') AND substr(key, instr(key, ':')+1) IN ("+ph+")", args...); err != nil {
				tx.Rollback()
				return totalDeleted, err
			}
			if err = tx.Commit(); err != nil {
				return totalDeleted, err
			}
		}
	}

	// 3. Clear plan on other old finished jobs, and memory/credentials/changes/observed in a short final transaction
	tx, e := s.DB.Begin()
	if e != nil {
		return totalDeleted, e
	}
	defer tx.Rollback()

	if _, e = tx.Exec("UPDATE jobs SET plan='' WHERE plan!='' AND julianday(created)<julianday(?) AND state NOT IN ('queued','generating','processing','awaiting_approval','submitting')", cutoff); e != nil {
		return totalDeleted, e
	}
	if _, e = tx.Exec("DELETE FROM memory WHERE julianday(expires)<=julianday('now')"); e != nil {
		return totalDeleted, e
	}
	if _, e = tx.Exec("DELETE FROM credentials WHERE kind='session' AND expires>0 AND expires<=?", time.Now().Unix()); e != nil {
		return totalDeleted, e
	}
	if _, e = tx.Exec("DELETE FROM message_changes WHERE conversation NOT IN (SELECT conversation FROM archive_conversations UNION SELECT conversation FROM archive_pending)"); e != nil {
		return totalDeleted, e
	}
	if _, e = tx.Exec("DELETE FROM message_changes WHERE seq < (SELECT coalesce(max(seq) - 5000, 0) FROM message_changes)"); e != nil {
		return totalDeleted, e
	}
	if _, e = tx.Exec("DELETE FROM observed_events WHERE id NOT IN (SELECT id FROM messages UNION SELECT alias FROM message_alias)"); e != nil {
		return totalDeleted, e
	}
	if e = tx.Commit(); e != nil {
		return totalDeleted, e
	}
	if totalDeleted > 0 {
		s.noteMessageDeletion()
	}
	return totalDeleted, nil
}

// PruneExpiredSessions removes browser sessions whose expires timestamp is in the past.
func (s *Store) PruneExpiredSessions() (int64, error) {
	res, err := s.DB.Exec("DELETE FROM credentials WHERE kind='session' AND expires>0 AND expires<=?", time.Now().Unix())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// PruneObservedEvents removes deduplication hashes that no longer correspond to existing messages or message aliases.
func (s *Store) PruneObservedEvents() (int64, error) {
	res, err := s.DB.Exec("DELETE FROM observed_events WHERE id NOT IN (SELECT id FROM messages UNION SELECT alias FROM message_alias)")
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// PruneMessageChanges removes message_changes with seq < minSeq.
func (s *Store) PruneMessageChanges(minSeq int64) (int64, error) {
	if minSeq <= 0 {
		return 0, nil
	}
	res, err := s.DB.Exec("DELETE FROM message_changes WHERE seq < ?", minSeq)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
