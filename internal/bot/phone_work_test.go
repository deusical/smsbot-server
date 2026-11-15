package bot

import (
	"context"
	"errors"
	"github.com/deusical/smsbot-server/internal/media"
	"github.com/deusical/smsbot-server/internal/store"
	"github.com/deusical/smsbot-server/internal/transport"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type countedCaptureTransport struct {
	*lookupTransport
	epoch atomic.Uint64
}

func (f *countedCaptureTransport) IdentityEpoch() uint64 { return f.epoch.Load() }

func TestCaptureDuplicateAndChangedSnapshots(t *testing.T) {
	t.Parallel()
	e, f := setup(t)
	settings := enableInbox(t, e)
	var calls atomic.Int32
	source := &countedCaptureTransport{lookupTransport: &lookupTransport{Fixture: f, lookup: func(ctx context.Context, id string) (transport.Conversation, error) {
		calls.Add(1)
		return fixtureConversation(ctx, id)
	}}}
	useTransport(e, source)
	m := incoming()
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := e.Collection.Observe(context.Background(), m); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("duplicate capture lookups = %d", calls.Load())
	}
	verify := func() {
		t.Helper()
		before := calls.Load()
		if err := e.Collection.Observe(context.Background(), m); err != nil {
			t.Fatal(err)
		}
		if calls.Load() != before+1 {
			t.Fatal("changed snapshot skipped verification")
		}
	}
	m.Text = "edited"
	verify()
	m.Status = "read"
	verify()
	m.Attachments = []media.File{{Info: media.Info{ID: "synthetic-file", Name: "first.txt"}}}
	verify()
	m.Attachments[0].Name = "changed.txt"
	verify()
	file, err := e.Store.Attachment(m.ConversationID, store.Hash(m.ConversationID+"\x00"+m.ID), m.Attachments[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	updated := file
	updated.Failures++
	if err = e.Store.UpdateArchivedFile(context.Background(), store.Hash(m.ConversationID+"\x00"+m.ID), file, updated); err != nil {
		t.Fatal(err)
	}
	verify()
	m.Interaction = &transport.MessageInteraction{Protocol: "RCS", ObservedAt: time.Now()}
	verify()
	m.Interaction.Reactions = []transport.Reaction{{Emoji: "synthetic", Participants: []string{"demo-peer"}}}
	verify()
	m.Outgoing = true
	m.TransactionID = "synthetic-transaction"
	verify()
	m.Interaction.ObservedAt = m.Interaction.ObservedAt.Add(time.Second)
	verify()
	source.epoch.Add(1)
	verify()
	if err := e.Collection.SetSettings(context.Background(), settings); err != nil {
		t.Fatal(err)
	}
	verify()
	if err := e.Store.SaveObservedFiles("demo-chat", "another", "", "incoming", "local update", "phone", "", time.Now(), nil); err != nil {
		t.Fatal(err)
	}
	verify()
}

func TestCaptureFailedVerificationIsNotRemembered(t *testing.T) {
	t.Parallel()
	e, f := setup(t)
	enableInbox(t, e)
	calls := 0
	useTransport(e, &lookupTransport{Fixture: f, lookup: func(ctx context.Context, id string) (transport.Conversation, error) {
		calls++
		if calls == 1 {
			return transport.Conversation{}, errors.New("synthetic_failure")
		}
		return fixtureConversation(ctx, id)
	}})
	if e.Collection.Observe(context.Background(), incoming()) == nil {
		t.Fatal("failure hidden")
	}
	if err := e.Collection.Observe(context.Background(), incoming()); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatal("failed capture cached")
	}
}

func TestQueuedPreflightBackoffAndFreshness(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"recover", "stale", "pause"} {
		t.Run(mode, func(t *testing.T) {
			e, f := setup(t)
			arm(t, e)
			now := time.Now()
			e.now = func() time.Time { return now }
			if ok, err := e.Accept(context.Background(), incoming()); !ok || err != nil {
				t.Fatal("queue failed", err)
			}
			calls := 0
			fail := true
			useTransport(e, &lookupTransport{Fixture: f, lookup: func(ctx context.Context, id string) (transport.Conversation, error) {
				calls++
				if fail {
					return transport.Conversation{}, errors.New("synthetic_failure")
				}
				return fixtureConversation(ctx, id)
			}})
			if e.Dispatch(context.Background()) == nil {
				t.Fatal("failed preflight hidden")
			}
			for range 20 {
				e.Dispatch(context.Background())
			}
			if calls != 1 {
				t.Fatal("preflight busy loop")
			}
			now = now.Add(time.Second)
			if e.Dispatch(context.Background()) == nil || calls != 2 {
				t.Fatal("due preflight not attempted")
			}
			fail = false
			switch mode {
			case "pause":
				e.Pause(true)
			case "stale":
				now = now.Add(3 * time.Minute)
			default:
				now = now.Add(2 * time.Second)
			}
			if err := e.Dispatch(context.Background()); err != nil {
				t.Fatal(err)
			}
			expected := 0
			if mode == "recover" {
				expected = 1
			}
			if len(f.Sent) != expected {
				t.Fatal("freshness/pause or recovery send failed")
			}
			e.Dispatch(context.Background())
			if len(f.Sent) != expected {
				t.Fatal("duplicate send")
			}
		})
	}
}

