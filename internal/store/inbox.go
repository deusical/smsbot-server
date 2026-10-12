package store

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// InboxPage contains locally retained messages, not complete provider history.
// Search scans one bounded page and advances even when there are no matches.
type InboxPage struct {
	Messages   []Message `json:"messages"`
	NextCursor string    `json:"next_cursor"`
}
type inboxCursor struct {
	At string `json:"at"`
	ID string `json:"id"`
}

func (s *Store) MessagePage(conv, before, search string, limit int) (InboxPage, error) {
	out := InboxPage{Messages: []Message{}}
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
	rows, e := s.DB.Query(`SELECT id,direction,at,payload FROM messages WHERE conversation=? AND (?='' OR at<? OR (at=? AND id<?)) ORDER BY at DESC,id DESC LIMIT ?`, conv, before, cursor.At, cursor.At, cursor.ID, limit+1)
	if e != nil {
		return out, e
	}
	defer rows.Close()
	count := 0
	var last inboxCursor
	for rows.Next() {
		var m Message
		var enc string
		if e = rows.Scan(&m.ID, &m.Direction, &m.At, &enc); e != nil {
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
		if search == "" || strings.Contains(strings.ToLower(m.Text), strings.ToLower(search)) {
			out.Messages = append(out.Messages, m)
		}
	}
	return out, rows.Err()
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
	tx, e := s.DB.Begin()
	if e != nil {
		return 0, e
	}
	defer tx.Rollback()
	result, e := tx.Exec("DELETE FROM messages WHERE julianday(at)<julianday(?)", cutoff)
	if e != nil {
		return 0, e
	}
	if _, e = tx.Exec("UPDATE jobs SET plan='' WHERE julianday(created)<julianday(?) AND state NOT IN ('queued','generating','processing','awaiting_approval','submitting')", cutoff); e != nil {
		return 0, e
	}
	if _, e = tx.Exec("DELETE FROM memory WHERE julianday(expires)<=julianday('now')"); e != nil {
		return 0, e
	}
	n, e := result.RowsAffected()
	if e != nil {
		return 0, e
	}
	return n, tx.Commit()
}
