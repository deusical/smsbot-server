package bot

import (
	"context"
	"errors"
	"github.com/deusical/smsbot-server/internal/history"
	"github.com/deusical/smsbot-server/internal/transport"
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
	// Enrollment is this route's only grant; collection consent plays no part.
	enrolled := history.Access{
		Allows: func(c transport.Conversation) bool {
			return !c.Group && c.Writable && e.Store.Enrolled(conv, c.ParticipantID)
		},
		Denied: errors.New("enrollment_required"),
	}
	verified, page, _, err := history.ReadPage(ctx, e.Transport, e.Store, source, conv, cursor, enrolled)
	if err != nil {
		return nil, err
	}
	trustedPeer := verified.ParticipantID
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
		if len(m.Attachments) > 0 {
			continue
		}
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