type cachedConversationTransport struct {
	*transport.Fixture
	mu            sync.Mutex
	cache         map[string]transport.Conversation
	livePeer      string
	liveLookups   atomic.Int32
	cachedLookups atomic.Int32
}

func (c *cachedConversationTransport) LookupConversation(ctx context.Context, id string) (transport.Conversation, error) {
	c.liveLookups.Add(1)
	list, err := c.Fixture.Conversations(ctx)
	if err != nil {
		return transport.Conversation{}, err
	}
	for _, conv := range list {
		if conv.ID == id {
			c.mu.Lock()
			if c.livePeer != "" && id == "demo-chat" {
				conv.ParticipantID = c.livePeer
			}
			if c.cache == nil {
				c.cache = map[string]transport.Conversation{}
			}
			c.cache[id] = conv
			c.mu.Unlock()
			return conv, nil
		}
	}
	return transport.Conversation{}, errors.New("conversation_unavailable")
}

func (c *cachedConversationTransport) CachedConversation(id string) (transport.Conversation, bool) {
	c.cachedLookups.Add(1)
	c.mu.Lock()
	defer c.mu.Unlock()
	conv, ok := c.cache[id]
	return conv, ok
}

type multiHoldTasks struct {
	holdsRemaining int
	polls          int
	data           []byte
}

func (m *multiHoldTasks) Poll(_ context.Context, _ *TaskState, id, _ string, _ time.Time, req TaskRequest) (TaskStatus, error) {
	switch req.Task {
	case "video.download":
		return TaskStatus{Stage: "complete", Outputs: map[string]TaskValue{"original": {Handle: id + "/original"}}}, nil
	case "video.compress":
		m.polls++
		if m.holdsRemaining > 0 {
			m.holdsRemaining--
			return TaskStatus{Stage: "encoding"}, nil
		}
		m.data = []byte("synthetic-video")
		return TaskStatus{Stage: "complete", Outputs: map[string]TaskValue{"video": {Handle: id + "/video"}}}, nil
	default:
		return TaskStatus{}, taskFailed{"unknown_task"}
	}
}

func (m *multiHoldTasks) File(context.Context, string, int64) ([]byte, string, error) {
	return m.data, "complete", nil
}

