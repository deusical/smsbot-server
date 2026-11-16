package app

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/deusical/smsbot-server/internal/store"
	"github.com/deusical/smsbot-server/internal/transport"
	"github.com/deusical/smsbot-server/internal/vault"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func openStore(t *testing.T) *store.Store {
	t.Helper()
	v, _ := vault.New(make([]byte, 32))
	s, err := store.Open(filepath.Join(t.TempDir(), "state.db"), v)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.DB.Close() })
	return s
}

func assemble(t *testing.T, s *store.Store) *Service {
	return New(s, transport.NewFixture(), filepath.Join(filepath.Dir(s.Path), "archive"))
}

// Opted-in history is marked for preservation before the legacy expiry pass;
// without collection the same old message expires.
func TestInitPreservesOptedInHistoryBeforeExpiry(t *testing.T) {
	t.Parallel()
	for _, enabled := range []bool{true, false} {
		s := openStore(t)
		old := time.Now().AddDate(0, 0, -30)
		if err := s.SaveObserved("kept-chat", "old-message", "", "incoming", "Synthetic old text", "phone", "", old); err != nil {
			t.Fatal(err)
		}
		settings, _ := json.Marshal(map[string]any{"enabled": enabled, "scope": "all", "conversations": []any{}})
		if err := s.SetSecretSetting("inbox_settings", "inbox-settings", settings); err != nil {
			t.Fatal(err)
		}
		if err := assemble(t, s).Init(); err != nil {
			t.Fatal(err)
		}
		var n int
		if err := s.DB.QueryRow("SELECT count(*) FROM messages WHERE conversation='kept-chat'").Scan(&n); err != nil {
			t.Fatal(err)
		}
		if (n == 1) != enabled {
			t.Fatalf("collection=%v kept=%d", enabled, n)
		}
	}
}

func TestPrepareStorePausesEveryStart(t *testing.T) {
	t.Parallel()
	s := openStore(t)
	if err := s.Pause(false); err != nil {
		t.Fatal(err)
	}
	if err := PrepareStore(s); err != nil {
		t.Fatal(err)
	}
	if paused, err := s.Paused(); err != nil || !paused {
		t.Fatal("start did not pause automation", err)
	}
}

type heldEvents struct {
	started, stopped chan struct{}
}

func (h *heldEvents) Run(ctx context.Context, _ func(transport.Incoming), _ func(), _ func(string, string, string, string), _ ...func() error) {
	close(h.started)
	<-ctx.Done()
	close(h.stopped)
}

// Stop returns only after every worker, including the event loop, has exited.
func TestStopWaitsForEveryWorker(t *testing.T) {
	t.Parallel()
	s := openStore(t)
	svc := assemble(t, s)
	if err := svc.Init(); err != nil {
		t.Fatal(err)
	}
	events := &heldEvents{started: make(chan struct{}), stopped: make(chan struct{})}
	workers := svc.Start(context.Background(), events, nil)
	select {
	case <-events.started:
	case <-time.After(5 * time.Second):
		t.Fatal("event loop did not start")
	}
	done := make(chan struct{})
	go func() { workers.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("workers did not stop")
	}
	select {
	case <-events.stopped:
	default:
		t.Fatal("Stop returned before the event loop exited")
	}
}

// The fixture speed-up shortens every background loop, and a factor of one
// leaves the production intervals in place.
func TestAccelerateOnlyShortensWhenAsked(t *testing.T) {
	t.Parallel()
	a := assemble(t, openStore(t))
	notifyInterval := a.Notify.Interval
	a.Accelerate(1)
	if a.History.ActiveInterval != 0 || a.Archive.Interval != 0 || a.Collection.CaptureInterval != 0 || a.Collection.GapRecheck != 0 || a.Engine.WorkerPollDelay != 0 || a.Notify.Interval != notifyInterval {
		t.Fatal("factor 1 changed intervals")
	}
	a.Accelerate(10)
	if a.History.ActiveInterval != 300*time.Millisecond || a.Archive.Interval != 300*time.Millisecond || a.Collection.CaptureInterval != 100*time.Millisecond || a.Collection.GapRecheck != 300*time.Millisecond || a.Notify.Interval != notifyInterval/10 || a.Engine.WorkerPollDelay != 100*time.Millisecond {
		t.Fatal("factor 10 not applied")
	}
}

