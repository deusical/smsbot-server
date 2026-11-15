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
func (h *deadlineHost) Commit(ctx context.Context, c Commit) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if c.Page != nil {
		h.committed = true
	}
	_, err := h.s.UpdateArchive(c.Conversation.ID, 0, c.ApplyTo)
	return err
}
func TestFetchDeadlineDoesNotConsumePersistenceBudget(t *testing.T) {
	t.Parallel()
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
	err   error
}
type repairRetryHost struct {
	*retryHost
	repairs int
}

func (h *repairRetryHost) RepairCaptureGap(context.Context) error { h.repairs++; return nil }
func TestFailedViewedSectionDoesNotStarveImportsAndRepairs(t *testing.T) {
	t.Parallel()
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
		if h.err != nil {
			return transport.HistoryPage{}, h.err
		}
		return transport.HistoryPage{}, errors.New("synthetic unavailable")
	}
	return transport.HistoryPage{}, nil
}
func (h *retryHost) Commit(_ context.Context, c Commit) error {
	_, err := h.s.UpdateArchive(c.Conversation.ID, 0, c.ApplyTo)
	return err
}
func TestIndependentRecentAndBackfillRetries(t *testing.T) {
	t.Parallel()
	v, _ := vault.New(make([]byte, 32))
	s, err := store.Open(filepath.Join(t.TempDir(), "state.db"), v)
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	// When the transport itself is unready, recentStep stops the turn before backfill.
	h := &retryHost{s: s, fail: map[string]bool{"": true, "old": true}, err: transport.ErrUnavailable}
	worker := New(s, h)
	a := store.Archive{Conversation: transport.Conversation{ID: "chat", ParticipantID: "peer", Writable: true}, State: "running", Cursor: "old", Generation: 1}
	if err = s.SaveArchive(a, 0); err != nil {
		t.Fatal(err)
	}
	worker.Tick(context.Background())
	if len(h.calls) != 1 || h.calls[0] != "" {
		t.Fatalf("unready recent failure triggered backfill: %v", h.calls)
	}
	a, _ = s.Archive("chat")
	if a.RecentFailures != 1 || a.Failures != 0 || a.RecentRetryAt == "" {
		t.Fatalf("recent failure state: %+v", a)
	}
	h.err = nil
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
	t.Parallel()
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
	t.Parallel()
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

func TestIneligibleConversationEndsWithoutShortRetryOrActiveInterval(t *testing.T) {
	t.Parallel()
	v, _ := vault.New(make([]byte, 32))
	s, err := store.Open(filepath.Join(t.TempDir(), "state.db"), v)
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	now := time.Now().UTC()
	h := &retryHost{s: s, fail: map[string]bool{"": true, "recent-mid": true, "import-mid": true, "view-mid": true}, err: transport.ErrIneligibleConversation}
	worker := newService(s, h, func() time.Time { return now })
	worker.nextDiscovery = now.Add(idleInterval)
	worker.nextFallback = now

	a := store.Archive{
		Conversation: transport.Conversation{ID: "stale-chat", ParticipantID: "old-phone-peer", Writable: true},
		Generation:   1,
		ImportCheckpoint: store.ImportCheckpoint{
			State: "running", Cursor: "import-mid", Failures: 2,
		},
		RecentCheckpoint: store.RecentCheckpoint{
			RecentStarted:  now.Add(-time.Minute).Format(time.RFC3339Nano),
			RecentCursor:   "recent-mid",
			RecentFailures: 3,
		},
		ViewCheckpoint: store.ViewCheckpoint{
			RefreshState:    "running",
			RefreshCursor:   "view-mid",
			RefreshSince:    now.Add(-time.Hour).Format(time.RFC3339Nano),
			RefreshFailures: 2,
			ViewGeneration:  1,
		},
	}
	if err := s.SaveArchive(a, 0); err != nil {
		t.Fatal(err)
	}

	worker.Tick(context.Background())
	got, err := s.Archive("stale-chat")
	if err != nil {
		t.Fatal(err)
	}
	if got.Conversation.ParticipantID != "old-phone-peer" {
		t.Fatalf("stored participant changed to %q", got.Conversation.ParticipantID)
	}
	if got.RecentStarted != "" || got.RecentCursor != "" || got.RecentRetryAt != "" || got.RecentFailures != 0 || got.RecentError != "ineligible" {
		t.Fatalf("recent checkpoint still scheduled retry: %+v", got.RecentCheckpoint)
	}
	if got.State != "ineligible" || got.Cursor != "" || got.RetryAt != "" || got.Failures != 0 || got.Error != "ineligible" {
		t.Fatalf("import checkpoint still scheduled retry: %+v", got.ImportCheckpoint)
	}
	if got.RefreshState != "ineligible" || got.RefreshCursor != "" || got.RefreshRetryAt != "" || got.RefreshFailures != 0 || got.RefreshError != "ineligible" {
		t.Fatalf("view checkpoint still scheduled retry: %+v", got.ViewCheckpoint)
	}
	if d := worker.nextInterval(); d != idleInterval {
		t.Fatalf("ineligible conversation drove interval %v, want %v", d, idleInterval)
	}

	// Advancing past activeInterval and past the old 320s cap must not trigger any retry.
	callsBefore := len(h.calls)
	now = now.Add(activeInterval)
	worker.Tick(context.Background())
	now = now.Add(6 * time.Minute)
	worker.Tick(context.Background())
	if len(h.calls) != callsBefore {
		t.Fatalf("ineligible conversation retried before slow fallback sweep: %v", h.calls[callsBefore:])
	}

	// Only the slow 15-minute fallback rotation rechecks recent once.
	now = worker.nextFallback
	worker.nextDiscovery = now.Add(idleInterval)
	worker.Tick(context.Background())
	if len(h.calls) != callsBefore+1 || h.calls[len(h.calls)-1] != "" {
		t.Fatalf("expected one fallback check on idle cadence, got %v", h.calls[callsBefore:])
	}
}

func TestRealFetchFailureBacksOffBeyond320SecondsToDailyCap(t *testing.T) {
	t.Parallel()
	v, _ := vault.New(make([]byte, 32))
	s, err := store.Open(filepath.Join(t.TempDir(), "state.db"), v)
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	now := time.Now().UTC()
	h := &retryHost{s: s, fail: map[string]bool{"": true}, err: transport.ErrLookupFailed}
	worker := newService(s, h, func() time.Time { return now })
	worker.nextDiscovery = now.Add(48 * time.Hour)
	worker.nextFallback = now

	a := store.Archive{
		Conversation: transport.Conversation{ID: "chat", ParticipantID: "peer", Writable: true},
		State:        "available_pages_exhausted",
		Generation:   1,
	}
	if err := s.SaveArchive(a, 0); err != nil {
		t.Fatal(err)
	}

	var prevDelay time.Duration
	for attempt := 1; attempt <= 16; attempt++ {
		worker.nextDiscovery = now.Add(48 * time.Hour)
		worker.Tick(context.Background())
		got, err := s.Archive("chat")
		if err != nil {
			t.Fatal(err)
		}
		if got.RecentFailures != attempt {
			t.Fatalf("attempt %d: RecentFailures=%d", attempt, got.RecentFailures)
		}
		retryAt := parseTime(got.RecentRetryAt)
		delay := retryAt.Sub(now)
		if attempt == 7 && delay <= 320*time.Second {
			t.Fatalf("attempt 7 delay %v was still capped at 320s", delay)
		}
		if delay < prevDelay {
			t.Fatalf("attempt %d delay %v shrank from %v", attempt, delay, prevDelay)
		}
		if delay > 24*time.Hour {
			t.Fatalf("attempt %d delay %v exceeded 24h cap", attempt, delay)
		}
		prevDelay = delay
		now = retryAt
	}
	if prevDelay < 23*time.Hour {
		t.Fatalf("sustained failures only reached %v backoff, want ~24h", prevDelay)
	}
}

type perConversationFailHost struct {
	*retryHost
	recentErr error
}

func (h *perConversationFailHost) Fetch(_ context.Context, c transport.Conversation, cursor string) (transport.HistoryPage, error) {
	h.calls = append(h.calls, c.ID+":"+cursor)
	if c.ID == "a-recent" {
		return transport.HistoryPage{}, h.recentErr
	}
	return transport.HistoryPage{NextCursor: "older-2"}, nil
}

func TestRecentFailureDoesNotStarveImportWork(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"lookup_failure", transport.ErrLookupFailed},
		{"ineligible", transport.ErrIneligibleConversation},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			v, _ := vault.New(make([]byte, 32))
			s, err := store.Open(filepath.Join(t.TempDir(), "state.db"), v)
			if err != nil {
				t.Fatal(err)
			}
			defer s.DB.Close()
			now := time.Now().UTC()
			h := &perConversationFailHost{retryHost: &retryHost{s: s}, recentErr: tc.err}
			worker := newService(s, h, func() time.Time { return now })
			worker.nextDiscovery = now.Add(idleInterval)
			worker.nextFallback = now

			for _, a := range []store.Archive{
				{Conversation: transport.Conversation{ID: "a-recent", ParticipantID: "peer-a", Writable: true}, State: "available_pages_exhausted", Generation: 1},
				{Conversation: transport.Conversation{ID: "b-import", ParticipantID: "peer-b", Writable: true}, State: "running", Cursor: "older-1", RecentRetryAt: now.Add(time.Hour).Format(time.RFC3339Nano), Generation: 1},
			} {
				if err := s.SaveArchive(a, 0); err != nil {
					t.Fatal(err)
				}
			}

			worker.Tick(context.Background())
			b, err := s.Archive("b-import")
			if err != nil {
				t.Fatal(err)
			}
			if len(h.calls) != 2 || b.Pages != 1 || b.Cursor != "older-2" {
				t.Fatalf("recent error (%v) starved background import: calls=%v archive=%+v", tc.err, h.calls, b.ImportCheckpoint)
			}
		})
	}
}

