package bot

import (
	"context"
	"github.com/deusical/smsbot-server/internal/history"
	"github.com/deusical/smsbot-server/internal/store"
	"github.com/deusical/smsbot-server/internal/transport"
	"testing"
	"time"
)

type viewedHistoryTransport struct {
	*transport.Fixture
	at time.Time
}

func (f *viewedHistoryTransport) HistoryCursorBefore(time.Time) string { return "view-start" }
func (f *viewedHistoryTransport) FetchHistory(ctx context.Context, conv, peer, cursor string) (transport.HistoryPage, error) {
	switch cursor {
	case "view-start":
		return transport.HistoryPage{Messages: []transport.HistoricalMessage{{ID: "edited", Direction: "incoming", Text: "Synthetic corrected text", At: f.at}, {ID: "gap", Direction: "incoming", Text: "Synthetic previously missing message", At: f.at.Add(time.Minute)}}, NextCursor: "view-next"}, nil
	case "view-next":
		return transport.HistoryPage{Messages: []transport.HistoricalMessage{{ID: "below-range", Direction: "incoming", Text: "Synthetic out of range", At: f.at.Add(-time.Hour)}}}, nil
	}
	return transport.HistoryPage{}, nil
}
func TestViewedRefreshRepairsGapsAndEditsWithoutReplacingDeepImport(t *testing.T) {
	e, f := setup(t)
	settings := enableInbox(t, e)
	settings.ArchiveRetentionDays = 0
	if err := e.SetInboxSettings(context.Background(), settings); err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(-365 * 24 * time.Hour)
	e.Transport = &viewedHistoryTransport{Fixture: f, at: at}
	a := store.Archive{Conversation: transport.Conversation{ID: "demo-chat", ParticipantID: "demo-peer", Writable: true}, Since: time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano), State: "running", Cursor: "deep-checkpoint", Generation: 3, RetryAt: time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano), RecentRetryAt: time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano)}
	if err := e.Store.SaveArchive(a, 0); err != nil {
		t.Fatal(err)
	}
	if err := e.Store.SaveObservedFiles("demo-chat", "edited", "", "incoming", "Synthetic stale text", "history", "", at, nil); err != nil {
		t.Fatal(err)
	}
	queued, err := e.RequestArchiveRefresh(context.Background(), "demo-chat", at.Add(-time.Minute).UTC().Format(time.RFC3339Nano), at.Add(time.Hour).UTC().Format(time.RFC3339Nano))
	if err != nil || queued.Cursor != a.Cursor || queued.Since != a.Since || queued.State != a.State {
		t.Fatal("view replaced deep import", err)
	}
	// A phone page started before the new range request cannot overwrite its state.
	if err := (archiveHost{e}).Commit(context.Background(), a, history.Policy{Revision: settings.Revision}, transport.HistoryPage{}, true); err == nil {
		t.Fatal("superseded generation committed")
	}
	e.History.Tick(context.Background())
	e.History.Tick(context.Background())
	saved, err := e.Store.OwnerMessagePage("demo-chat", "", "", 50)
	if err != nil || len(saved.Messages) != 2 {
		t.Fatal("missing range messages", err)
	}
	foundEdit, foundGap := false, false
	for _, m := range saved.Messages {
		foundEdit = foundEdit || m.Text == "Synthetic corrected text"
		foundGap = foundGap || m.Text == "Synthetic previously missing message"
	}
	current, _ := e.Store.Archive("demo-chat")
	if !foundEdit || !foundGap || current.RefreshState != "checked_available" || current.RefreshCheckedAt == "" || current.Cursor != a.Cursor || current.State != a.State {
		t.Fatal("view reconciliation did not preserve independent progress")
	}
	jobs, _ := e.Store.Jobs()
	if len(jobs) != 0 {
		t.Fatal("historical refresh routed commands")
	}
}

func TestExplicitDeepImportPreservesViewedRefresh(t *testing.T) {
	e, _ := setup(t)
	enableInbox(t, e)
	a, err := e.RequestArchiveRefresh(context.Background(), "demo-chat", time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano), "")
	if err != nil {
		t.Fatal(err)
	}
	a.RefreshCursor = "view-checkpoint"
	a.RefreshState = "running"
	if err := e.Store.SaveArchive(a, 0); err != nil {
		t.Fatal(err)
	}
	deep, err := e.RequestArchive(context.Background(), "demo-chat", time.Now().Add(-24*time.Hour).UTC().Format(time.RFC3339Nano), false)
	if err != nil || deep.RefreshCursor != a.RefreshCursor || deep.RefreshSince != a.RefreshSince || deep.RefreshState != "running" {
		t.Fatal("deep import erased viewed progress", err)
	}
}

type revokeViewedTransport struct {
	*viewedHistoryTransport
	revoke func()
}

func (f *revokeViewedTransport) FetchHistory(ctx context.Context, conv, peer, cursor string) (transport.HistoryPage, error) {
	if cursor == "view-start" {
		f.revoke()
	}
	return f.viewedHistoryTransport.FetchHistory(ctx, conv, peer, cursor)
}
func TestViewedRefreshCannotPublishAfterConsentRevocation(t *testing.T) {
	e, f := setup(t)
	enableInbox(t, e)
	at := time.Now().Add(-time.Hour)
	e.Transport = &revokeViewedTransport{viewedHistoryTransport: &viewedHistoryTransport{Fixture: f, at: at}, revoke: func() {
		cfg, _ := e.InboxSettings()
		cfg.Enabled = false
		if err := e.SetInboxSettings(context.Background(), cfg); err != nil {
			t.Error(err)
		}
	}}
	if _, err := e.RequestArchiveRefresh(context.Background(), "demo-chat", at.Add(-time.Minute).UTC().Format(time.RFC3339Nano), ""); err != nil {
		t.Fatal(err)
	}
	e.History.Tick(context.Background())
	page, err := e.Store.OwnerMessagePage("demo-chat", "", "", 50)
	if err != nil || len(page.Messages) != 0 {
		t.Fatal("revoked view published phone data", err)
	}
}
