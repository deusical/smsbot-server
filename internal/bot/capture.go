package bot

import (
	"context"
	"github.com/deusical/smsbot-server/internal/store"
	"github.com/deusical/smsbot-server/internal/transport"
)

// Ingest keeps optional collection off the transport control-event consumer.
// Only failures in the existing command path propagate to its pause handler.
func (e *Engine) Ingest(ctx context.Context, m transport.Incoming) (bool, error) {
	if m.ID != "" && !m.Outgoing {
		first, err := e.Store.FirstObservation(m.ConversationID, m.ID)
		if err != nil {
			return false, err
		}
		if !first {
			m.Historical = true
		}
	}

	e.Collection.QueueObservation(m)
	accepted, err := e.Accept(ctx, m)
	if accepted && err == nil {
		err = e.Store.PublishCommandMessage(store.Hash(m.ConversationID + "\x00" + m.ID))
	}
	return accepted, err
}