type transientLookupFixture struct {
	*transport.Fixture
	fail atomic.Bool
}

func (f *transientLookupFixture) LookupConversation(ctx context.Context, id string) (transport.Conversation, error) {
	if f.fail.Load() {
		return transport.Conversation{}, transport.ErrLookupFailed
	}
	return transport.Lookup(ctx, f.Fixture, id)
}

func TestReadyLookupFailureDoesNotCancelJobsOrPauseService(t *testing.T) {
	t.Parallel()
	s := openStore(t)
	tr := &transientLookupFixture{Fixture: transport.NewFixture()}
	svc := New(s, tr, filepath.Join(filepath.Dir(s.Path), "archive"))
	if err := svc.Init(); err != nil {
		t.Fatal(err)
	}
	if err := s.Enroll("demo-chat", "demo-peer"); err != nil {
		t.Fatal(err)
	}
	if err := svc.Engine.Pause(false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Enqueue("job-1", "demo-chat", "!ping", store.Plan{Participant: "demo-peer", Reply: "pong"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Enqueue("job-2", "demo-chat", "!ping", store.Plan{Participant: "demo-peer", Reply: "pong-2"}); err != nil {
		t.Fatal(err)
	}
	tr.fail.Store(true)
	var failed atomic.Bool
	workers := svc.Start(context.Background(), nil, func() { failed.Store(true) })
	defer workers.Stop()

	time.Sleep(350 * time.Millisecond)
	if failed.Load() {
		t.Fatal("ready lookup failure triggered dispatchFailed")
	}
	if paused, err := s.Paused(); err != nil || paused {
		t.Fatalf("ready lookup failure paused service: paused=%t err=%v", paused, err)
	}
	jobs, err := s.Jobs()
	if err != nil || len(jobs) != 2 || jobs[0].State != "queued" || jobs[1].State != "queued" {
		t.Fatalf("ready lookup failure cancelled queued jobs: %+v %v", jobs, err)
	}
}

type rolloverFixture struct {
	*transport.Fixture
	sessionUp         atomic.Bool
	streamUp          atomic.Bool
	dropOnUse         atomic.Bool
	watchDispatch     atomic.Bool
	rolloverAttempted chan struct{}
	postRolloverTick  chan struct{}
	dropAttempted     chan struct{}
}

func (f *rolloverFixture) Ready() bool {
	up := f.sessionUp.Load() && f.streamUp.Load()
	if !up && f.watchDispatch.Load() {
		select {
		case f.postRolloverTick <- struct{}{}:
		default:
		}
	}
	return up
}

func (f *rolloverFixture) SessionUp() bool {
	return f.sessionUp.Load()
}

func (f *rolloverFixture) LookupConversation(_ context.Context, _ string) (transport.Conversation, error) {
	// Flip streamUp false during the lookup to simulate a rollover gap; when
	// dropOnUse is set, simulate the session itself going down.
	f.streamUp.Store(false)
	if f.dropOnUse.Load() {
		f.sessionUp.Store(false)
		select {
		case f.dropAttempted <- struct{}{}:
		default:
		}
		return transport.Conversation{}, transport.ErrUnavailable
	}
	if f.watchDispatch.Load() {
		select {
		case f.rolloverAttempted <- struct{}{}:
		default:
		}
	}
	return transport.Conversation{}, errors.New("synthetic rollover lookup failure")
}

func TestStreamRolloverBlipPreservesUnsentJobsUntilRealSessionDrop(t *testing.T) {
	t.Parallel()
	s := openStore(t)
	tr := &rolloverFixture{
		Fixture:           transport.NewFixture(),
		rolloverAttempted: make(chan struct{}, 1),
		postRolloverTick:  make(chan struct{}, 1),
		dropAttempted:     make(chan struct{}, 1),
	}
	tr.sessionUp.Store(true)
	tr.streamUp.Store(true)
	svc := New(s, tr, filepath.Join(filepath.Dir(s.Path), "archive"))
	if err := svc.Init(); err != nil {
		t.Fatal(err)
	}
	if err := s.Enroll("demo-chat", "demo-peer"); err != nil {
		t.Fatal(err)
	}
	if err := svc.Engine.Pause(false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Enqueue("job-1", "demo-chat", "!ping", store.Plan{Participant: "demo-peer", Reply: "pong"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Enqueue("job-2", "demo-chat", "!ping", store.Plan{Participant: "demo-peer", Reply: "pong-2"}); err != nil {
		t.Fatal(err)
	}

	// An ingest lookup failing mid-rollover (Ready false, SessionUp true) must not
	// cancel unsent jobs or pause automation.
	svc.ingest(context.Background())(transport.Incoming{
		ID: "ingest-rollover", ConversationID: "demo-chat", ParticipantID: "demo-peer", Text: "!ping", At: time.Now(),
	})
	if paused, err := s.Paused(); err != nil || paused {
		t.Fatalf("rollover ingest paused service: paused=%t err=%v", paused, err)
	}
	jobs, err := s.Jobs()
	if err != nil || len(jobs) != 2 || jobs[0].State != "queued" || jobs[1].State != "queued" {
		t.Fatalf("rollover ingest cancelled queued jobs: %+v %v", jobs, err)
	}

	// Dispatch lookup failing mid-rollover also preserves queued jobs.
	tr.streamUp.Store(true)
	tr.watchDispatch.Store(true)
	var failed atomic.Bool
	workers := svc.Start(context.Background(), nil, func() { failed.Store(true) })
	defer workers.Stop()

	select {
	case <-tr.rolloverAttempted:
	case <-time.After(10 * time.Second):
		t.Fatal("dispatch did not attempt rollover lookup")
	}
	select {
	case <-tr.postRolloverTick:
	case <-time.After(10 * time.Second):
		t.Fatal("dispatch loop did not advance past rollover tick")
	}
	if failed.Load() {
		t.Fatal("rollover lookup failure triggered dispatchFailed")
	}
	if paused, err := s.Paused(); err != nil || paused {
		t.Fatalf("rollover lookup failure paused service: paused=%t err=%v", paused, err)
	}
	jobs, err = s.Jobs()
	if err != nil || len(jobs) != 2 || jobs[0].State != "queued" || jobs[1].State != "queued" {
		t.Fatalf("rollover lookup failure cancelled queued jobs: %+v %v", jobs, err)
	}

	// A real session drop (SessionUp false) yields ErrUnavailable and cancels unsent work
	// without flipping Live/Pause.
	tr.watchDispatch.Store(false)
	tr.dropOnUse.Store(true)
	tr.streamUp.Store(true)
	select {
	case <-tr.dropAttempted:
	case <-time.After(15 * time.Second):
		t.Fatal("dispatch did not retry after backoff for session drop")
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		jobs, err = s.Jobs()
		if err == nil && len(jobs) == 2 && jobs[0].State == "cancelled" && jobs[1].State == "cancelled" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(jobs) != 2 || jobs[0].State != "cancelled" || jobs[0].Code != "connection_interrupted" || jobs[1].State != "cancelled" || jobs[1].Code != "connection_interrupted" {
		t.Fatalf("real session drop did not cancel unsent jobs: %+v", jobs)
	}
	if failed.Load() {
		t.Fatal("connection drop triggered dispatchFailed instead of ConnectionInterrupted")
	}
	if paused, err := s.Paused(); err != nil || paused {
		t.Fatalf("connection drop changed Live/Pause state: paused=%t err=%v", paused, err)
	}
}
