package history

import (
	"context"
	"errors"
	"path/filepath"
	"smsbot-server/internal/store"
	"smsbot-server/internal/transport"
	"smsbot-server/internal/vault"
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
	worker.recent["chat"] = time.Time{}
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
