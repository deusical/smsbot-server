package store

import (
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"github.com/deusical/smsbot-server/internal/media"
	"github.com/deusical/smsbot-server/internal/transport"
	"time"
)

type ReactionView struct {
	Emoji       string `json:"emoji"`
	Count       int    `json:"count"`
	Mine        bool   `json:"mine"`
	Unsupported bool   `json:"unsupported,omitempty"`
}
type Quote struct {
	ID        string `json:"id,omitempty"`
	Text      string `json:"text"`
	Direction string `json:"direction,omitempty"`
	Available bool   `json:"available"`
}

// MutationIntent is sealed separately so completing a job never loses the
// idempotency proof or the evidence needed to reconcile uncertain reactions.
type MutationIntent struct {
	Target     string    `json:"target"`
	WireTarget string    `json:"wire_target"`
	Protocol   string    `json:"protocol"`
	Emoji      string    `json:"emoji"`
	Operation  string    `json:"operation"`
	Version    string    `json:"version"`
	Started    time.Time `json:"started"`
}

func ComposeIntent(text string, files []media.File, target string) string {
	base := media.Intent(text, files)
	if target == "" {
		return base
	}
	b, _ := json.Marshal([]string{base, target, "RCS"})
	return string(b)
}
func (s *Store) SaveReplyRelationship(id, conv, target string) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	err = s.saveInteraction(tx, conv, id, &transport.MessageInteraction{Protocol: "RCS", ReplyTo: target, ObservedAt: time.Now().UTC(), Reactions: []transport.Reaction{}})
	if err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) Mutation(id string) (MutationIntent, error) {
	var enc string
	var out MutationIntent
	if err := s.DB.QueryRow("SELECT payload FROM interaction_intents WHERE job=?", id).Scan(&enc); err != nil {
		return out, err
	}
	b, err := s.Vault.Open("mutation:"+id, enc)
	if err != nil {
		return out, err
	}
	err = json.Unmarshal(b, &out)
	return out, err
}
func (s *Store) ActiveMutation(conv, target string) (string, error) {
	var id string
	err := s.DB.QueryRow(`SELECT a.job FROM interaction_intents a JOIN jobs j ON j.id=a.job WHERE a.target=? AND j.state IN ('queued','generating','submitting','submitted','unknown_submission') LIMIT 1`, Hash(conv+"\x00"+target)).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return id, err
}
func (s *Store) StartMutation(id string, intent MutationIntent) error {
	intent.Started = time.Now().UTC()
	b, err := json.Marshal(intent)
	if err != nil {
		return err
	}
	enc, err := s.Vault.Seal("mutation:"+id, b)
	if err != nil {
		return err
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	r, err := tx.Exec("UPDATE jobs SET state='submitting',txn=? WHERE id=? AND state='queued'", TransactionID(), id)
	if err != nil {
		return err
	}
	n, err := r.RowsAffected()
	if err != nil || n != 1 {
		return errors.New("action_not_queued")
	}
	r, err = tx.Exec("UPDATE interaction_intents SET payload=? WHERE job=?", enc, id)
	if err != nil {
		return err
	}
	n, err = r.RowsAffected()
	if err != nil || n != 1 {
		return errors.New("action_intent_missing")
	}
	return tx.Commit()
}

func (s *Store) Interaction(conv, id string) (*transport.MessageInteraction, error) {
	var enc string
	err := s.DB.QueryRow(`SELECT i.payload FROM message_interactions i JOIN messages m ON m.id=i.message WHERE m.conversation=? AND m.id=?`, conv, id).Scan(&enc)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return s.OpenInteraction(id, enc)
}
func (s *Store) OpenInteraction(id, enc string) (*transport.MessageInteraction, error) {
	b, err := s.Vault.Open("interaction:"+id, enc)
	if err != nil {
		return nil, err
	}
	var v transport.MessageInteraction
	if err = json.Unmarshal(b, &v); err != nil {
		return nil, err
	}
	return &v, nil
}
func (s *Store) saveInteraction(tx *sql.Tx, conv, id string, v *transport.MessageInteraction) error {
	if v == nil {
		return nil
	}
	var enc string
	changed := true
	err := tx.QueryRow("SELECT payload FROM message_interactions WHERE message=?", id).Scan(&enc)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err == nil {
		old, err := s.OpenInteraction(id, enc)
		if err != nil {
			return err
		}
		if old.ObservedAt.After(v.ObservedAt) {
			return nil
		}
		changed = InteractionVersion(old) != InteractionVersion(v)
	}
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	enc, err = s.Vault.Seal("interaction:"+id, b)
	if err != nil {
		return err
	}
	if _, err = tx.Exec("INSERT INTO message_interactions VALUES(?,?) ON CONFLICT(message) DO UPDATE SET payload=excluded.payload", id, enc); err != nil {
		return err
	}
	if changed {
		if _, err = tx.Exec("INSERT INTO message_changes(conversation,message) VALUES(?,?)", conv, id); err != nil {
			return err
		}
	}
	// A snapshot whose read began before submission cannot confirm an operation.
	rows, err := tx.Query(`SELECT a.job,a.payload FROM interaction_intents a JOIN jobs j ON j.id=a.job WHERE a.target=? AND j.state IN ('submitted','unknown_submission')`, Hash(conv+"\x00"+id))
	if err != nil {
		return err
	}
	type pending struct{ id, enc string }
	var all []pending
	for rows.Next() {
		var p pending
		if err = rows.Scan(&p.id, &p.enc); err != nil {
			rows.Close()
			return err
		}
		all = append(all, p)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, p := range all {
		b, err := s.Vault.Open("mutation:"+p.id, p.enc)
		if err != nil {
			return err
		}
		var intent MutationIntent
		if err = json.Unmarshal(b, &intent); err != nil {
			return err
		}
		if intent.Started.IsZero() || v.ObservedAt.Before(intent.Started) || v.OwnerID == "" {
			continue
		}
		mine := ""
		for _, r := range v.Reactions {
			for _, actor := range r.Participants {
				if actor == v.OwnerID {
					mine = r.Emoji
				}
			}
		}
		if (intent.Operation == "remove" && mine == "") || (intent.Operation != "remove" && transport.CanonicalEmoji(mine) == transport.CanonicalEmoji(intent.Emoji)) {
			if _, err = tx.Exec("UPDATE jobs SET state='reaction_confirmed',code='reaction_observed' WHERE id=?", p.id); err != nil {
				return err
			}
		}
	}
	return nil
}
func (s *Store) EnrichMessages(conv string, messages []Message) error {
	for n := range messages {
		m := &messages[n]
		v, err := s.Interaction(conv, m.ID)
		if err != nil {
			return err
		}
		if v == nil {
			m.Protocol = "unknown"
			continue
		}
		m.PendingAction, err = s.ActiveMutation(conv, m.ID)
		if err != nil {
			return err
		}
		m.Protocol = v.Protocol
		m.Reactions = []ReactionView{}
		for _, r := range v.Reactions {
			item := ReactionView{Emoji: r.Emoji, Count: len(r.Participants), Unsupported: r.Unsupported}
			for _, actor := range r.Participants {
				if actor == v.OwnerID && v.OwnerID != "" {
					item.Mine = true
				}
			}
			m.Reactions = append(m.Reactions, item)
		}
		if v.ReplyTo != "" {
			q := &Quote{Text: "Original message unavailable"}
			m.Quote = q
			var id, direction, payload string
			err = s.DB.QueryRow(`SELECT m.id,m.direction,m.payload FROM messages m JOIN message_alias a ON a.message=m.id WHERE m.conversation=? AND a.alias=?`, conv, Hash(conv+"\x00"+v.ReplyTo)).Scan(&id, &direction, &payload)
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			if err != nil {
				return err
			}
			b, err := s.Vault.Open("message:"+id, payload)
			if err != nil {
				return err
			}
			text := []rune(string(b))
			if len(text) > 180 {
				text = append(text[:180], '…')
			}
			if len(text) == 0 {
				text = []rune("Attachment")
			}
			*q = Quote{ID: id, Text: string(text), Direction: direction, Available: true}
		}
	}
	return nil
}
func InteractionVersion(v *transport.MessageInteraction) string {
	if v == nil {
		return ""
	}
	copy := *v
	copy.ObservedAt = time.Time{}
	b, _ := json.Marshal(copy)
	return Hash(string(b))
}
func (s *Store) MessageWindow(conv, id string) (InboxPage, error) {
	var at string
	if err := s.DB.QueryRow("SELECT at FROM messages WHERE conversation=? AND id=?", conv, id).Scan(&at); err != nil {
		return InboxPage{}, err
	}
	// At most 25 newer rows, followed by one normal bounded history page.
	var beforeID, beforeAt string
	err := s.DB.QueryRow("SELECT id,at FROM messages WHERE conversation=? AND (at>? OR (at=? AND id>?)) ORDER BY at,id LIMIT 1 OFFSET 24", conv, at, at, id).Scan(&beforeID, &beforeAt)
	if errors.Is(err, sql.ErrNoRows) {
		return s.OwnerMessagePage(conv, "", "", 50)
	}
	if err != nil {
		return InboxPage{}, err
	}
	b, _ := json.Marshal(inboxCursor{At: beforeAt, ID: beforeID})
	return s.OwnerMessagePage(conv, base64.RawURLEncoding.EncodeToString(b), "", 50)
}
