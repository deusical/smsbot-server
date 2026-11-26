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

type videoLookupTransport struct {
	*cachedConversationTransport
	failChatLive  atomic.Bool
	failRemaining atomic.Int32
	failAtCall    atomic.Int32
	chatCalls     atomic.Int32
	uploads       atomic.Int32
	onTransient   func()
	uploadFn      func(context.Context, []media.File) ([]media.File, error)
}

func (v *videoLookupTransport) LookupConversation(ctx context.Context, id string) (transport.Conversation, error) {
	if id == "demo-chat" {
		call := v.chatCalls.Add(1)
		if target := v.failAtCall.Load(); target > 0 && call >= target {
			v.liveLookups.Add(1)
			return transport.Conversation{}, transport.ErrLookupFailed
		}
		if rem := v.failRemaining.Load(); rem > 0 && v.failRemaining.CompareAndSwap(rem, rem-1) {
			v.liveLookups.Add(1)
			if v.onTransient != nil {
				v.onTransient()
			}
			return transport.Conversation{}, transport.ErrLookupFailed
		}
		if v.failChatLive.Load() {
			v.liveLookups.Add(1)
			return transport.Conversation{}, transport.ErrLookupFailed
		}
	}
	return v.cachedConversationTransport.LookupConversation(ctx, id)
}

func (v *videoLookupTransport) PrepareFiles(ctx context.Context, files []media.File) ([]media.File, error) {
	v.uploads.Add(1)
	if v.uploadFn != nil {
		return v.uploadFn(ctx, files)
	}
	return v.Fixture.PrepareFiles(ctx, files)
}

func jobRecord(t *testing.T, e *Engine, id string) store.Job {
	t.Helper()
	jobs, err := e.Store.Jobs()
	if err != nil {
		t.Fatal(err)
	}
	for _, j := range jobs {
		if j.ID == id {
			return j
		}
	}
	t.Fatalf("job %s not found", id)
	return store.Job{}
}

func setupVideoLookupTest(t *testing.T) (*Engine, *transport.Fixture, *videoLookupTransport, *multiHoldTasks, func(time.Duration)) {
	t.Helper()
	e, f := setup(t)
	arm(t, e)
	tr := &videoLookupTransport{cachedConversationTransport: &cachedConversationTransport{Fixture: f, cache: map[string]transport.Conversation{}}}
	useTransport(e, tr)
	cfg := DefaultConfiguration()
	cfg.Commands = []Command{{Name: "download", Type: "download", Enabled: true}}
	if err := e.SetConfiguration(cfg); err != nil {
		t.Fatal(err)
	}
	tasks := &multiHoldTasks{holdsRemaining: 1}
	e.Tasks = tasks
	var clockMu sync.Mutex
	now := time.Now()
	e.now = func() time.Time { clockMu.Lock(); defer clockMu.Unlock(); return now }
	advance := func(d time.Duration) { clockMu.Lock(); now = now.Add(d); clockMu.Unlock() }
	t.Cleanup(func() { _ = e.Pause(true); e.downloadWG.Wait() })
	return e, f, tr, tasks, advance
}

