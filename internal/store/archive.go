package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/deusical/smsbot-server/internal/transport"
	"strconv"
	"strings"
	"time"
)

// Archive holds encrypted verified identity and durable read-job checkpoints.
// Consent is always checked separately; the record itself never authorizes sync.
type Archive struct {
	ScanRevision   int64  `json:"scan_revision,omitempty"`
	RequestSource  string `json:"request_source,omitempty"`
	RequestedAt    string `json:"requested_at,omitempty"`
	LastAttemptAt  string `json:"last_attempt_at,omitempty"`
	LastProgressAt string `json:"last_progress_at,omitempty"`
	Checked        int    `json:"checked"`
	Pages          int    `json:"pages"`
	Updated        int    `json:"updated"`
	ScannedThrough string `json:"scanned_through,omitempty"`

	DeepCheckedAt    string                 `json:"deep_checked_at,omitempty"`
	RefreshSince     string                 `json:"refresh_since,omitempty"`
	RefreshUntil     string                 `json:"refresh_until,omitempty"`
	RefreshCursor    string                 `json:"refresh_cursor,omitempty"`
	RefreshState     string                 `json:"refresh_state,omitempty"`
	RefreshCheckedAt string                 `json:"refresh_checked_at,omitempty"`
	RefreshRetryAt   string                 `json:"refresh_retry_at,omitempty"`
	RefreshFailures  int                    `json:"refresh_failures,omitempty"`
	RefreshError     string                 `json:"refresh_error,omitempty"`
	FocusAt          string                 `json:"focus_at,omitempty"`
	RecentRetryAt    string                 `json:"recent_retry_at,omitempty"`
	RecentFailures   int                    `json:"recent_failures"`
	RecentError      string                 `json:"recent_error,omitempty"`
	RecentCursor     string                 `json:"recent_cursor"`
	RecentSince      string                 `json:"recent_since"`
	RecentStarted    string                 `json:"recent_started"`
	RetryAt          string                 `json:"retry_at,omitempty"`
	Failures         int                    `json:"failures"`
	Duplicates       int                    `json:"duplicates"`
	Skipped          int                    `json:"skipped"`
	Conversation     transport.Conversation `json:"conversation"`
	Cursor           string                 `json:"cursor"`
	Since            string                 `json:"since"`
	State            string                 `json:"state"`
	Imported         int                    `json:"imported"`
	Oldest           string                 `json:"oldest"`
	LastSync         string                 `json:"last_sync"`
	Error            string                 `json:"error,omitempty"`
	Generation       int64                  `json:"generation"`
}

