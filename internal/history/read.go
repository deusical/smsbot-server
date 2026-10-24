package history

import (
	"context"
	"errors"
	"github.com/deusical/smsbot-server/internal/store"
	"github.com/deusical/smsbot-server/internal/transport"
)

// Access is one entry point's permission rule for an owner-requested history
// page. The read sequence is shared; the rules are not: enrolled bot history,
// collected inbox history and durable archive jobs never borrow one another's
// grants, retention or persistence.
type Access struct {
	Allows func(transport.Conversation) bool // checked before and after the fetch
	Denied error                             // returned when Allows fails first
}

// ErrIdentityChanged rejects a page whose conversation changed identity or
// lost access while it was being read.
var ErrIdentityChanged = errors.New("conversation_identity_changed")

// ReadPage is the shared bounded read: verify identity and access, record the
// change watermark, fetch one page for that exact participant, then verify
// identity and access again so a page read for a conversation whose identity
// changed is never returned. It holds no lock; callers re-check their policy
// under the control lock before saving anything.
func ReadPage(ctx context.Context, t transport.Transport, s *store.Store, source transport.HistorySource, conv, cursor string, access Access) (transport.Conversation, transport.HistoryPage, int64, error) {
	c, err := transport.Lookup(ctx, t, conv)
	if err != nil || !access.Allows(c) {
		return c, transport.HistoryPage{}, 0, access.Denied
	}
	snapshot, err := s.ChangeWatermark(conv)
	if err != nil {
		return c, transport.HistoryPage{}, 0, err
	}
	page, err := source.FetchHistory(ctx, conv, c.ParticipantID, cursor)
	if err != nil {
		return c, page, 0, err
	}
	verified, err := transport.Lookup(ctx, t, conv)
	if err != nil || verified.ParticipantID != c.ParticipantID || !access.Allows(verified) {
		return c, transport.HistoryPage{}, 0, ErrIdentityChanged
	}
	return verified, page, snapshot, nil
}