func TestVideoTransientLookupFailuresRetryWithoutLosingWork(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("healthy_sendable_video_avoids_duplicate_pre_upload_lookup", func(t *testing.T) {
		e, f, tr, tasks, _ := setupVideoLookupTest(t)
		m := incoming()
		m.Text = "!download https://v.douyin.com/synthetic/"
		m.At = e.now()
		if ok, err := e.Accept(ctx, m); !ok || err != nil {
			t.Fatal("accept", ok, err)
		}
		id := store.Hash(m.ConversationID + "\x00" + m.ID)
		if err := e.Dispatch(ctx); err != nil || tasks.polls != 1 {
			t.Fatalf("first poll: err=%v polls=%d", err, tasks.polls)
		}
		if _, err := e.Store.DB.Exec("UPDATE job_work SET next_poll=0"); err != nil {
			t.Fatal(err)
		}
		before := tr.liveLookups.Load()
		if err := e.Dispatch(ctx); err != nil {
			t.Fatal(err)
		}
		if got := tr.liveLookups.Load() - before; got != 1 {
			t.Fatalf("live lookups across sendableVideo+prepareDownloadUpload = %d, want 1", got)
		}
		finishDownloadUploads(t, e)
		if stateOf(t, e, id) != "submitted" || len(f.Sent) != 1 || tr.uploads.Load() != 1 {
			t.Fatalf("state=%s sent=%d uploads=%d", stateOf(t, e, id), len(f.Sent), tr.uploads.Load())
		}
	})

	t.Run("sendable_video_and_pre_upload_fail_then_recover", func(t *testing.T) {
		e, f, tr, tasks, advance := setupVideoLookupTest(t)
		m := incoming()
		m.Text = "!download https://v.douyin.com/synthetic/"
		m.At = e.now()
		if ok, err := e.Accept(ctx, m); !ok || err != nil {
			t.Fatal("accept", ok, err)
		}
		id := store.Hash(m.ConversationID + "\x00" + m.ID)
		if err := e.Dispatch(ctx); err != nil || tasks.polls != 1 {
			t.Fatalf("first poll: err=%v polls=%d", err, tasks.polls)
		}
		if _, err := e.Store.DB.Exec("UPDATE job_work SET next_poll=0"); err != nil {
			t.Fatal(err)
		}

		// Fail live lookups when compress finishes: sendableVideo saves the step
		// output and prepareDownloadUpload gates the upload with backoff.
		tr.failChatLive.Store(true)
		err := e.Dispatch(ctx)
		if !errors.Is(err, transport.ErrLookupFailed) {
			t.Fatalf("dispatch err = %v, want ErrLookupFailed", err)
		}
		if got := stateOf(t, e, id); got != "queued" {
			t.Fatalf("state = %s, want queued", got)
		}
		if tr.uploads.Load() != 0 || len(e.downloadUploads) != 0 || len(f.Sent) != 0 {
			t.Fatalf("upload or send started while lookup failing: uploads=%d active=%d sent=%d", tr.uploads.Load(), len(e.downloadUploads), len(f.Sent))
		}
		_, savedPlan, err := e.Store.JobPlan(id)
		if err != nil || len(savedPlan.Attachments) != 1 || pollsWorker(savedPlan) || savedPlan.FilesUploaded {
			t.Fatalf("plan not saved coherently: err=%v attachments=%d pollsWorker=%t uploaded=%t", err, len(savedPlan.Attachments), pollsWorker(savedPlan), savedPlan.FilesUploaded)
		}

		// Backoff stops hot-looping before 1s elapses.
		beforeBackoff := tr.liveLookups.Load()
		for range 10 {
			if derr := e.Dispatch(ctx); derr != nil {
				t.Fatalf("expected quiet backoff, got %v", derr)
			}
		}
		if tr.liveLookups.Load() != beforeBackoff || tr.uploads.Load() != 0 {
			t.Fatalf("hot-looped during backoff: lookups=%d->%d uploads=%d", beforeBackoff, tr.liveLookups.Load(), tr.uploads.Load())
		}

		// Still failing after 1s: still no upload, job stays queued.
		advance(time.Second)
		if derr := e.Dispatch(ctx); !errors.Is(derr, transport.ErrLookupFailed) || tr.uploads.Load() != 0 || stateOf(t, e, id) != "queued" {
			t.Fatalf("retry while still failing: err=%v uploads=%d state=%s", derr, tr.uploads.Load(), stateOf(t, e, id))
		}

		// Recover lookup: upload starts without re-running worker compression, then sends.
		tr.failChatLive.Store(false)
		advance(5 * time.Second)
		if err := e.Dispatch(ctx); err != nil {
			t.Fatal(err)
		}
		finishDownloadUploads(t, e)
		if tasks.polls != 2 || tr.uploads.Load() != 1 {
			t.Fatalf("worker re-polled or upload missing: polls=%d uploads=%d", tasks.polls, tr.uploads.Load())
		}
		if stateOf(t, e, id) != "submitted" || len(f.Sent) != 1 {
			t.Fatalf("did not recover to submitted: state=%s sent=%d", stateOf(t, e, id), len(f.Sent))
		}
	})

	t.Run("sendable_video_single_blip_recovers_at_pre_upload", func(t *testing.T) {
		e, f, tr, tasks, _ := setupVideoLookupTest(t)
		m := incoming()
		m.Text = "!download https://v.douyin.com/synthetic/"
		m.At = e.now()
		if ok, err := e.Accept(ctx, m); !ok || err != nil {
			t.Fatal("accept", ok, err)
		}
		id := store.Hash(m.ConversationID + "\x00" + m.ID)
		if err := e.Dispatch(ctx); err != nil || tasks.polls != 1 {
			t.Fatalf("first poll: err=%v polls=%d", err, tasks.polls)
		}
		if _, err := e.Store.DB.Exec("UPDATE job_work SET next_poll=0"); err != nil {
			t.Fatal(err)
		}
		// Fail only sendableVideo's single lookup; prepareDownloadUpload's live check passes.
		tr.failRemaining.Store(1)
		if err := e.Dispatch(ctx); err != nil {
			t.Fatalf("single blip failed dispatch: %v", err)
		}
		finishDownloadUploads(t, e)
		if stateOf(t, e, id) != "submitted" || len(f.Sent) != 1 || tr.uploads.Load() != 1 {
			t.Fatalf("state=%s sent=%d uploads=%d", stateOf(t, e, id), len(f.Sent), tr.uploads.Load())
		}
	})

	t.Run("pre_upload_check_after_waiting_upload_backs_off_then_recovers", func(t *testing.T) {
		e, f, tr, _, advance := setupVideoLookupTest(t)
		if err := e.Store.Enroll("demo-sms", "demo-sms-peer"); err != nil {
			t.Fatal(err)
		}
		e.Tasks = &scriptTasks{fn: func(context.Context, string, int64) ([]byte, error) { return []byte("synthetic-video"), nil }}
		enteredFirst, releaseFirst := make(chan struct{}), make(chan struct{})
		var smsUploads, chatUploads atomic.Int32
		tr.uploadFn = func(ctx context.Context, files []media.File) ([]media.File, error) {
			if smsUploads.Add(1) == 1 {
				close(enteredFirst)
				<-releaseFirst
				return f.PrepareFiles(ctx, files)
			}
			chatUploads.Add(1)
			return f.PrepareFiles(ctx, files)
		}

		// Job 1 in demo-sms occupies the single upload lane.
		m1 := transport.Incoming{ID: "dl-sms", ConversationID: "demo-sms", ParticipantID: "demo-sms-peer", Text: "!download https://v.douyin.com/synthetic/", At: e.now()}
		if ok, err := e.Accept(ctx, m1); !ok || err != nil {
			t.Fatal("accept m1", ok, err)
		}
		if err := e.Dispatch(ctx); err != nil {
			t.Fatal(err)
		}
		<-enteredFirst

		// Job 2 in demo-chat finishes sendableVideo cleanly and defers as waiting_upload.
		m2 := incoming()
		m2.ID, m2.Text, m2.At = "dl-chat", "!download https://v.douyin.com/synthetic/", e.now()
		if ok, err := e.Accept(ctx, m2); !ok || err != nil {
			t.Fatal("accept m2", ok, err)
		}
		id2 := store.Hash(m2.ConversationID + "\x00" + m2.ID)
		if err := e.Dispatch(ctx); err != nil {
			t.Fatal(err)
		}
		if stateOf(t, e, id2) != "queued" || chatUploads.Load() != 0 {
			t.Fatalf("m2 not waiting_upload: state=%s chatUploads=%d", stateOf(t, e, id2), chatUploads.Load())
		}

		// Finish Job 1 so the upload lane is free.
		close(releaseFirst)
		finishDownloadUploads(t, e)
		if len(f.Sent) != 1 {
			t.Fatalf("m1 not sent: %d", len(f.Sent))
		}
		if _, err := e.Store.DB.Exec("UPDATE job_work SET next_poll=0"); err != nil {
			t.Fatal(err)
		}

		// On Job 2's next Dispatch, let nextReady pass its live lookup and fail
		// specifically at prepareDownloadUpload's live check (and subsequent checks).
		tr.failAtCall.Store(tr.chatCalls.Load() + 2)
		err := e.Dispatch(ctx)
		if !errors.Is(err, transport.ErrLookupFailed) {
			t.Fatalf("pre-upload check err = %v, want ErrLookupFailed", err)
		}
		if stateOf(t, e, id2) != "queued" || chatUploads.Load() != 0 || len(e.downloadUploads) != 0 {
			t.Fatalf("upload started despite failing pre-upload check: state=%s chatUploads=%d active=%d", stateOf(t, e, id2), chatUploads.Load(), len(e.downloadUploads))
		}

		// Backoff blocks immediate retry without new lookups.
		callsBefore := tr.chatCalls.Load()
		if derr := e.Dispatch(ctx); derr != nil || tr.chatCalls.Load() != callsBefore {
			t.Fatalf("pre-upload backoff failed: err=%v calls=%d->%d", derr, callsBefore, tr.chatCalls.Load())
		}

		// Recover lookup and advance past backoff: Job 2 uploads and sends.
		tr.failAtCall.Store(0)
		advance(2 * time.Second)
		if err := e.Dispatch(ctx); err != nil {
			t.Fatal(err)
		}
		finishDownloadUploads(t, e)
		if stateOf(t, e, id2) != "submitted" || chatUploads.Load() != 1 || len(f.Sent) != 2 {
			t.Fatalf("m2 did not recover: state=%s chatUploads=%d sent=%d", stateOf(t, e, id2), chatUploads.Load(), len(f.Sent))
		}
	})

	t.Run("post_upload_collect_fails_then_recovers_without_reuploading", func(t *testing.T) {
		e, f, tr, tasks, advance := setupVideoLookupTest(t)
		tasks.holdsRemaining = 0
		entered, release := make(chan struct{}), make(chan struct{})
		tr.uploadFn = func(ctx context.Context, files []media.File) ([]media.File, error) {
			close(entered)
			<-release
			return f.PrepareFiles(ctx, files)
		}
		m := incoming()
		m.Text = "!download https://v.douyin.com/synthetic/"
		m.At = e.now()
		if ok, err := e.Accept(ctx, m); !ok || err != nil {
			t.Fatal("accept", ok, err)
		}
		id := store.Hash(m.ConversationID + "\x00" + m.ID)
		if err := e.Dispatch(ctx); err != nil {
			t.Fatal(err)
		}
		<-entered
		tr.failChatLive.Store(true)
		close(release)

		// Wait until the upload goroutine writes to its buffered result channel.
		e.downloadWG.Wait()

		err := e.Dispatch(ctx)
		if !errors.Is(err, transport.ErrLookupFailed) {
			t.Fatalf("collect err = %v, want ErrLookupFailed", err)
		}
		if stateOf(t, e, id) != "queued" || len(f.Sent) != 0 || tr.uploads.Load() != 1 {
			t.Fatalf("post-upload collect lost or sent job: state=%s sent=%d uploads=%d", stateOf(t, e, id), len(f.Sent), tr.uploads.Load())
		}
		_, savedPlan, err := e.Store.JobPlan(id)
		if err != nil || !savedPlan.FilesUploaded || len(savedPlan.Attachments) != 1 || pollsWorker(savedPlan) {
			t.Fatalf("uploaded files not preserved on plan: err=%v uploaded=%t attachments=%d pollsWorker=%t", err, savedPlan.FilesUploaded, len(savedPlan.Attachments), pollsWorker(savedPlan))
		}

		// Backoff stops hot-looping before 1s.
		beforeBackoff := tr.liveLookups.Load()
		for range 10 {
			if derr := e.Dispatch(ctx); derr != nil {
				t.Fatalf("expected quiet backoff, got %v", derr)
			}
		}
		if tr.liveLookups.Load() != beforeBackoff || len(f.Sent) != 0 {
			t.Fatalf("hot-looped after collect: lookups=%d->%d sent=%d", beforeBackoff, tr.liveLookups.Load(), len(f.Sent))
		}

		// Recover lookup: next Dispatch checks live in nextReady and submits without re-uploading.
		tr.failChatLive.Store(false)
		advance(2 * time.Second)
		if err := e.Dispatch(ctx); err != nil {
			t.Fatal(err)
		}
		if stateOf(t, e, id) != "submitted" || len(f.Sent) != 1 || tr.uploads.Load() != 1 {
			t.Fatalf("did not submit without re-upload: state=%s sent=%d uploads=%d", stateOf(t, e, id), len(f.Sent), tr.uploads.Load())
		}
	})

	t.Run("worker_pipeline_without_attachment_backs_off_then_recovers", func(t *testing.T) {
		e, f, tr, _, advance := setupVideoLookupTest(t)
		cfg := DefaultConfiguration()
		cfg.Commands = []Command{customCommand("fetchonly", Pipeline{Steps: []PipelineStep{
			step("link", "text.extract_url", map[string]string{"text": "trigger.args"}, ""),
			step("source", "video.download", map[string]string{"url": "link.url"}, ""),
			step("done", "text.template", nil, `{"template":"archived original"}`),
			reply(map[string]string{"text": "done.text"}),
		}})}
		if err := e.SetConfiguration(cfg); err != nil {
			t.Fatal(err)
		}
		m := incoming()
		m.Text = "!fetchonly https://v.douyin.com/synthetic/"
		m.At = e.now()
		if ok, err := e.Accept(ctx, m); !ok || err != nil {
			t.Fatal("accept", ok, err)
		}
		id := store.Hash(m.ConversationID + "\x00" + m.ID)

		tr.failChatLive.Store(true)
		err := e.Dispatch(ctx)
		if !errors.Is(err, transport.ErrLookupFailed) {
			t.Fatalf("dispatch err = %v, want ErrLookupFailed", err)
		}
		if stateOf(t, e, id) != "queued" || len(f.Sent) != 0 {
			t.Fatalf("job ended or sent early: state=%s sent=%d", stateOf(t, e, id), len(f.Sent))
		}
		before := tr.liveLookups.Load()
		if derr := e.Dispatch(ctx); derr != nil || tr.liveLookups.Load() != before {
			t.Fatalf("expected backoff, got err=%v lookups=%d->%d", derr, before, tr.liveLookups.Load())
		}

		tr.failChatLive.Store(false)
		advance(2 * time.Second)
		if err := e.Dispatch(ctx); err != nil {
			t.Fatal(err)
		}
		if stateOf(t, e, id) != "submitted" || len(f.Sent) != 1 {
			t.Fatalf("state=%s sent=%d", stateOf(t, e, id), len(f.Sent))
		}
	})
}

