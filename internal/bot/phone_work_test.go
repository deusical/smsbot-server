package bot

import (
	"context"
	"errors"
	"smsbot-server/internal/media"
	"smsbot-server/internal/store"
	"smsbot-server/internal/transport"
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

func TestCaptureWaitCancellation(t *testing.T) {
	var reads captureReads
	release, err := reads.acquire(context.Background(), "synthetic")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = reads.acquire(ctx, "synthetic"); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled duplicate blocked")
	}
}

func TestCaptureDuplicateAndChangedSnapshots(t *testing.T) {
	e, f := setup(t)
	settings := enableInbox(t, e)
	var calls atomic.Int32
	source := &countedCaptureTransport{lookupTransport: &lookupTransport{Fixture: f, lookup: func(ctx context.Context, id string) (transport.Conversation, error) {
		calls.Add(1)
		return fixtureConversation(ctx, id)
	}}}
	e.Transport = source
	m := incoming()
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := e.Observe(context.Background(), m); err != nil {
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
		if err := e.Observe(context.Background(), m); err != nil {
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
	if err := e.SetInboxSettings(context.Background(), settings); err != nil {
		t.Fatal(err)
	}
	verify()
	if err := e.Store.SaveObservedFiles("demo-chat", "another", "", "incoming", "local update", "phone", "", time.Now(), nil); err != nil {
		t.Fatal(err)
	}
	verify()
}

func TestCaptureFailedVerificationIsNotRemembered(t *testing.T) {
	e, f := setup(t)
	enableInbox(t, e)
	calls := 0
	e.Transport = &lookupTransport{Fixture: f, lookup: func(ctx context.Context, id string) (transport.Conversation, error) {
		calls++
		if calls == 1 {
			return transport.Conversation{}, errors.New("synthetic_failure")
		}
		return fixtureConversation(ctx, id)
	}}
	if e.Observe(context.Background(), incoming()) == nil {
		t.Fatal("failure hidden")
	}
	if err := e.Observe(context.Background(), incoming()); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatal("failed capture cached")
	}
}

func TestQueuedPreflightBackoffAndFreshness(t *testing.T) {
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
			e.Transport = &lookupTransport{Fixture: f, lookup: func(ctx context.Context, id string) (transport.Conversation, error) {
				calls++
				if fail {
					return transport.Conversation{}, errors.New("synthetic_failure")
				}
				return fixtureConversation(ctx, id)
			}}
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
