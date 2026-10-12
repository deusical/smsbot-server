package bot

import (
	"context"
	"errors"
	"smsbot-server/internal/store"
	"smsbot-server/internal/transport"
	"time"
)

// ImportHistory has no command execution path. It only retains an explicitly
// requested text page for a currently enrolled individual conversation.
func (e *Engine) ImportHistory(ctx context.Context, conv, cursor string) (map[string]any, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	source, ok := e.Transport.(transport.HistorySource)
	if !ok {
		return nil, errors.New("history_import_unavailable")
	}
	convs, err := e.Transport.Conversations(ctx)
	if err != nil {
		return nil, err
	}
	trustedPeer := ""
	for _, c := range convs {
		if c.ID == conv && !c.Group && c.Writable && e.Store.Enrolled(conv, c.ParticipantID) {
			trustedPeer = c.ParticipantID
			break
		}
	}
	if trustedPeer == "" {
		return nil, errors.New("enrollment_required")
	}
	page, err := source.FetchHistory(ctx, conv, trustedPeer, cursor)
	if err != nil {
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
		if m.At.Before(e.now().AddDate(0, 0, -cfg.Settings.RetentionDays)) {
			continue
		}
		id := store.Hash(conv + "\x00" + m.ID)
		if err = e.Store.SaveMessageAt(id, conv, m.Direction, m.Text, m.At); err != nil {
			return nil, err
		}
		count++
	}
	return map[string]any{"imported": count, "next_cursor": page.NextCursor, "available_pages_exhausted": page.NextCursor == "", "text_only": true, "observed_at": e.now().UTC().Format(time.RFC3339)}, nil
}