func TestVideoSafetyCodesAndDeadlinesStillCancelOrExpire(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	for _, mode := range []string{
		"identity_changed_at_pre_upload_check",
		"deadline_expired_at_pre_upload_check",
		"identity_changed_after_pre_upload_blip",
		"unenrolled_after_pre_upload_blip",
		"paused_after_pre_upload_blip",
		"deadline_expired_after_pre_upload_blip",
		"identity_changed_at_collect",
		"identity_changed_after_collect_blip",
		"deadline_expired_after_collect_blip",
	} {
		t.Run(mode, func(t *testing.T) {
			e, f, tr, tasks, advance := setupVideoLookupTest(t)
			tasks.holdsRemaining = 0
			directPreUpload := mode == "identity_changed_at_pre_upload_check" || mode == "deadline_expired_at_pre_upload_check"
			afterCollect := mode == "identity_changed_at_collect" || mode == "identity_changed_after_collect_blip" || mode == "deadline_expired_after_collect_blip"
			entered, release := make(chan struct{}), make(chan struct{})
			if afterCollect {
				tr.uploadFn = func(ctx context.Context, files []media.File) ([]media.File, error) {
					close(entered)
					<-release
					return f.PrepareFiles(ctx, files)
				}
			}

			m := incoming()
			m.Text = "!download https://v.douyin.com/synthetic/"
			m.At = e.now()
			if ok, err := e.Accept(ctx, m); !ok || err != nil {
				t.Fatal("accept", ok, err)
			}
			id := store.Hash(m.ConversationID + "\x00" + m.ID)

			if directPreUpload {
				tr.onTransient = func() {
					if mode == "identity_changed_at_pre_upload_check" {
						tr.mu.Lock()
						tr.livePeer = "changed-peer"
						tr.mu.Unlock()
					} else {
						advance(16 * time.Minute)
					}
				}
				tr.failRemaining.Store(1)
				if err := e.Dispatch(ctx); err != nil {
					t.Fatal(err)
				}
			} else if !afterCollect {
				tr.failChatLive.Store(true)
				if err := e.Dispatch(ctx); !errors.Is(err, transport.ErrLookupFailed) {
					t.Fatalf("expected ErrLookupFailed, got %v", err)
				}
				if tr.uploads.Load() != 0 {
					t.Fatalf("upload started during failed lookup: %d", tr.uploads.Load())
				}
				tr.failChatLive.Store(false)
				switch mode {
				case "identity_changed_after_pre_upload_blip":
					tr.mu.Lock()
					tr.livePeer = "changed-peer"
					tr.mu.Unlock()
				case "unenrolled_after_pre_upload_blip":
					if err := e.Unenroll("demo-chat"); err != nil {
						t.Fatal(err)
					}
				case "paused_after_pre_upload_blip":
					if err := e.Pause(true); err != nil {
						t.Fatal(err)
					}
				case "deadline_expired_after_pre_upload_blip":
					advance(16 * time.Minute)
				}
				advance(2 * time.Second)
				if err := e.Dispatch(ctx); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := e.Dispatch(ctx); err != nil {
					t.Fatal(err)
				}
				<-entered
				if mode == "identity_changed_at_collect" {
					tr.mu.Lock()
					tr.livePeer = "changed-peer"
					tr.mu.Unlock()
					close(release)
					e.downloadWG.Wait()
					if err := e.Dispatch(ctx); err != nil {
						t.Fatal(err)
					}
				} else {
					tr.failChatLive.Store(true)
					close(release)
					e.downloadWG.Wait()
					if err := e.Dispatch(ctx); !errors.Is(err, transport.ErrLookupFailed) {
						t.Fatalf("expected ErrLookupFailed on collect, got %v", err)
					}
					tr.failChatLive.Store(false)
					if mode == "identity_changed_after_collect_blip" {
						tr.mu.Lock()
						tr.livePeer = "changed-peer"
						tr.mu.Unlock()
					} else {
						advance(16 * time.Minute)
					}
					advance(2 * time.Second)
					if err := e.Dispatch(ctx); err != nil {
						t.Fatal(err)
					}
				}
			}

			j := jobRecord(t, e, id)
			wantState, wantCode := "cancelled", ""
			switch mode {
			case "identity_changed_at_pre_upload_check", "identity_changed_after_pre_upload_blip", "identity_changed_at_collect", "identity_changed_after_collect_blip":
				wantCode = "identity_changed"
			case "unenrolled_after_pre_upload_blip":
				wantCode = "unenrolled"
			case "paused_after_pre_upload_blip":
				wantCode = "paused"
			case "deadline_expired_at_pre_upload_check", "deadline_expired_after_pre_upload_blip", "deadline_expired_after_collect_blip":
				wantState, wantCode = "expired", "execution_expired"
			}
			if j.State != wantState || j.Code != wantCode || len(f.Sent) != 0 {
				t.Fatalf("got state=%s code=%s sent=%d, want state=%s code=%s sent=0", j.State, j.Code, len(f.Sent), wantState, wantCode)
			}
			if !afterCollect && tr.uploads.Load() != 0 {
				t.Fatalf("upload ran despite %s: uploads=%d", mode, tr.uploads.Load())
			}
		})
	}
}

