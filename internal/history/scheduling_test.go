package history

import (
	"context"
	"fmt"
	"path/filepath"
	"smsbot-server/internal/store"
	"smsbot-server/internal/transport"
	"smsbot-server/internal/vault"
	"testing"
	"time"
)

type replayHost struct {
	*retryHost
	ids              []string
	discoveryCalls   int
	discoveryFailure bool
}

func (h *replayHost) Discover(context.Context) ([]transport.Conversation, error) {
	h.discoveryCalls++
	if h.discoveryFailure {
		return nil, fmt.Errorf("synthetic discovery failure")
	}
	// Deliberately reverse discovery order; job fairness must not depend on it.
	out := []transport.Conversation{}
	for i := len(h.ids) - 1; i >= 0; i-- {
		out = append(out, transport.Conversation{ID: h.ids[i], ParticipantID: "peer", Writable: true})
	}
	return out, nil
}
func (h *replayHost) Fetch(ctx context.Context, c transport.Conversation, cursor string) (transport.HistoryPage, error) {
	if _, ok := ctx.Deadline(); !ok {
		return transport.HistoryPage{}, fmt.Errorf("missing deadline")
	}
	h.calls = append(h.calls, c.ID+":"+cursor)
	if cursor != "" {
		return transport.HistoryPage{NextCursor: cursor + "x"}, nil
	}
	return transport.HistoryPage{}, nil
}
func TestBacklogDoesNotAccelerateCompletedArchivePolling(t *testing.T) {
	v, _ := vault.New(make([]byte, 32))
	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"), v)
	if err != nil {
		t.Fatal(err)
	}
	defer db.DB.Close()
	host := &replayHost{retryHost: &retryHost{s: db, fail: map[string]bool{}}}
	now := time.Now()
	for i := 0; i < 317; i++ {
		id := fmt.Sprintf("chat-%03d", i)
		host.ids = append(host.ids, id)
		a := store.Archive{Conversation: transport.Conversation{ID: id, ParticipantID: "peer", Writable: true}, State: "available_pages_exhausted", Generation: 1, LastSync: now.Format(time.RFC3339Nano)}
		if i < 6 {
			a.State = "running"
			a.Cursor = "older"
		}
		if err := db.SaveArchive(a, 0); err != nil {
			t.Fatal(err)
		}
	}
	worker := New(db, host)
	worker.now = func() time.Time { return now }
	worker.nextFallback = now.Add(idleInterval)
	for i := 0; i < 60; i++ {
		worker.Tick(context.Background())
		now = now.Add(activeInterval)
	}
	if len(host.calls) != 60 {
		t.Fatalf("want one useful backfill per turn, got %d calls", len(host.calls))
	}
	for _, id := range host.ids[:6] {
		a, _ := db.Archive(id)
		if a.Pages != 10 {
			t.Fatalf("backfill starved: pages=%d", a.Pages)
		}
	}
	if host.discoveryCalls != 1 {
		t.Fatalf("discovery accelerated with backlog: %d", host.discoveryCalls)
	}
	for _, id := range host.ids[6:] {
		a, _ := db.Archive(id)
		if a.LastSync != worker.now().Add(-60*activeInterval).Format(time.RFC3339Nano) {
			t.Fatal("completed conversation polled by unrelated backfill")
		}
	}
	// A due fallback is still performed, with its checkpoint preserved on restart.
	now = worker.nextFallback
	before := len(host.calls)
	worker.Tick(context.Background())
	if len(host.calls) <= before {
		t.Fatal("due reconciliation suppressed")
	}
	restarted := newService(db, host, worker.now)
	restarted.now = worker.now
	// Normal restart preserves both independent rotations and the next fallback.
	raw, err := db.GetSecretSetting("history_schedule", "history-schedule")
	if err != nil || len(raw) == 0 {
		t.Fatal("schedule not persisted")
	}
	if restarted.backgroundAfter != worker.backgroundAfter || restarted.fallbackAfter != worker.fallbackAfter || !restarted.nextFallback.Equal(worker.nextFallback) {
		t.Fatal("restart lost stable rotation")
	}
	// The six deep jobs must not trap fallback rotation in their own IDs.
	for i := 0; i < 7; i++ {
		now = worker.nextFallback
		worker.Tick(context.Background())
	}
	found := false
	for _, call := range host.calls {
		if call == "chat-006:" {
			found = true
		}
	}
	if !found {
		t.Fatal("unfinished jobs starved completed-archive reconciliation")
	}
}
func TestSavedAutomaticImportAdvancesDespiteDiscoveryFailure(t *testing.T) {
	v, _ := vault.New(make([]byte, 32))
	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"), v)
	if err != nil {
		t.Fatal(err)
	}
	defer db.DB.Close()
	host := &replayHost{retryHost: &retryHost{s: db}, discoveryFailure: true}
	worker := New(db, host)
	worker.nextFallback = time.Now().Add(time.Hour)
	a := store.Archive{Conversation: transport.Conversation{ID: "chat", ParticipantID: "peer", Writable: true}, State: "running", Cursor: "older", Generation: 1}
	if err := db.SaveArchive(a, 0); err != nil {
		t.Fatal(err)
	}
	worker.Tick(context.Background())
	a, _ = db.Archive("chat")
	if a.Pages != 1 {
		t.Fatal("discovery failure blocked saved import")
	}
}

