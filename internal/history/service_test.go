package history

import (
	"context"
	"errors"
	"github.com/deusical/smsbot-server/internal/store"
	"github.com/deusical/smsbot-server/internal/transport"
	"github.com/deusical/smsbot-server/internal/vault"
	"path/filepath"
	"testing"
	"time"
)

type deadlineHost struct {
	s         *store.Store
	committed bool
	t         *testing.T
}

func (h *deadlineHost) Prune(context.Context) error { return nil }
func (h *deadlineHost) Discover(context.Context) ([]transport.Conversation, error) {
	return []transport.Conversation{{ID: "chat", ParticipantID: "peer", Writable: true}}, nil
}
func (h *deadlineHost) Policy(transport.Conversation) (Policy, error) { return Policy{}, nil }
func (h *deadlineHost) Fetch(ctx context.Context, c transport.Conversation, cursor string) (transport.HistoryPage, error) {
	if _, ok := ctx.Deadline(); !ok {
		h.t.Error("fetch missing bounded deadline")
	}
	<-ctx.Done()
	// A successful read can race the network deadline; local persistence must
	// have its own deadline rather than inheriting the exhausted read budget.
	return transport.HistoryPage{Messages: []transport.HistoricalMessage{{ID: "msg", Direction: "incoming", Text: "Synthetic fetched text", At: time.Now()}}}, nil
}
func (h *deadlineHost) Commit(ctx context.Context, a store.Archive, p Policy, page transport.HistoryPage, stateOnly bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !stateOnly {
		h.committed = true
	}
	return h.s.SaveArchive(a, 0)
}
func TestFetchDeadlineDoesNotConsumePersistenceBudget(t *testing.T) {
	v, _ := vault.New(make([]byte, 32))
	s, err := store.Open(filepath.Join(t.TempDir(), "state.db"), v)
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	h := &deadlineHost{s: s, t: t}
	worker := New(s, h)
	worker.fetchTimeout = time.Millisecond
	worker.Tick(context.Background())
	if !h.committed {
		t.Fatal("successful response lost at fetch deadline")
	}
}

type retryHost struct {
	s     *store.Store
	calls []string
	fail  map[string]bool
}
type repairRetryHost struct {
	*retryHost
	repairs int
}

func (h *repairRetryHost) RepairCaptureGap(context.Context) error { h.repairs++; return nil }
func TestFailedViewedSectionDoesNotStarveImportsAndRepairs(t *testing.T) {
	v, _ := vault.New(make([]byte, 32))
	s, err := store.Open(filepath.Join(t.TempDir(), "state.db"), v)
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	h := &repairRetryHost{retryHost: &retryHost{s: s, fail: map[string]bool{"view": true}}}
	a := store.Archive{Conversation: transport.Conversation{ID: "chat", ParticipantID: "peer", Writable: true}, State: "running", Cursor: "old", RequestedAt: time.Now().UTC().Format(time.RFC3339Nano), RefreshState: "queued", RefreshCursor: "view", RecentRetryAt: time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano), Generation: 1}
	if err = s.SaveArchive(a, 0); err != nil {
		t.Fatal(err)
	}
	New(s, h).Tick(context.Background())
	if len(h.calls) != 2 || h.calls[0] != "view" || h.calls[1] != "old" || h.repairs != 1 {
		t.Fatal("failed view starved independent history work", h.calls, h.repairs)
	}
}

