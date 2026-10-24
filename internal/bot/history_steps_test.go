package bot

import (
	"context"
	"github.com/deusical/smsbot-server/internal/history"
	"github.com/deusical/smsbot-server/internal/store"
	"github.com/deusical/smsbot-server/internal/transport"
)

// commitStep publishes a's checkpoint for w through the real host, as a
// scheduler step that read a before its fetch would.
func commitStep(e *Engine, w history.Work, a store.Archive, p history.Policy, page *transport.HistoryPage) error {
	c := history.Commit{Work: w, Conversation: a.Conversation, Policy: p, Page: page}
	switch w {
	case history.ImportWork:
		c.Token = a.Generation
		c.Apply = func(r *store.Archive) { r.ImportCheckpoint = a.ImportCheckpoint }
	case history.RecentWork:
		c.Apply = func(r *store.Archive) { r.RecentCheckpoint = a.RecentCheckpoint }
	case history.ViewWork:
		c.Token = a.ViewGeneration
		c.Apply = func(r *store.Archive) { r.ViewCheckpoint = a.ViewCheckpoint }
	}
	return archiveHost{e}.Commit(context.Background(), c)
}
