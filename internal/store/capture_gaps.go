package store

import (
	"encoding/json"
	"time"
)

// CaptureGap contains no message text, file keys, or raw transport errors.
// The key hashes the conversation and the complete payload is encrypted.
type CaptureGap struct {
	Conversation string `json:"conversation"`
	Kind         string `json:"kind,omitempty"`
	ObservedAt   string `json:"observed_at,omitempty"`
	Since        string `json:"since"`
	Until        string `json:"until"`
	Reason       string `json:"reason"`
	State        string `json:"state"`
	CheckedAt    string `json:"checked_at,omitempty"`
	RetryAt      string `json:"retry_at,omitempty"`
	Failures     int    `json:"-"`
	Cursor       string `json:"-"`
	Generation   int64  `json:"-"`
}

// Private checkpoint fields must persist but must never be returned by the API.
type storedCaptureGap struct {
	CaptureGap
	PrivateCursor     string `json:"cursor,omitempty"`
	PrivateFailures   int    `json:"failures,omitempty"`
	PrivateGeneration int64  `json:"generation"`
}

func (s *Store) SaveCaptureGap(g CaptureGap) error {
	b, err := json.Marshal(storedCaptureGap{g, g.Cursor, g.Failures, g.Generation})
	if err != nil {
		return err
	}
	return s.SetSecretSetting("capture_gap:"+Hash(g.Conversation), "capture-gap:"+Hash(g.Conversation), b)
}
func (s *Store) CaptureGaps() ([]CaptureGap, error) {
	rows, err := s.DB.Query("SELECT key,value FROM settings WHERE key GLOB 'capture_gap:*' ORDER BY key")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CaptureGap{}
	for rows.Next() {
		var key, encrypted string
		if err = rows.Scan(&key, &encrypted); err != nil {
			return nil, err
		}
		b, err := s.Vault.Open("capture-gap:"+key[len("capture_gap:"):], encrypted)
		if err != nil {
			return nil, err
		}
		var value storedCaptureGap
		if err = json.Unmarshal(b, &value); err != nil {
			return nil, err
		}
		g := value.CaptureGap
		g.Cursor = value.PrivateCursor
		g.Failures = value.PrivateFailures
		g.Generation = value.PrivateGeneration
		out = append(out, g)
	}
	return out, rows.Err()
}
func GapRetryReady(g CaptureGap) bool {
	at, _ := time.Parse(time.RFC3339Nano, g.RetryAt)
	return !time.Now().Before(at)
}