type sessionRolloverTransport struct {
	*transport.Fixture
	readyFn   func() bool
	sessionFn func() bool
	lookupFn  func(context.Context, string) (transport.Conversation, error)
}

func (s *sessionRolloverTransport) Ready() bool {
	if s.readyFn != nil {
		return s.readyFn()
	}
	return s.Fixture.Ready()
}

func (s *sessionRolloverTransport) SessionUp() bool {
	if s.sessionFn != nil {
		return s.sessionFn()
	}
	return true
}

func (s *sessionRolloverTransport) LookupConversation(ctx context.Context, id string) (transport.Conversation, error) {
	if s.lookupFn != nil {
		return s.lookupFn(ctx, id)
	}
	return fixtureConversation(ctx, id)
}

func TestStreamRolloverGapBacksOffWithoutCancellingUnsentWork(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("check_rollover_gap_retries_and_never_sends_while_unready", func(t *testing.T) {
		t.Parallel()
		e, f := setup(t)
		arm(t, e)
		if err := e.Store.Enroll("demo-sms", "demo-sms-peer"); err != nil {
			t.Fatal(err)
		}
		now := time.Now()
		e.now = func() time.Time { return now }

		m1 := incoming()
		m1.ID, m1.At = "rollover-1", now
		if ok, err := e.Accept(ctx, m1); !ok || err != nil {
			t.Fatal("accept m1", ok, err)
		}
		id1 := store.Hash(m1.ConversationID + "\x00" + m1.ID)

		m2 := transport.Incoming{ID: "rollover-2", ConversationID: m1.ConversationID, ParticipantID: m1.ParticipantID, Text: "!ping", At: now}
		if ok, err := e.Accept(ctx, m2); !ok || err != nil {
			t.Fatal("accept m2", ok, err)
		}
		id2 := store.Hash(m2.ConversationID + "\x00" + m2.ID)

		// Simulate the stream rolling over right after nextReady's initial Ready()
		// check, so Engine.check observes !Ready() while SessionUp() is still true.
		var readyCalls atomic.Int32
		var streamConnected atomic.Bool
		tr := &sessionRolloverTransport{
			Fixture: f,
			readyFn: func() bool {
				if streamConnected.Load() {
					return true
				}
				return readyCalls.Add(1) == 1
			},
			sessionFn: func() bool { return true },
		}
		useTransport(e, tr)

		err := e.Dispatch(ctx)
		if !errors.Is(err, transport.ErrLookupFailed) || errors.Is(err, transport.ErrUnavailable) {
			t.Fatalf("check rollover gap returned %v, want ErrLookupFailed", err)
		}
		if stateOf(t, e, id1) != "queued" || stateOf(t, e, id2) != "queued" || len(f.Sent) != 0 {
			t.Fatalf("check rollover gap cancelled or sent jobs: j1=%s j2=%s sent=%d", stateOf(t, e, id1), stateOf(t, e, id2), len(f.Sent))
		}
		// Immediate retry during backoff is skipped cleanly.
		readyCalls.Store(0)
		if err := e.Dispatch(ctx); err != nil || len(f.Sent) != 0 {
			t.Fatalf("preflight backoff did not hold: err=%v sent=%d", err, len(f.Sent))
		}

		// Once stream reconnects and backoff expires, both jobs send.
		streamConnected.Store(true)
		f.Online = true
		now = now.Add(2 * time.Second)
		if err := e.Dispatch(ctx); err != nil {
			t.Fatal(err)
		}
		if err := e.Dispatch(ctx); err != nil {
			t.Fatal(err)
		}
		if stateOf(t, e, id1) != "submitted" || stateOf(t, e, id2) != "submitted" || len(f.Sent) != 2 {
			t.Fatalf("jobs did not send after rollover ended: j1=%s j2=%s sent=%d", stateOf(t, e, id1), stateOf(t, e, id2), len(f.Sent))
		}
	})

	t.Run("lookup_error_during_rollover_gap_maps_to_lookup_failed", func(t *testing.T) {
		t.Parallel()
		e, f := setup(t)
		arm(t, e)
		now := time.Now()
		e.now = func() time.Time { return now }

		m := incoming()
		m.ID, m.At = "lookup-rollover-1", now
		if ok, err := e.Accept(ctx, m); !ok || err != nil {
			t.Fatal("accept", ok, err)
		}
		id := store.Hash(m.ConversationID + "\x00" + m.ID)

		var streamConnected atomic.Bool
		streamConnected.Store(true)
		var sessionUp atomic.Bool
		sessionUp.Store(true)
		tr := &sessionRolloverTransport{
			Fixture:   f,
			readyFn:   func() bool { return streamConnected.Load() },
			sessionFn: func() bool { return sessionUp.Load() },
			lookupFn: func(context.Context, string) (transport.Conversation, error) {
				// Stream drops briefly mid-lookup while session stays up.
				streamConnected.Store(false)
				return transport.Conversation{}, transport.ErrLookupFailed
			},
		}
		useTransport(e, tr)

		err := e.Dispatch(ctx)
		if !errors.Is(err, transport.ErrLookupFailed) || errors.Is(err, transport.ErrUnavailable) {
			t.Fatalf("dispatch lookup during rollover returned %v, want ErrLookupFailed", err)
		}
		if stateOf(t, e, id) != "queued" || len(f.Sent) != 0 {
			t.Fatalf("job state after rollover lookup = %s, sent=%d", stateOf(t, e, id), len(f.Sent))
		}

		// Ingest lookup failing mid-rollover also maps to ErrLookupFailed, not ErrUnavailable.
		streamConnected.Store(true)
		m2 := incoming()
		m2.ID, m2.At = "lookup-rollover-ingest", now
		_, ingestErr := e.Ingest(ctx, m2)
		if !errors.Is(ingestErr, transport.ErrLookupFailed) || errors.Is(ingestErr, transport.ErrUnavailable) {
			t.Fatalf("ingest lookup during rollover returned %v, want ErrLookupFailed", ingestErr)
		}

		// When the session itself drops, the same lookup error maps to ErrUnavailable.
		now = now.Add(5 * time.Second)
		streamConnected.Store(true)
		tr.lookupFn = func(context.Context, string) (transport.Conversation, error) {
			streamConnected.Store(false)
			sessionUp.Store(false)
			return transport.Conversation{}, transport.ErrUnavailable
		}
		if err := e.Dispatch(ctx); !errors.Is(err, transport.ErrUnavailable) {
			t.Fatalf("dispatch lookup with session down returned %v, want ErrUnavailable", err)
		}
	})

	t.Run("check_with_session_down_cancels_as_transport_unavailable", func(t *testing.T) {
		t.Parallel()
		e, f := setup(t)
		arm(t, e)
		m := incoming()
		if ok, err := e.Accept(ctx, m); !ok || err != nil {
			t.Fatal("accept", ok, err)
		}
		id := store.Hash(m.ConversationID + "\x00" + m.ID)

		var calls atomic.Int32
		var sessionUp atomic.Bool
		sessionUp.Store(true)
		tr := &sessionRolloverTransport{
			Fixture: f,
			readyFn: func() bool {
				if calls.Add(1) == 1 {
					return true
				}
				sessionUp.Store(false)
				return false
			},
			sessionFn: func() bool { return sessionUp.Load() },
		}
		useTransport(e, tr)
		if err := e.Dispatch(ctx); err != nil {
			t.Fatalf("check with session down returned %v, want nil after cancelling job", err)
		}
		jobs, err := e.Store.Jobs()
		if err != nil || len(jobs) != 1 || jobs[0].ID != id || jobs[0].State != "cancelled" || jobs[0].Code != "transport_unavailable" {
			t.Fatalf("expected cancelled/transport_unavailable, got %+v %v", jobs, err)
		}
	})
}

