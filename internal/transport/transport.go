package transport

import (
	"context"
	"errors"
	"smsbot-server/internal/media"
	"sync"
	"time"
)

type Conversation struct {
	Archived      bool   `json:"archived,omitempty"`
	ID            string `json:"id"`
	ParticipantID string `json:"participant_id"`
	Label         string `json:"label"`
	Protocol      string `json:"protocol"`
	Group         bool   `json:"group"`
	Writable      bool   `json:"writable"`
	Enrolled      bool   `json:"enrolled"`
	// LastMessageAt (RFC3339), Unread and Pinned mirror the phone list for ordering.
	// They are volatile display hints and never part of identity checks.
	LastMessageAt string `json:"last_message_at,omitempty"`
	Unread        bool   `json:"unread,omitempty"`
	Pinned        bool   `json:"pinned,omitempty"`
}
type Incoming struct {
	Attachments    []media.File `json:"-"`
	ID             string       `json:"id"`
	ConversationID string       `json:"conversation_id"`
	ParticipantID  string       `json:"participant_id"`
	Text           string       `json:"text"`
	At             time.Time    `json:"at"`
	Historical     bool         `json:"historical"`
	Outgoing       bool         `json:"outgoing"`
	TransactionID  string       `json:"transaction_id,omitempty"`
	Status         string       `json:"status,omitempty"`
}
type ConversationLookup interface {
	LookupConversation(context.Context, string) (Conversation, error)
}
type Receipt struct {
	State     string
	MessageID string
}
type Transport interface {
	Conversations(context.Context) ([]Conversation, error)
	Send(context.Context, string, string, string) (Receipt, error)
	Ready() bool
}

var ErrUnavailable = errors.New("phone connection unavailable")

var ErrUnknown = errors.New("submission outcome unknown")
var ErrNotSubmitted = errors.New("transport did not submit")

// Fixture has no network implementation; it cannot contact Google or a recipient.
type Fixture struct {
	mu     sync.Mutex
	Online bool
	Fail   string
	Sent   map[string]string
}

func NewFixture() *Fixture     { return &Fixture{Online: true, Sent: map[string]string{}} }
func (f *Fixture) Ready() bool { f.mu.Lock(); defer f.mu.Unlock(); return f.Online }
func (f *Fixture) Conversations(context.Context) ([]Conversation, error) {
	return []Conversation{{ID: "demo-chat", ParticipantID: "demo-peer", Label: "Demo conversation", Protocol: "RCS", Writable: true}, {ID: "demo-sms", ParticipantID: "demo-sms-peer", Label: "SMS example", Protocol: "SMS", Writable: true, Pinned: true}, {ID: "demo-group", Label: "Group example", Protocol: "RCS", Group: true, Writable: false}}, nil
}
func (f *Fixture) Send(ctx context.Context, conv, txn, text string) (Receipt, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if e := ctx.Err(); e != nil {
		return Receipt{}, ErrNotSubmitted
	}
	if !f.Online {
		return Receipt{}, ErrNotSubmitted
	}
	if f.Fail == "unknown" {
		return Receipt{}, ErrUnknown
	}
	if f.Fail != "" {
		return Receipt{}, ErrNotSubmitted
	}
	f.Sent[txn] = text
	return Receipt{State: "submitted", MessageID: txn}, nil
}
