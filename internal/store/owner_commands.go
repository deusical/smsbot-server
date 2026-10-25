package store

import "strings"

// EnrolledParticipant returns the peer enrolled for bot replies in conv.
func (s *Store) EnrolledParticipant(conv string) (string, bool) {
	var peer string
	return peer, s.DB.QueryRow("SELECT participant FROM enrollment WHERE conversation=?", conv).Scan(&peer) == nil
}

// SentByBotIdentity recognizes only exact send transactions or recorded AI
// answer IDs. Text equality is not proof for a native thread follow-up.
func (s *Store) SentByBotIdentity(conv, txn, wire string) (bool, error) {
	if txn != "" {
		var ours bool
		if err := s.DB.QueryRow("SELECT EXISTS(SELECT 1 FROM jobs WHERE conversation=? AND txn=? AND command LIKE '!%')", conv, txn).Scan(&ours); err != nil || ours {
			return ours, err
		}
	}
	if wire == "" {
		return false, nil
	}
	answer, err := s.FindAIParent(conv, wire)
	return answer != nil, err
}

// SentByBot reports whether an outgoing message observed from the phone is a
// command reply this service sent: its transaction names one, or its text
// matches one of the latest replies sent into conv. Owner commands use it so
// the bot never answers itself. Dashboard sends are the owner's own messages
// and are not matched, so a command typed there runs like one typed on the
// phone.
func (s *Store) SentByBot(conv, txn, text string) (bool, error) {
	if txn != "" {
		var ours bool
		if err := s.DB.QueryRow("SELECT EXISTS(SELECT 1 FROM jobs WHERE conversation=? AND txn=? AND command LIKE '!%')", conv, txn).Scan(&ours); err != nil || ours {
			return ours, err
		}
	}
	rows, err := s.DB.Query(`SELECT m.id,m.payload FROM jobs j JOIN messages m ON m.id=j.txn AND m.conversation=j.conversation
WHERE j.conversation=? AND j.txn!='' AND j.command LIKE '!%' AND m.direction='outgoing' ORDER BY j.created DESC LIMIT 20`, conv)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	text = strings.TrimSpace(text)
	for rows.Next() {
		var id, enc string
		if err = rows.Scan(&id, &enc); err != nil {
			return false, err
		}
		plain, err := s.Vault.Open("message:"+id, enc)
		if err != nil {
			return false, err
		}
		if strings.TrimSpace(string(plain)) == text {
			return true, nil
		}
	}
	return false, rows.Err()
}

// FirstSighting records that an outgoing message was seen and reports whether
// this is the first time. Unlike FirstObservation it ignores aliases, which
// tie the phone's copy of a dashboard send to the send itself.
func (s *Store) FirstSighting(conv, upstream string) (bool, error) {
	r, err := s.DB.Exec("INSERT OR IGNORE INTO observed_events VALUES(?)", Hash(conv+"\x00"+upstream))
	if err != nil {
		return false, err
	}
	n, err := r.RowsAffected()
	return n > 0, err
}
