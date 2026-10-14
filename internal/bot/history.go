package bot

import (
	"context"
	"errors"
	"smsbot-server/internal/transport"
	"time"
)

// ImportHistory has no command execution path. It only retains an explicitly
// requested text page for a currently enrolled individual conversation.
func (e *Engine) ImportHistory(ctx context.Context, conv, cursor string) (map[string]any, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	source, ok := e.Transport.(transport.HistorySource)
	if !ok {
		return nil, errors.New("history_import_unavailable")
	}
	c, err := e.lookupConversation(ctx, conv)
	if err != nil || c.Group || !c.Writable || !e.Store.Enrolled(conv, c.ParticipantID) {
		return nil, errors.New("enrollment_required")
	}
	trustedPeer := c.ParticipantID
	page, err := source.FetchHistory(ctx, conv, trustedPeer, cursor)
	if err != nil {
		return nil, err
	}
	verified, err := e.lookupConversation(ctx, conv)
	if err != nil || verified.Group || !verified.Writable || verified.ParticipantID != trustedPeer {
		return nil, errors.New("conversation_identity_changed")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if !e.Store.Enrolled(conv, trustedPeer) {
		return nil, errors.New("enrollment_changed")
	}
	cfg, err := e.Configuration()
	if err != nil {
		return nil, err
	}
	count := 0
	for _, m := range page.Messages {
  if len(m.Attachments) > 0 { continue }
		if cfg.Settings.RetentionDays > 0 && m.At.Before(e.now().AddDate(0, 0, -cfg.Settings.RetentionDays)) {
			continue
		}
		if err = e.Store.SaveLegacyImported(conv, m.ID, m.TransactionID, m.Direction, m.Text, m.Status, m.At); err != nil {
			return nil, err
		}
		count++
	}
	return map[string]any{"imported": count, "next_cursor": page.NextCursor, "available_pages_exhausted": page.NextCursor == "", "text_only": true, "observed_at": e.now().UTC().Format(time.RFC3339)}, nil
}
