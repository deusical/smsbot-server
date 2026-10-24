package bot

import (
	"context"
	"github.com/deusical/smsbot-server/internal/history"
	"github.com/deusical/smsbot-server/internal/store"
	"github.com/deusical/smsbot-server/internal/transport"
	"testing"
	"time"
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
	return e.Collection.HistoryHost().Commit(context.Background(), c)
}

func TestImportedSendAliasDoesNotCountAsNewSavedMessage(t *testing.T) {
	e, _ := setup(t)
	cfg := enableInbox(t, e)
	arm(t, e)
	if ok, err := e.Accept(context.Background(), incoming()); err != nil || !ok {
		t.Fatal("fixture command not accepted")
	}
	if err := e.Dispatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	var txn string
	if err := e.Store.DB.QueryRow("SELECT txn FROM jobs WHERE txn!='' LIMIT 1").Scan(&txn); err != nil {
		t.Fatal(err)
	}
	a, err := e.Store.Archive("demo-chat")
	if err != nil {
		a = store.Archive{Conversation: transport.Conversation{ID: "demo-chat", ParticipantID: "demo-peer", Writable: true}, State: "running", Generation: 1}
		if err := e.Store.SaveArchive(a, 0); err != nil {
			t.Fatal(err)
		}
	}
	page := transport.HistoryPage{Messages: []transport.HistoricalMessage{{ID: "new-upstream-alias", TransactionID: txn, Direction: "outgoing", Text: "pong", At: time.Now(), Status: "submitted"}}}
	if err := commitStep(e, history.ImportWork, a, history.Policy{Revision: cfg.Revision}, &page); err != nil {
		t.Fatal(err)
	}
	saved, _ := e.Store.Archive("demo-chat")
	if saved.Imported != 0 || saved.Checked != 1 {
		t.Fatal("existing submitted intent counted as a new saved message")
	}
}
