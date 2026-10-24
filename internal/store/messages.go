package store

import (
	"context"
)

// MessageOrigin returns a saved message's origin ("web", "phone", "history",
// or "" when unknown) and its timestamp.
func (s *Store) MessageOrigin(conv, id string) (origin, at string, err error) {
	err = s.DB.QueryRow(`SELECT coalesce(meta.origin,''),m.at FROM messages m LEFT JOIN message_meta meta ON meta.message=m.id WHERE m.conversation=? AND m.id=?`, conv, id).Scan(&origin, &at)
	return origin, at, err
}

// AttachmentVersion is an opaque fingerprint of a message's stored file
// metadata (its sealed payload). It changes whenever file state is saved.
func (s *Store) AttachmentVersion(message string) (string, error) {
	var payload string
	err := s.DB.QueryRow("SELECT coalesce((SELECT payload FROM message_attachments WHERE message=?),'')", message).Scan(&payload)
	return payload, err
}

// MessageKnown reports whether an upstream message is already saved in conv,
// by its upstream ID, a recorded alias, or (for outgoing messages) the
// transaction of a job that submitted it.
func (s *Store) MessageKnown(conv, upstream, direction, txn string) (bool, error) {
	id := Hash(conv + "\x00" + upstream)
	var known bool
	err := s.DB.QueryRow(`SELECT EXISTS(SELECT 1 FROM messages WHERE conversation=? AND (id=? OR id IN (SELECT message FROM message_alias WHERE alias=?) OR (?='outgoing' AND id=? AND EXISTS(SELECT 1 FROM jobs WHERE conversation=? AND txn=?))))`, conv, id, id, direction, txn, conv, txn).Scan(&known)
	return known, err
}

// MessageExists reports whether message id belongs to conv.
func (s *Store) MessageExists(conv, id string) (bool, error) {
	var n int
	err := s.DB.QueryRow("SELECT count(*) FROM messages WHERE conversation=? AND id=?", conv, id).Scan(&n)
	return n == 1, err
}

// Revision is one earlier observed text of a message.
type Revision struct {
	Revision   int    `json:"revision"`
	Text       string `json:"text"`
	ObservedAt string `json:"observed_at"`
}

// MessageRevisions returns a message's earlier observed texts, oldest first.
func (s *Store) MessageRevisions(id string) ([]Revision, error) {
	rows, err := s.DB.Query("SELECT revision,payload,observed FROM message_revisions WHERE message=? ORDER BY revision", id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Revision{}
	for rows.Next() {
		var r Revision
		var enc string
		if err = rows.Scan(&r.Revision, &enc, &r.ObservedAt); err != nil {
			return nil, err
		}
		b, err := s.Vault.Open("message:"+id, enc)
		if err != nil {
			return nil, err
		}
		r.Text = string(b)
		out = append(out, r)
	}
	return out, rows.Err()
}

// ArchivedMessageCount counts saved messages in archived conversations.
func (s *Store) ArchivedMessageCount(ctx context.Context) (int64, error) {
	var n int64
	err := s.DB.QueryRowContext(ctx, `SELECT count(*) FROM messages WHERE conversation IN (SELECT conversation FROM archive_conversations)`).Scan(&n)
	return n, err
}

// LatestChange is the newest message change number, or zero.
func (s *Store) LatestChange(ctx context.Context) (int64, error) {
	var n int64
	err := s.DB.QueryRowContext(ctx, "SELECT coalesce(max(seq),0) FROM message_changes").Scan(&n)
	return n, err
}

// PreserveHistory protects saved messages of the given conversations (or of
// every conversation without a verified archive, when all is true) from the
// legacy expiry pass. The markers grant no viewing or collection rights;
// recording a verified archive clears them.
func (s *Store) PreserveHistory(all bool, conversations []string) error {
	if all {
		_, err := s.DB.Exec("INSERT OR IGNORE INTO archive_pending SELECT DISTINCT conversation FROM messages WHERE conversation NOT IN (SELECT conversation FROM archive_conversations)")
		return err
	}
	for _, conv := range conversations {
		if _, err := s.DB.Exec("INSERT OR IGNORE INTO archive_pending SELECT ? WHERE NOT EXISTS(SELECT 1 FROM archive_conversations WHERE conversation=?)", conv, conv); err != nil {
			return err
		}
	}
	return nil
}

// HasEnrollment reports whether any participant is enrolled in conv.
func (s *Store) HasEnrollment(conv string) bool {
	var n int
	return s.DB.QueryRow("SELECT count(*) FROM enrollment WHERE conversation=?", conv).Scan(&n) == nil && n == 1
}

// RevokeSession deletes a browser session credential. Owner credentials are
// never affected.
func (s *Store) RevokeSession(token string) error {
	_, err := s.DB.Exec("DELETE FROM credentials WHERE hash=? AND kind='session'", Hash(token))
	return err
}