func (h *retryHost) Prune(context.Context) error { return nil }
func (h *retryHost) Discover(context.Context) ([]transport.Conversation, error) {
	return []transport.Conversation{{ID: "chat", ParticipantID: "peer", Writable: true}}, nil
}
func (h *retryHost) Policy(transport.Conversation) (Policy, error) { return Policy{}, nil }
func (h *retryHost) Fetch(_ context.Context, _ transport.Conversation, cursor string) (transport.HistoryPage, error) {
	h.calls = append(h.calls, cursor)
	if h.fail[cursor] {
		return transport.HistoryPage{}, errors.New("synthetic unavailable")
	}
	return transport.HistoryPage{}, nil
}
func (h *retryHost) Commit(_ context.Context, a store.Archive, _ Policy, _ transport.HistoryPage, _ bool) error {
	return h.s.SaveArchive(a, 0)
}
func TestIndependentRecentAndBackfillRetries(t *testing.T) {
	v, _ := vault.New(make([]byte, 32))
	s, err := store.Open(filepath.Join(t.TempDir(), "state.db"), v)
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	h := &retryHost{s: s, fail: map[string]bool{"": true, "old": true}}
	worker := New(s, h)
	a := store.Archive{Conversation: transport.Conversation{ID: "chat", ParticipantID: "peer", Writable: true}, State: "running", Cursor: "old", Generation: 1}
	if err = s.SaveArchive(a, 0); err != nil {
		t.Fatal(err)
	}
	worker.Tick(context.Background())
	if len(h.calls) != 1 || h.calls[0] != "" {
		t.Fatalf("recent failure immediately triggered backfill: %v", h.calls)
	}
	a, _ = s.Archive("chat")
	if a.RecentFailures != 1 || a.Failures != 0 || a.RecentRetryAt == "" {
		t.Fatalf("recent failure state: %+v", a)
	}
	// While recent reconciliation is backed off, deep work can run independently.
	worker.Tick(context.Background())
	a, _ = s.Archive("chat")
	if len(h.calls) != 2 || h.calls[1] != "old" || a.Failures != 1 || a.RecentFailures != 1 {
		t.Fatalf("independent backfill failure: %v %+v", h.calls, a)
	}
	backfillRetry := a.RetryAt
	// A recent success must not clear or bypass the backfill's retry budget.
	a.RecentRetryAt = time.Now().Add(-time.Second).UTC().Format(time.RFC3339Nano)
	if err = s.SaveArchive(a, 0); err != nil {
		t.Fatal(err)
	}
	h.fail[""] = false
	worker.Tick(context.Background())
	a, _ = s.Archive("chat")
	if len(h.calls) != 3 || h.calls[2] != "" || a.Failures != 1 || a.RetryAt != backfillRetry || a.RecentFailures != 0 || a.RecentRetryAt != "" {
		t.Fatalf("recent success altered deep backoff: %v %+v", h.calls, a)
	}
	// Conversely, deep success must preserve a recent failure and its backoff.
	a.RecentFailures = 2
	a.RecentRetryAt = time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano)
	a.RecentError = "history_fetch_failed"
	a.RetryAt = time.Now().Add(-time.Second).UTC().Format(time.RFC3339Nano)
	if err = s.SaveArchive(a, 0); err != nil {
		t.Fatal(err)
	}
	h.fail["old"] = false
	worker.Tick(context.Background())
	a, _ = s.Archive("chat")
	if len(h.calls) != 4 || h.calls[3] != "old" || a.Failures != 0 || a.RetryAt != "" || a.RecentFailures != 2 || a.RecentError == "" {
		t.Fatalf("backfill success altered recent backoff: %v %+v", h.calls, a)
	}
}

