package bot

import (
	"context"
	"errors"
	"github.com/deusical/smsbot-server/internal/transport"
)

// historyAccess is one entry point's permission rule for an owner-requested
// history page. The read sequence below is shared; the rules are not: enrolled
// bot history, collected inbox history and durable archive jobs never borrow
// one another's grants, retention or persistence.
type historyAccess struct {
	allows func(transport.Conversation) bool // checked before and after the fetch
	denied error                             // returned when allows fails first
}

var errIdentityChanged = errors.New("conversation_identity_changed")

// readHistoryPage is the shared bounded read: verify identity and access, record
// the change watermark, fetch one page for that exact participant, then verify
// identity and access again so a page read for a conversation whose identity
// changed is never returned. No engine lock is held; callers re-check their
// policy under the lock before saving anything.
func (e *Engine) readHistoryPage(ctx context.Context, source transport.HistorySource, conv, cursor string, access historyAccess) (transport.Conversation, transport.HistoryPage, int64, error) {
	c, err := e.lookupConversation(ctx, conv)
	if err != nil || !access.allows(c) {
		return c, transport.HistoryPage{}, 0, access.denied
	}
	snapshot, err := e.Store.ChangeWatermark(conv)
	if err != nil {
		return c, transport.HistoryPage{}, 0, err
	}
	page, err := source.FetchHistory(ctx, conv, c.ParticipantID, cursor)
	if err != nil {
		return c, page, 0, err
	}
	verified, err := e.lookupConversation(ctx, conv)
	if err != nil || verified.ParticipantID != c.ParticipantID || !access.allows(verified) {
		return c, transport.HistoryPage{}, 0, errIdentityChanged
	}
	return verified, page, snapshot, nil
}