type futureGapHost struct {
	*retryHost
	at time.Time
}

func (h *futureGapHost) NextGapAttempt() (time.Time, bool) { return h.at, true }
func TestGapBackoffAndUnfinishedRecentRetryDoNotSpin(t *testing.T) {
	v, _ := vault.New(make([]byte, 32))
	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"), v)
	if err != nil {
		t.Fatal(err)
	}
	defer db.DB.Close()
	now := time.Now()
	host := &futureGapHost{&retryHost{s: db}, now.Add(time.Minute)}
	worker := New(db, host)
	worker.now = func() time.Time { return now }
	worker.nextFallback = now.Add(idleInterval)
	worker.nextDiscovery = now.Add(idleInterval)
	a := store.Archive{Conversation: transport.Conversation{ID: "chat", ParticipantID: "peer", Writable: true}, State: "available_pages_exhausted", RecentStarted: now.Format(time.RFC3339Nano), RecentRetryAt: now.Add(2 * time.Minute).Format(time.RFC3339Nano), Generation: 1}
	if err := db.SaveArchive(a, 0); err != nil {
		t.Fatal(err)
	}
	if d := worker.nextInterval(); d != time.Minute {
		t.Fatalf("backoff accelerated to %v", d)
	}
	worker.Tick(context.Background())
	if len(host.calls) != 0 {
		t.Fatal("read before retry deadline")
	}
}

func TestContinuousOwnerImportDoesNotStarveDueFallback(t *testing.T) {
	v, _ := vault.New(make([]byte, 32))
	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"), v)
	if err != nil {
		t.Fatal(err)
	}
	defer db.DB.Close()
	h := &replayHost{retryHost: &retryHost{s: db}}
	now := time.Now()
	w := newService(db, h, func() time.Time { return now })
	for _, a := range []store.Archive{
		{Conversation: transport.Conversation{ID: "a-import", ParticipantID: "peer", Writable: true}, State: "running", Cursor: "older", RequestedAt: now.Format(time.RFC3339Nano), Generation: 1},
		{Conversation: transport.Conversation{ID: "b-complete", ParticipantID: "peer", Writable: true}, State: "available_pages_exhausted", Generation: 1},
	} {
		if err := db.SaveArchive(a, 0); err != nil {
			t.Fatal(err)
		}
	}
	w.Tick(context.Background())
	now = w.nextFallback
	w.Tick(context.Background())
	now = w.nextFallback
	w.Tick(context.Background())
	seen := false
	for _, c := range h.calls {
		if c == "b-complete:" {
			seen = true
		}
	}
	if !seen {
		t.Fatal("continuous owner import suppressed due routine coverage")
	}
}