func (s *Store) Archive(conv string) (Archive, error) {
	var payload string
	var a Archive
	if err := s.DB.QueryRow("SELECT payload FROM archive_conversations WHERE conversation=?", conv).Scan(&payload); err != nil {
		return a, err
	}
	b, err := s.Vault.Open("archive:"+Hash(conv), payload)
	if err != nil {
		return a, err
	}
	err = json.Unmarshal(b, &a)
	return a, err
}
func (s *Store) SaveArchive(a Archive, retention int) error {
	b, err := json.Marshal(a)
	if err != nil {
		return err
	}
	enc, err := s.Vault.Seal("archive:"+Hash(a.Conversation.ID), b)
	if err != nil {
		return err
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec("INSERT INTO archive_conversations VALUES(?,?,?) ON CONFLICT(conversation) DO UPDATE SET payload=excluded.payload,retention_days=excluded.retention_days", a.Conversation.ID, enc, retention); err != nil {
		return err
	}
	if _, err = tx.Exec("DELETE FROM archive_pending WHERE conversation=?", a.Conversation.ID); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) Archives() ([]Archive, error) { return s.ArchivesContext(context.Background()) }
func (s *Store) ArchivesContext(ctx context.Context) ([]Archive, error) {
	rows, err := s.DB.QueryContext(ctx, "SELECT conversation,payload FROM archive_conversations ORDER BY conversation")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Archive{}
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var conv, enc string
		if err = rows.Scan(&conv, &enc); err != nil {
			return nil, err
		}
		b, e := s.Vault.Open("archive:"+Hash(conv), enc)
		if e != nil {
			return nil, e
		}
		var a Archive
		if e = json.Unmarshal(b, &a); e != nil {
			return nil, e
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
func (s *Store) ArchiveRetention(pendingDays ...int) error {
	rows, err := s.DB.Query("SELECT conversation,retention_days FROM archive_conversations WHERE retention_days>0")
	if err != nil {
		return err
	}
	type policy struct {
		conv string
		days int
	}
	policies := []policy{}
	for rows.Next() {
		var p policy
		if err = rows.Scan(&p.conv, &p.days); err != nil {
			rows.Close()
			return err
		}
		policies = append(policies, p)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, p := range policies {
		cutoff := time.Now().UTC().AddDate(0, 0, -p.days).Format("2006-01-02T15:04:05.000000000Z")
		if _, err = s.DB.Exec("DELETE FROM messages WHERE conversation=? AND at<?", p.conv, cutoff); err != nil {
			return err
		}
	}
	if len(pendingDays) > 0 && pendingDays[0] > 0 {
		cutoff := time.Now().UTC().AddDate(0, 0, -pendingDays[0]).Format("2006-01-02T15:04:05.000000000Z")
		_, err = s.DB.Exec("DELETE FROM messages WHERE conversation IN (SELECT conversation FROM archive_pending) AND at<?", cutoff)
		if err != nil {
			return err
		}
	}
	return nil
}

type Changes struct {
	Deleted  []string  `json:"deleted"`
	Messages []Message `json:"messages"`
	Cursor   int64     `json:"cursor"`
	More     bool      `json:"more"`
}

// Changes advances over every observation, including nonmatching search results.
func (s *Store) Changes(conv, after, search string) (Changes, error) {
	out := Changes{Messages: []Message{}}
	seq, err := strconv.ParseInt(after, 10, 64)
	if err != nil || seq < 0 {
		return out, errors.New("invalid_change_cursor")
	}
	out.Cursor = seq
	rows, err := s.DB.Query("SELECT seq,message FROM message_changes WHERE conversation=? AND seq>? ORDER BY seq LIMIT 101", conv, seq)
	if err != nil {
		return out, err
	}
	ids := []string{}
	for rows.Next() {
		var n int64
		var id string
		if err = rows.Scan(&n, &id); err != nil {
			rows.Close()
			return out, err
		}
		if len(ids) == 100 {
			out.More = true
			break
		}
		out.Cursor = n
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		var m Message
		var enc, files string
		err = s.DB.QueryRow("SELECT m.id,m.direction,m.at,m.payload,coalesce(meta.origin,'bot'),coalesce(meta.status,''),coalesce(a.payload,''),coalesce((SELECT max(revision) FROM message_revisions WHERE message=m.id),0) FROM messages m LEFT JOIN message_meta meta ON meta.message=m.id LEFT JOIN message_attachments a ON a.message=m.id WHERE m.id=? AND m.conversation=?", id, conv).Scan(&m.ID, &m.Direction, &m.At, &enc, &m.Origin, &m.Status, &files, &m.Revision)
		if err != nil {
			if !errors.Is(err, sql.ErrNoRows) {
				return out, err
			}
			out.Deleted = append(out.Deleted, id)
			continue
		}
		b, e := s.Vault.Open("message:"+id, enc)
		if e != nil {
			return out, e
		}
		m.Text = string(b)
		if files != "" {
			f, e := s.openAttachments(id, files)
			if e != nil {
				return out, e
			}
			for _, v := range f {
				m.Attachments = append(m.Attachments, v.Info)
			}
		}
		// Return changes even when an edit stops matching search, so clients can remove it.
		out.Messages = append(out.Messages, m)
	}
	if err = s.EnrichMessages(conv, out.Messages); err != nil {
		return out, err
	}
	return out, nil
}
func ParseArchiveSince(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	return time.Parse(time.RFC3339, s)
}

func (s *Store) ChangeWatermark(conv string) (int64, error) {
	var n int64
	err := s.DB.QueryRow("SELECT coalesce(max(seq),0) FROM message_changes WHERE conversation=?", conv).Scan(&n)
	return n, err
}

// A newer live observation wins over a history snapshot fetched concurrently.
func (s *Store) MessageChangedSince(conv, upstream, txn string, seq int64) (bool, error) {
	alias := Hash(conv + "\x00" + upstream)
	var changed bool
	err := s.DB.QueryRow("SELECT EXISTS(SELECT 1 FROM message_changes WHERE conversation=? AND seq>? AND (message=? OR message IN (SELECT message FROM message_alias WHERE alias=?) OR (?!='' AND message=?)))", conv, seq, alias, alias, txn, txn).Scan(&changed)
	return changed, err
}

// LatestMessageTimes returns the newest saved message time per conversation so
// the owner list can order conversations by recent activity without decrypting
// message bodies.
func (s *Store) LatestMessageTimes(ctx context.Context) (map[string]string, error) {
	rows, err := s.DB.QueryContext(ctx, "SELECT conversation,MAX(at) FROM messages GROUP BY conversation")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var conv, at string
		if err = rows.Scan(&conv, &at); err != nil {
			return nil, err
		}
		out[conv] = at
	}
	return out, rows.Err()
}

// ChangeSequence is the newest message change number; it moves whenever a
// message is added, edited or receives a new status.
func (s *Store) ChangeSequence() uint64 {
	var seq sql.NullInt64
	if err := s.DB.QueryRow("SELECT max(seq) FROM message_changes").Scan(&seq); err != nil || !seq.Valid || seq.Int64 < 0 {
		return 0
	}
	return uint64(seq.Int64)
}

// Preview is the newest saved message of a conversation, for the owner list.
type Preview struct {
	Text       string
	FromMe     bool
	Attachment bool
}

// LatestPreviews opens only the newest message per requested conversation. The
// caller must restrict ids to conversations the owner may see.
func (s *Store) LatestPreviews(ctx context.Context, ids []string) (map[string]Preview, error) {
	out := map[string]Preview{}
	for _, id := range ids {
		var mid, direction, enc string
		var files int
		err := s.DB.QueryRowContext(ctx, "SELECT m.id,m.direction,m.payload,EXISTS(SELECT 1 FROM message_attachments a WHERE a.message=m.id) FROM messages m WHERE m.conversation=? ORDER BY m.at DESC,m.id DESC LIMIT 1", id).Scan(&mid, &direction, &enc, &files)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		plain, err := s.Vault.Open("message:"+mid, enc)
		if err != nil {
			continue
		}
		text := strings.Join(strings.Fields(string(plain)), " ")
		if runes := []rune(text); len(runes) > 120 {
			text = string(runes[:120]) + "…"
		}
		out[id] = Preview{Text: text, FromMe: direction == "outgoing", Attachment: files == 1}
	}
	return out, nil
}
