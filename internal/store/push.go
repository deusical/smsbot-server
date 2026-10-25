package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// PushSubscription is one browser's Web Push endpoint and that device's own
// notification settings. The endpoint URL and keys address a device, so the
// record is sealed like message content.
type PushSubscription struct {
	ID       string            `json:"id"`
	Endpoint string            `json:"endpoint"`
	P256dh   string            `json:"p256dh"`
	Auth     string            `json:"auth"`
	Label    string            `json:"label"`
	Created  string            `json:"created"`
	Prefs    NotificationPrefs `json:"prefs"`
}

// NotificationPrefs are configured per device.
type NotificationPrefs struct {
	// Messages is "all", "bot_on", "chosen" or "off".
	Messages      string   `json:"messages"`
	Conversations []string `json:"conversations"`
	Muted         []string `json:"muted"`
	ShowText      bool     `json:"show_text"`
	Approvals     bool     `json:"approvals"`
	Problems      bool     `json:"problems"`
	Connection    bool     `json:"connection"`
}

func DefaultNotificationPrefs() NotificationPrefs {
	return NotificationPrefs{Messages: "all", Conversations: []string{}, Muted: []string{}, ShowText: true, Approvals: true, Problems: true, Connection: true}
}

func (p NotificationPrefs) Valid() bool {
	switch p.Messages {
	case "all", "bot_on", "chosen", "off":
	default:
		return false
	}
	return len(p.Conversations) <= 1000 && len(p.Muted) <= 1000
}

func (s *Store) migratePush() error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`CREATE TABLE IF NOT EXISTS push_subscriptions(id TEXT PRIMARY KEY,payload TEXT NOT NULL,created TEXT NOT NULL); PRAGMA user_version=8;`); err != nil {
		return err
	}
	return tx.Commit()
}

// PushSubscriptionID derives a device ID from its push endpoint.
func PushSubscriptionID(endpoint string) string {
	return Hash("push\x00" + endpoint)
}

// SavePushSubscription registers a device. The ID is derived from the
// endpoint, so registering again refreshes its keys and keeps its label,
// settings and creation time.
func (s *Store) SavePushSubscription(p PushSubscription) (PushSubscription, error) {
	p.ID = PushSubscriptionID(p.Endpoint)
	old, ok, err := s.PushSubscription(p.ID)
	if err != nil {
		return p, err
	}
	if ok {
		p.Created, p.Prefs = old.Created, old.Prefs
		if p.Label == "" {
			p.Label = old.Label
		}
	} else {
		p.Created = time.Now().UTC().Format(time.RFC3339)
		p.Prefs = DefaultNotificationPrefs()
	}
	return p, s.putPushSubscription(p)
}

// UpdatePushSubscription changes a registered device's label and settings.
func (s *Store) UpdatePushSubscription(id, label string, prefs NotificationPrefs) (PushSubscription, bool, error) {
	p, ok, err := s.PushSubscription(id)
	if err != nil || !ok {
		return p, ok, err
	}
	if prefs.Conversations == nil {
		prefs.Conversations = []string{}
	}
	if prefs.Muted == nil {
		prefs.Muted = []string{}
	}
	p.Label, p.Prefs = label, prefs
	return p, true, s.putPushSubscription(p)
}

func (s *Store) PushSubscription(id string) (PushSubscription, bool, error) {
	var enc string
	var p PushSubscription
	err := s.DB.QueryRow("SELECT payload FROM push_subscriptions WHERE id=?", id).Scan(&enc)
	if errors.Is(err, sql.ErrNoRows) {
		return p, false, nil
	}
	if err != nil {
		return p, false, err
	}
	b, err := s.Vault.Open("push:"+id, enc)
	if err != nil {
		return p, false, err
	}
	return p, true, json.Unmarshal(b, &p)
}

func (s *Store) putPushSubscription(p PushSubscription) error {
	b, err := json.Marshal(p)
	if err != nil {
		return err
	}
	enc, err := s.Vault.Seal("push:"+p.ID, b)
	if err != nil {
		return err
	}
	_, err = s.DB.Exec("INSERT INTO push_subscriptions VALUES(?,?,?) ON CONFLICT(id) DO UPDATE SET payload=excluded.payload", p.ID, enc, p.Created)
	return err
}