func TestCompletedOlderHistoryIsNotRecheckedAutomatically(t *testing.T) {
	v, _ := vault.New(make([]byte, 32))
	s, err := store.Open(filepath.Join(t.TempDir(), "state.db"), v)
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	h := &retryHost{s: s, fail: map[string]bool{}}
	worker := New(s, h)
	a := store.Archive{Conversation: transport.Conversation{ID: "chat", ParticipantID: "peer", Writable: true}, State: "available_pages_exhausted", Generation: 1, Pages: 20, Checked: 1000, Imported: 50, DeepCheckedAt: time.Now().Add(-25 * time.Hour).UTC().Format(time.RFC3339Nano), RecentRetryAt: time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano)}
	if err := s.SaveArchive(a, 0); err != nil {
		t.Fatal(err)
	}
	worker.Tick(context.Background())
	worker.Tick(context.Background())
	if len(h.calls) != 0 {
		t.Fatalf("completed history should not be revisited automatically, got calls: %v", h.calls)
	}
	a, _ = s.Archive("chat")
	if a.State != "available_pages_exhausted" || a.Pages != 20 || a.Checked != 1000 || a.Imported != 50 {
		t.Fatalf("completed archive state was reset: %+v", a)
	}

	// Explicit owner request initiates a rescan.
	a.State = "queued"
	a.RequestSource = "owner"
	a.RequestedAt = time.Now().UTC().Format(time.RFC3339Nano)
	a.RetryAt = ""
	if err := s.SaveArchive(a, 0); err != nil {
		t.Fatal(err)
	}
	worker.Tick(context.Background())
	if len(h.calls) != 1 {
		t.Fatalf("explicit owner request did not trigger fetch: %v", h.calls)
	}
}

func TestServiceIdleBackoffAndWake(t *testing.T) {
	v, _ := vault.New(make([]byte, 32))
	s, err := store.Open(filepath.Join(t.TempDir(), "state.db"), v)
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	h := &retryHost{s: s, fail: map[string]bool{}}
	worker := New(s, h)

	// No archives -> idle interval
	if d := worker.nextInterval(); d != idleInterval {
		t.Fatalf("expected idle interval %v, got %v", idleInterval, d)
	}

	// Completed archive -> idle interval
	a := store.Archive{Conversation: transport.Conversation{ID: "chat", ParticipantID: "peer", Writable: true}, State: "available_pages_exhausted", Generation: 1}
	if err := s.SaveArchive(a, 0); err != nil {
		t.Fatal(err)
	}
	if d := worker.nextInterval(); d != idleInterval {
		t.Fatalf("expected idle interval %v for completed archive, got %v", idleInterval, d)
	}

	// Active queued state -> active interval
	a.State = "queued"
	if err := s.SaveArchive(a, 0); err != nil {
		t.Fatal(err)
	}
	if d := worker.nextInterval(); d != activeInterval {
		t.Fatalf("expected active interval %v for queued archive, got %v", activeInterval, d)
	}

	// Active view refresh -> active interval
	a.State = "available_pages_exhausted"
	a.RefreshState = "queued"
	if err := s.SaveArchive(a, 0); err != nil {
		t.Fatal(err)
	}
	if d := worker.nextInterval(); d != activeInterval {
		t.Fatalf("expected active interval %v for view refresh, got %v", activeInterval, d)
	}

	// Ready recent retry -> active interval
	a.RefreshState = ""
	a.RecentRetryAt = time.Now().Add(-time.Second).UTC().Format(time.RFC3339Nano)
	if err := s.SaveArchive(a, 0); err != nil {
		t.Fatal(err)
	}
	if d := worker.nextInterval(); d != activeInterval {
		t.Fatalf("expected active interval %v for ready recent retry, got %v", activeInterval, d)
	}

	// Future recent retry -> returns delta delay
	a.RecentRetryAt = time.Now().Add(30 * time.Second).UTC().Format(time.RFC3339Nano)
	if err := s.SaveArchive(a, 0); err != nil {
		t.Fatal(err)
	}
	if d := worker.nextInterval(); d <= 0 || d > 35*time.Second {
		t.Fatalf("expected bounded delay for future recent retry, got %v", d)
	}

	// Clear recent retry for wake test
	a.RecentRetryAt = ""
	_ = s.SaveArchive(a, 0)

	// Verify Wake() promptly triggers Tick in Run()
	a.RefreshState = ""
	_ = s.SaveArchive(a, 0)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runDone := make(chan struct{})
	go func() {
		defer close(runDone)
		worker.Run(ctx)
	}()

	// Wake signals processing
	worker.Wake()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case <-runDone:
	case <-time.After(2 * time.Second):
		t.Fatal("worker.Run did not stop on context cancellation")
	}
}
