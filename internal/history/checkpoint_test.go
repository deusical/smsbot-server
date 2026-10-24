package history

import (
	"context"
	"github.com/deusical/smsbot-server/internal/store"
	"github.com/deusical/smsbot-server/internal/transport"
	"github.com/deusical/smsbot-server/internal/vault"
	"path/filepath"
	"testing"
	"time"
)

type pagedHost struct{ *retryHost }

func (h *pagedHost) Fetch(_ context.Context, _ transport.Conversation, cursor string) (transport.HistoryPage, error) {
	h.calls = append(h.calls, cursor)
	// Every lane finishes in one page except the import, which has more.
	if cursor == "import" {
		return transport.HistoryPage{NextCursor: "import-2"}, nil
	}
	return transport.HistoryPage{}, nil
}

// Each step publishes only its own checkpoint, so steps from one snapshot can
// commit in any order without erasing one another's progress.
func TestLaneCommitsFromOneSnapshotKeepEachOthersProgress(t *testing.T) {
	v, _ := vault.New(make([]byte, 32))
	s, err := store.Open(filepath.Join(t.TempDir(), "state.db"), v)
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	now := time.Now()
	c := transport.Conversation{ID: "chat", ParticipantID: "peer", Writable: true}
	a := store.Archive{Conversation: c, Generation: 4, FocusAt: now.UTC().Format(time.RFC3339Nano),
		ImportCheckpoint: store.ImportCheckpoint{State: "running", Cursor: "import", RequestSource: "owner", RequestedAt: now.UTC().Format(time.RFC3339Nano)},
		ViewCheckpoint:   store.ViewCheckpoint{RefreshState: "queued", RefreshCursor: "view", RefreshSince: now.Add(-time.Hour).UTC().Format(time.RFC3339Nano), ViewGeneration: 2}}
	if err = s.SaveArchive(a, 0); err != nil {
		t.Fatal(err)
	}
	h := &pagedHost{&retryHost{s: s, fail: map[string]bool{}}}
	worker := New(s, h)
	worker.nextFallback = now.Add(-time.Second) // routine reconciliation is due
	worker.nextDiscovery = now.Add(time.Hour)
	worker.Tick(context.Background())
	if len(h.calls) != 3 {
		t.Fatalf("want view, import and recent steps, got %v", h.calls)
	}
	got, err := s.Archive("chat")
	if err != nil {
		t.Fatal(err)
	}
	if got.RefreshState != "checked_available" || got.ViewGeneration != 2 || got.FocusAt != a.FocusAt {
		t.Fatalf("view progress lost: %+v", got.ViewCheckpoint)
	}
	if got.Pages != 1 || got.Cursor != "import-2" || got.Generation != 4 {
		t.Fatalf("import progress lost: %+v", got.ImportCheckpoint)
	}
	if got.LastSync == "" || got.RecentStarted != "" {
		t.Fatalf("recent progress lost: %+v", got.RecentCheckpoint)
	}
	// A late import commit read from the original snapshot still leaves the
	// other workflows' newer checkpoints in place.
	if err = worker.importStep(context.Background(), a, Policy{}, true); err != nil {
		t.Fatal(err)
	}
	again, _ := s.Archive("chat")
	if again.LastSync != got.LastSync || again.RefreshState != "checked_available" {
		t.Fatal("import commit erased recent or view progress")
	}
	// A restart supersedes the snapshot's import; its in-flight page is rejected.
	if _, err = s.UpdateArchive("chat", 0, func(r *store.Archive, _ bool) error { r.Generation++; return nil }); err != nil {
		t.Fatal(err)
	}
	if err = worker.importStep(context.Background(), a, Policy{}, true); err != ErrJobChanged {
		t.Fatal("superseded import committed", err)
	}
}