type countingPolicyHost struct {
	*retryHost
	snapshots int
	policies  int
	deny      string
}

func (h *countingPolicyHost) PolicySnapshot() (func(transport.Conversation) (Policy, error), error) {
	h.snapshots++
	return func(c transport.Conversation) (Policy, error) {
		if c.ID == h.deny {
			return Policy{}, errors.New("archive_access_disabled")
		}
		return Policy{Revision: 1}, nil
	}, nil
}

func (h *countingPolicyHost) Policy(c transport.Conversation) (Policy, error) {
	h.policies++
	if c.ID == h.deny {
		return Policy{}, errors.New("archive_access_disabled")
	}
	return Policy{Revision: 1}, nil
}

func TestSettingsReadOncePerTick(t *testing.T) {
	t.Parallel()
	v, _ := vault.New(make([]byte, 32))
	s, err := store.Open(filepath.Join(t.TempDir(), "state.db"), v)
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	now := time.Now().UTC()
	h := &countingPolicyHost{retryHost: &retryHost{s: s, fail: map[string]bool{}}, deny: "chat-denied"}
	worker := newService(s, h, func() time.Time { return now })

	for _, a := range []store.Archive{
		{Conversation: transport.Conversation{ID: "chat-denied", ParticipantID: "p0", Writable: true}, State: "queued", Generation: 1},
		{Conversation: transport.Conversation{ID: "chat-view", ParticipantID: "p1", Writable: true}, State: "available_pages_exhausted", Generation: 1, FocusAt: now.Format(time.RFC3339Nano), ViewCheckpoint: store.ViewCheckpoint{RefreshState: "queued", RefreshSince: now.Add(-time.Hour).Format(time.RFC3339Nano), ViewGeneration: 1}},
		{Conversation: transport.Conversation{ID: "chat-owner", ParticipantID: "p2", Writable: true}, State: "queued", RequestSource: "owner", RequestedAt: now.Format(time.RFC3339Nano), Generation: 1},
		{Conversation: transport.Conversation{ID: "chat-bg", ParticipantID: "p3", Writable: true}, State: "running", Cursor: "older", Generation: 1},
		{Conversation: transport.Conversation{ID: "chat-idle-1", ParticipantID: "p4", Writable: true}, State: "available_pages_exhausted", Generation: 1},
		{Conversation: transport.Conversation{ID: "chat-idle-2", ParticipantID: "p5", Writable: true}, State: "available_pages_exhausted", Generation: 1},
	} {
		if err := s.SaveArchive(a, 0); err != nil {
			t.Fatal(err)
		}
	}

	// First turn includes Discovery, ViewWork, owner ImportWork, RecentWork,
	// background ImportWork and nextInterval scheduling: still one settings read.
	_ = worker.tick(context.Background(), true)
	if h.snapshots != 1 || h.policies != 0 {
		t.Fatalf("turn with discovery read settings %d times (per-conversation Policy %d), want 1 and 0", h.snapshots, h.policies)
	}

	// Subsequent active turn without Discovery also reads settings once.
	h.snapshots = 0
	worker.Tick(context.Background())
	if h.snapshots != 1 || h.policies != 0 {
		t.Fatalf("active tick read settings %d times (per-conversation Policy %d), want 1 and 0", h.snapshots, h.policies)
	}
}