func TestDeferredWorkerRedispatchUsesCacheAndChecksLiveBeforeSend(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"success", "sendable_video_live_check", "submit_live_check"} {
		t.Run(mode, func(t *testing.T) {
			e, fixture := setup(t)
			arm(t, e)
			tr := &cachedConversationTransport{Fixture: fixture, cache: map[string]transport.Conversation{}}
			useTransport(e, tr)
			cfg := DefaultConfiguration()
			cfg.Commands = []Command{{Name: "download", Type: "download", Enabled: true}}
			if err := e.SetConfiguration(cfg); err != nil {
				t.Fatal(err)
			}
			tasks := &multiHoldTasks{holdsRemaining: 4}
			e.Tasks = tasks

			m := incoming()
			m.Text = "!download https://v.douyin.com/synthetic/"
			if ok, err := e.Accept(context.Background(), m); !ok || err != nil {
				t.Fatal("accept", ok, err)
			}
			id := store.Hash(m.ConversationID + "\x00" + m.ID)
			if got := tr.liveLookups.Load(); got != 1 {
				t.Fatalf("accept live lookups = %d, want 1", got)
			}

			// Worker preparation dispatches and deferred re-dispatches use the cached
			// conversation without hitting live LookupConversation.
			for i := 0; i < 2; i++ {
				if _, err := e.Store.DB.Exec("UPDATE job_work SET next_poll=0"); err != nil {
					t.Fatal(err)
				}
				if err := e.Dispatch(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
			if got := tr.liveLookups.Load(); got != 1 || tr.cachedLookups.Load() < 2 || tasks.polls != 2 {
				t.Fatalf("worker polls made live lookups: live=%d cached=%d polls=%d", got, tr.cachedLookups.Load(), tasks.polls)
			}

			// With no cached record, worker preparation falls back to a live lookup,
			// then resumes using the cached record on subsequent polls.
			tr.mu.Lock()
			delete(tr.cache, "demo-chat")
			tr.mu.Unlock()
			if _, err := e.Store.DB.Exec("UPDATE job_work SET next_poll=0"); err != nil {
				t.Fatal(err)
			}
			if err := e.Dispatch(context.Background()); err != nil {
				t.Fatal(err)
			}
			if got := tr.liveLookups.Load(); got != 2 {
				t.Fatalf("missing cache did not fall back to live lookup: live=%d", got)
			}
			if _, err := e.Store.DB.Exec("UPDATE job_work SET next_poll=0"); err != nil {
				t.Fatal(err)
			}
			if err := e.Dispatch(context.Background()); err != nil {
				t.Fatal(err)
			}
			if got := tr.liveLookups.Load(); got != 2 || tasks.polls != 4 {
				t.Fatalf("re-cached worker poll made live lookup: live=%d polls=%d", got, tasks.polls)
			}

			// Final worker completion and pre-send checks must check live even when
			// the cached conversation still shows the original peer.
			if mode == "sendable_video_live_check" {
				tr.mu.Lock()
				tr.livePeer = "changed-peer"
				tr.cache["demo-chat"] = transport.Conversation{OutgoingID: "demo-self", ID: "demo-chat", ParticipantID: "demo-peer", Protocol: "RCS", Writable: true}
				tr.mu.Unlock()
			}
			if _, err := e.Store.DB.Exec("UPDATE job_work SET next_poll=0"); err != nil {
				t.Fatal(err)
			}
			beforeFinal := tr.liveLookups.Load()
			if err := e.Dispatch(context.Background()); err != nil {
				t.Fatal(err)
			}
			if tr.liveLookups.Load() <= beforeFinal {
				t.Fatal("sendableVideo after worker completion skipped live lookup")
			}
			if mode == "sendable_video_live_check" {
				if got := stateOf(t, e, id); got != "cancelled" || len(fixture.Sent) != 0 {
					t.Fatalf("stale cache bypassed live check in sendableVideo: state=%s sent=%d", got, len(fixture.Sent))
				}
				return
			}

			// Wait for the background upload result to be collected, then test the
			// final pre-submit check.
			until := time.Now().Add(3 * time.Second)
			for len(e.downloadUploads) > 0 && time.Now().Before(until) {
				time.Sleep(time.Millisecond)
				if err := e.collectDownloadUploads(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
			if len(e.downloadUploads) > 0 {
				t.Fatal("upload did not finish")
			}

			if mode == "submit_live_check" {
				tr.mu.Lock()
				tr.livePeer = "changed-peer"
				tr.cache["demo-chat"] = transport.Conversation{OutgoingID: "demo-self", ID: "demo-chat", ParticipantID: "demo-peer", Protocol: "RCS", Writable: true}
				tr.mu.Unlock()
			}
			beforeSubmit := tr.liveLookups.Load()
			if err := e.Dispatch(context.Background()); err != nil {
				t.Fatal(err)
			}
			if tr.liveLookups.Load() <= beforeSubmit {
				t.Fatal("final pre-send check skipped live lookup")
			}
			if mode == "submit_live_check" {
				if got := stateOf(t, e, id); got != "cancelled" || len(fixture.Sent) != 0 {
					t.Fatalf("stale cache bypassed final pre-send check: state=%s sent=%d", got, len(fixture.Sent))
				}
				return
			}
			if got := stateOf(t, e, id); got != "submitted" || len(fixture.Sent) != 1 {
				t.Fatalf("state=%s sent=%d", got, len(fixture.Sent))
			}
		})
	}
}

func TestLookupErrorWhileReadyBacksOffWithoutCancellingOtherJobs(t *testing.T) {
	t.Parallel()
	e, f := setup(t)
	arm(t, e)
	if err := e.Store.Enroll("demo-sms", "demo-sms-peer"); err != nil {
		t.Fatal(err)
	}
	cfg := DefaultConfiguration()
	cfg.Commands = []Command{{Name: "download", Type: "download", Enabled: true}}
	if err := e.SetConfiguration(cfg); err != nil {
		t.Fatal(err)
	}
	var clockMu sync.Mutex
	now := time.Now()
	e.now = func() time.Time { clockMu.Lock(); defer clockMu.Unlock(); return now }
	advance := func(d time.Duration) { clockMu.Lock(); now = now.Add(d); clockMu.Unlock() }
	tasks := &multiHoldTasks{holdsRemaining: 1}
	e.Tasks = tasks

	var failChat atomic.Bool
	var lookups atomic.Int32
	tr := &lookupTransport{Fixture: f, lookup: func(ctx context.Context, id string) (transport.Conversation, error) {
		lookups.Add(1)
		if id == "demo-chat" && failChat.Load() {
			return transport.Conversation{}, transport.ErrUnavailable
		}
		list, err := f.Conversations(ctx)
		if err != nil {
			return transport.Conversation{}, err
		}
		for _, c := range list {
			if c.ID == id {
				return c, nil
			}
		}
		return transport.Conversation{}, errors.New("conversation_unavailable")
	}}
	useTransport(e, tr)

	// Start a deferred video job in demo-sms, plus a queued ping in demo-chat.
	dl := transport.Incoming{ID: "video-1", ConversationID: "demo-sms", ParticipantID: "demo-sms-peer", Text: "!download https://v.douyin.com/synthetic/", At: now}
	if ok, err := e.Accept(context.Background(), dl); !ok || err != nil {
		t.Fatal("accept download", ok, err)
	}
	dlID := store.Hash(dl.ConversationID + "\x00" + dl.ID)
	if err := e.Dispatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := stateOf(t, e, dlID); got != "queued" || tasks.polls != 1 {
		t.Fatalf("download not deferred: state=%s polls=%d", got, tasks.polls)
	}

	ping := incoming()
	ping.At = now
	if ok, err := e.Accept(context.Background(), ping); !ok || err != nil {
		t.Fatal("accept ping", ok, err)
	}
	pingID := store.Hash(ping.ConversationID + "\x00" + ping.ID)

	// A lookup failure while Transport.Ready() is true returns ErrLookupFailed,
	// backs off demo-chat, never reports to Engine.Run, and cancels neither job.
	failChat.Store(true)
	lookups.Store(0)
	err := e.Dispatch(context.Background())
	if !errors.Is(err, transport.ErrLookupFailed) || errors.Is(err, transport.ErrUnavailable) {
		t.Fatalf("dispatch error = %v, want ErrLookupFailed and not ErrUnavailable", err)
	}
	if err := e.Dispatch(context.Background()); err != nil || lookups.Load() != 1 {
		t.Fatalf("preflight did not back off: err=%v lookups=%d", err, lookups.Load())
	}

	advance(2 * time.Second)
	runCtx, cancelRun := context.WithCancel(context.Background())
	reported := make(chan error, 1)
	doneRun := make(chan struct{})
	go func() {
		defer close(doneRun)
		e.Run(runCtx, func(err error) { reported <- err })
	}()
	time.Sleep(300 * time.Millisecond)
	cancelRun()
	<-doneRun
	select {
	case rep := <-reported:
		t.Fatalf("ready lookup blip escalated from Run: %v", rep)
	default:
	}

	if stateOf(t, e, pingID) != "queued" || stateOf(t, e, dlID) != "queued" {
		t.Fatalf("transient lookup cancelled queued jobs: ping=%s dl=%s", stateOf(t, e, pingID), stateOf(t, e, dlID))
	}
	if paused, err := e.Store.Paused(); err != nil || paused {
		t.Fatalf("transient lookup changed pause: paused=%t err=%v", paused, err)
	}

	// Once the lookup recovers and backoff passes, both jobs finish and send.
	failChat.Store(false)
	advance(30 * time.Second)
	if _, err := e.Store.DB.Exec("UPDATE job_work SET next_poll=0"); err != nil {
		t.Fatal(err)
	}
	if err := e.Dispatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	finishDownloadUploads(t, e)
	if err := e.Dispatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if stateOf(t, e, dlID) != "submitted" || stateOf(t, e, pingID) != "submitted" || len(f.Sent) != 2 {
		t.Fatalf("jobs did not recover: dl=%s ping=%s sent=%d", stateOf(t, e, dlID), stateOf(t, e, pingID), len(f.Sent))
	}

	// An actually unready transport still reports ErrUnavailable.
	clockMu.Lock()
	atNow := now
	clockMu.Unlock()
	m2 := incoming()
	m2.ID, m2.At = "unready-msg", atNow
	if ok, err := e.Accept(context.Background(), m2); !ok || err != nil {
		t.Fatal("accept m2", ok, err)
	}
	failChat.Store(true)
	tr.lookup = func(context.Context, string) (transport.Conversation, error) {
		f.SetOutgoingID("demo-self")
		f.Online = false
		return transport.Conversation{}, errors.New("disconnected")
	}
	if err := e.Dispatch(context.Background()); !errors.Is(err, transport.ErrUnavailable) {
		t.Fatalf("unready lookup returned %v, want ErrUnavailable", err)
	}
}

func TestOutgoingIdentityChangedAfterAcceptanceRefusedAtSend(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("bot_command", func(t *testing.T) {
		e, f := setup(t)
		arm(t, e)
		useTransport(e, &lookupTransport{Fixture: f, lookup: func(ctx context.Context, id string) (transport.Conversation, error) {
			list, err := f.Conversations(ctx)
			if err != nil {
				return transport.Conversation{}, err
			}
			for _, c := range list {
				if c.ID == id {
					return c, nil
				}
			}
			return transport.Conversation{}, errors.New("missing")
		}})
		m := incoming()
		if ok, err := e.Accept(ctx, m); !ok || err != nil {
			t.Fatal("accept", ok, err)
		}
		id := store.Hash(m.ConversationID + "\x00" + m.ID)
		_, p, err := e.Store.JobPlan(id)
		if err != nil || p.OutgoingID == "" {
			t.Fatalf("outgoing identity not snapshotted: %+v %v", p, err)
		}
		f.SetOutgoingID("switched-sim")
		if err := e.Dispatch(ctx); err != nil {
			t.Fatal(err)
		}
		jobs, _ := e.Store.Jobs()
		if len(jobs) != 1 || jobs[0].State != "failed" || jobs[0].Code != "not_submitted" || jobs[0].Reason != "outgoing_identity_changed" || len(f.Sent) != 0 {
			t.Fatalf("expected outgoing_identity_changed refusal, got %+v sent=%d", jobs, len(f.Sent))
		}
	})

	t.Run("dashboard_compose", func(t *testing.T) {
		e, f := setup(t)
		enableInbox(t, e)
		if err := e.Pause(false); err != nil {
			t.Fatal(err)
		}
		useTransport(e, &lookupTransport{Fixture: f, lookup: func(ctx context.Context, id string) (transport.Conversation, error) {
			list, err := f.Conversations(ctx)
			if err != nil {
				return transport.Conversation{}, err
			}
			for _, c := range list {
				if c.ID == id {
					return c, nil
				}
			}
			return transport.Conversation{}, errors.New("missing")
		}})
		id, err := e.ComposeInboxTo(ctx, "demo-chat", "", "Synthetic compose", "compose-outgoing-id-0001", nil)
		if err != nil {
			t.Fatal(err)
		}
		_, p, err := e.Store.JobPlan(id)
		if err != nil || p.OutgoingID == "" {
			t.Fatalf("compose outgoing identity not snapshotted: %+v %v", p, err)
		}
		f.SetOutgoingID("switched-sim")
		if err := e.Dispatch(ctx); err != nil {
			t.Fatal(err)
		}
		jobs, _ := e.Store.Jobs()
		if len(jobs) != 1 || jobs[0].State != "failed" || jobs[0].Code != "not_submitted" || jobs[0].Reason != "outgoing_identity_changed" || len(f.Sent) != 0 {
			t.Fatalf("expected compose outgoing_identity_changed refusal, got %+v sent=%d", jobs, len(f.Sent))
		}
	})

	t.Run("reaction", func(t *testing.T) {
		e, f := setup(t)
		enableInbox(t, e)
		if err := e.Pause(false); err != nil {
			t.Fatal(err)
		}
		m := interactionMessage("synthetic-react-outgoing", "RCS")
		if err := e.Collection.Observe(ctx, m); err != nil {
			t.Fatal(err)
		}
		target := store.Hash("demo-chat\x00" + m.ID)
		a, err := e.MessageActions(ctx, "demo-chat", target)
		if err != nil || !a.React {
			t.Fatal("actions unavailable", a, err)
		}
		id, err := e.QueueReactionIn(ctx, "demo-chat", "", target, "👍", "add", a.Version, "reaction-outgoing-0001")
		if err != nil {
			t.Fatal(err)
		}
		if _, p, err := e.Store.JobPlan(id); err != nil || p.OutgoingID == "" {
			t.Fatalf("reaction outgoing identity not snapshotted: %+v %v", p, err)
		}
		f.SetOutgoingID("switched-sim")
		if err := e.Dispatch(ctx); err != nil {
			t.Fatal(err)
		}
		jobs, _ := e.Store.Jobs()
		if len(jobs) != 1 || jobs[0].State != "failed" || jobs[0].Code != "not_submitted" || jobs[0].Reason != "outgoing_identity_changed" || len(f.Sent) != 0 {
			t.Fatalf("expected reaction outgoing_identity_changed refusal, got %+v sent=%d", jobs, len(f.Sent))
		}
	})

	t.Run("legacy_plan_without_snapshot", func(t *testing.T) {
		e, f := setup(t)
		arm(t, e)
		if _, err := e.Store.Enqueue("legacy-job", "demo-chat", "!ping", store.Plan{Participant: "demo-peer", Reply: "pong"}); err != nil {
			t.Fatal(err)
		}
		f.SetOutgoingID("switched-sim")
		if err := e.Dispatch(ctx); err != nil {
			t.Fatal(err)
		}
		if got := stateOf(t, e, "legacy-job"); got != "submitted" || len(f.Sent) != 1 {
			t.Fatalf("legacy plan without snapshot failed: state=%s sent=%d", got, len(f.Sent))
		}
	})
}