func TestLookupIDMismatchCancelsAsIdentityChangedWithoutRetry(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e, f := setup(t)
	arm(t, e)
	m := incoming()
	if ok, err := e.Accept(ctx, m); !ok || err != nil {
		t.Fatal("accept", ok, err)
	}
	id := store.Hash(m.ConversationID + "\x00" + m.ID)

	var lookups atomic.Int32
	useTransport(e, &lookupTransport{
		Fixture: f,
		lookup: func(context.Context, string) (transport.Conversation, error) {
			lookups.Add(1)
			return transport.Conversation{}, transport.ErrIneligibleConversation
		},
	})

	if err := e.Dispatch(ctx); err != nil {
		t.Fatalf("ineligible lookup returned %v, want nil", err)
	}
	if lookups.Load() != 1 {
		t.Fatalf("lookups = %d, want 1", lookups.Load())
	}
	jobs, err := e.Store.Jobs()
	if err != nil || len(jobs) != 1 || jobs[0].ID != id || jobs[0].State != "cancelled" || jobs[0].Code != "identity_changed" || len(f.Sent) != 0 {
		t.Fatalf("expected cancelled/identity_changed, got %+v sent=%d err=%v", jobs, len(f.Sent), err)
	}

	// Incoming commands in an ID-mismatched conversation are declined cleanly without error or pause.
	m2 := incoming()
	m2.ID = "ineligible-incoming"
	accepted, err := e.Ingest(ctx, m2)
	if err != nil || accepted {
		t.Fatalf("ingest on ineligible conversation: accepted=%v err=%v", accepted, err)
	}
	if paused, err := e.Store.Paused(); err != nil || paused {
		t.Fatalf("ineligible conversation paused engine: paused=%v err=%v", paused, err)
	}
}
