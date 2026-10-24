package store

import "strings"

// EnrolledParticipant returns the peer enrolled for bot replies in conv.
func (s *Store) EnrolledParticipant(conv string) (string, bool) {
	var peer string
	return peer, s.DB.QueryRow("SELECT participant FROM enrollment WHERE conversation=?", conv).Scan(&peer) == nil
}

// SentByService reports whether an outgoing message observed from the phone
// is one this service sent: its transaction names one of our sends, or its
// text matches one of the latest messages we sent into conv. Owner commands
// use it so the bot never answers its own replies or dashboard sends.
func (s *Store) SentByService(conv, txn, text string) (bool, error) {
	if txn != "" {
		var ours bool
		if err := s.DB.QueryRow("SELECT EXISTS(SELECT 1 FROM jobs WHERE conversation=? AND txn=?)", conv, txn).Scan(&ours); err != nil || ours {
			return ours, err
		}
	}
	rows, err := s.DB.Query(`SELECT m.id,m.payload FROM jobs j JOIN messages m ON m.id=j.txn AND m.conversation=j.conversation
WHERE j.conversation=? AND j.txn!='' AND m.direction='outgoing' ORDER BY j.created DESC LIMIT 20`, conv)
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