func (s *Store) PushSubscriptions() ([]PushSubscription, error) {
	rows, err := s.DB.Query("SELECT id,payload FROM push_subscriptions ORDER BY created")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PushSubscription{}
	for rows.Next() {
		var id, enc string
		if err = rows.Scan(&id, &enc); err != nil {
			return nil, err
		}
		b, err := s.Vault.Open("push:"+id, enc)
		if err != nil {
			return nil, err
		}
		var p PushSubscription
		if err = json.Unmarshal(b, &p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) DeletePushSubscription(id string) (bool, error) {
	r, err := s.DB.Exec("DELETE FROM push_subscriptions WHERE id=?", id)
	if err != nil {
		return false, err
	}
	n, err := r.RowsAffected()
	return n > 0, err
}

// NotificationMessage is the newest-first input to a message notification.
type NotificationMessage struct {
	Seq          int64
	ID           string
	Conversation string
	At           string
	Text         string
	Attachments  bool
}

// MessageChangesAfter reads up to limit changes after seq and returns the
// incoming messages among them, oldest first and once each, with the last
// sequence read so other changes still advance the cursor.
func (s *Store) MessageChangesAfter(seq int64, limit int) ([]NotificationMessage, int64, error) {
	rows, err := s.DB.Query(`SELECT c.seq,coalesce(m.id,''),coalesce(m.conversation,''),coalesce(m.direction,''),coalesce(m.at,''),coalesce(m.payload,''),
 EXISTS(SELECT 1 FROM message_meta WHERE message=m.id AND owner_only=1),EXISTS(SELECT 1 FROM message_attachments a WHERE a.message=m.id)
FROM message_changes c LEFT JOIN messages m ON m.id=c.message WHERE c.seq>? ORDER BY c.seq LIMIT ?`, seq, limit)
	if err != nil {
		return nil, seq, err
	}
	type row struct {
		n         NotificationMessage
		direction string
		enc       string
		ownerOnly bool
	}
	var batch []row
	for rows.Next() {
		var r row
		if err = rows.Scan(&r.n.Seq, &r.n.ID, &r.n.Conversation, &r.direction, &r.n.At, &r.enc, &r.ownerOnly, &r.n.Attachments); err != nil {
			rows.Close()
			return nil, seq, err
		}
		batch = append(batch, r)
	}
	if err = rows.Close(); err != nil {
		return nil, seq, err
	}
	out := []NotificationMessage{}
	seen := map[string]bool{}
	for _, r := range batch {
		seq = r.n.Seq
		if r.n.ID == "" || r.direction != "incoming" || r.ownerOnly || seen[r.n.ID] {
			continue
		}
		seen[r.n.ID] = true
		b, err := s.Vault.Open("message:"+r.n.ID, r.enc)
		if err != nil {
			return nil, seq, err
		}
		r.n.Text = string(b)
		out = append(out, r.n)
	}
	return out, seq, nil
}

// LatestMessageChange is the change-feed position a new notifier starts from.
func (s *Store) LatestMessageChange() (int64, error) {
	var n int64
	err := s.DB.QueryRow("SELECT coalesce(max(seq),0) FROM message_changes").Scan(&n)
	return n, err
}

// AlertJob is a job state the owner may want to hear about.
type AlertJob struct {
	ID           string
	Conversation string
	Command      string
	State        string
	Code         string
}

func (s *Store) AlertJobs() ([]AlertJob, error) {
	rows, err := s.DB.Query("SELECT id,conversation,command,state,code FROM jobs WHERE state IN ('awaiting_approval','unknown_submission','failed') ORDER BY created DESC LIMIT 100")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AlertJob{}
	for rows.Next() {
		var j AlertJob
		if err = rows.Scan(&j.ID, &j.Conversation, &j.Command, &j.State, &j.Code); err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}
