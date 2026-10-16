package bot

import (
	"context"
	"smsbot-server/internal/store"
	"testing"
	"time"
)

func TestConnectionInterruptedPreservesChoiceAndNeverRetriesUncertain(t *testing.T) {
	for _, paused := range []bool{false, true} {
		e, f := setup(t)
		arm(t, e)
		if err := e.Pause(paused); err != nil {
			t.Fatal(err)
		}
		states := []string{"queued", "generating", "awaiting_approval", "submitting", "unknown_submission", "submitted", "delivered", "read"}
		for _, state := range states {
			if _, err := e.Store.Enqueue("synthetic-"+state, "demo-chat", "ping", store.Plan{Reply: "synthetic", Participant: "demo-peer"}); err != nil {
				t.Fatal(err)
			}
			if _, err := e.Store.DB.Exec("UPDATE jobs SET state=? WHERE id=?", state, "synthetic-"+state); err != nil {
				t.Fatal(err)
			}
		}
		call, cancel := context.WithCancel(context.Background())
		defer cancel()
		e.pending["synthetic-generating"] = cancel
		if err := e.ConnectionInterrupted(); err != nil {
			t.Fatal(err)
		}
		if call.Err() == nil {
			t.Fatal("provider work not cancelled")
		}
		got, err := e.Store.Paused()
		if err != nil || got != paused {
			t.Fatal("owner choice changed")
		}
		for i, state := range states {
			var actual, plan, code string
			if err := e.Store.DB.QueryRow("SELECT state,plan,code FROM jobs WHERE id=?", "synthetic-"+state).Scan(&actual, &plan, &code); err != nil {
				t.Fatal(err)
			}
			if i < 3 {
				if actual != "cancelled" || plan != "" || code != "connection_interrupted" {
					t.Fatal("unsent work survived gap")
				}
			} else if actual != state {
				t.Fatal("submission evidence changed")
			}
		}
		if err := e.Dispatch(context.Background()); err != nil {
			t.Fatal(err)
		}
		if len(f.Sent) != 0 {
			t.Fatal("old or uncertain work replayed")
		}
		if !paused {
			m := incoming()
			m.ID = "synthetic-fresh-after-recovery"
			if ok, err := e.Accept(context.Background(), m); err != nil || !ok {
				t.Fatal("live did not resume for fresh commands")
			}
			if err := e.Dispatch(context.Background()); err != nil {
				t.Fatal(err)
			}
			if len(f.Sent) != 1 {
				t.Fatal("fresh command not submitted")
			}
		}
	}
}
func TestPauseDuringInterruptionStaysPaused(t *testing.T) {
	e, _ := setup(t)
	arm(t, e)
	if err := e.ConnectionInterrupted(); err != nil {
		t.Fatal(err)
	}
	if err := e.Pause(true); err != nil {
		t.Fatal(err)
	}
	if ok, err := e.Accept(context.Background(), incoming()); err != nil || ok {
		t.Fatal("explicit pause was lost")
	}
}

func TestConnectionInterruptionCancelsActiveGeneration(t *testing.T) {
	e, f := setup(t)
	arm(t, e)
	configureAI(t, e, false, false)
	p := &controlledProvider{started: make(chan store.Plan, 1), release: make(chan struct{})}
	e.Provider = p
	id := acceptAsk(t, e)
	done := make(chan error, 1)
	go func() { done <- e.Dispatch(context.Background()) }()
	select {
	case <-p.started:
	case <-time.After(3 * time.Second):
		t.Fatal("generation did not start")
	}
	if err := e.ConnectionInterrupted(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("generation not cancelled")
	}
	paused, err := e.Store.Paused()
	if err != nil || paused {
		t.Fatal("interruption changed Live")
	}
	if stateOf(t, e, id) != "cancelled" || len(f.Sent) != 0 {
		t.Fatal("old generation submitted")
	}
}
func TestConnectionInterruptionBlocksUploadCompletion(t *testing.T) {
	e, fixture := setup(t)
	enableInbox(t, e)
	if err := e.Pause(false); err != nil {
		t.Fatal(err)
	}
	f := &blockedFileTransport{Fixture: fixture, started: make(chan struct{}), release: make(chan struct{})}
	e.Transport = f
	id, err := e.ComposeInboxFiles(context.Background(), "demo-chat", "", "synthetic-interrupted-upload", syntheticUpload())
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- e.Dispatch(context.Background()) }()
	select {
	case <-f.started:
	case <-time.After(2 * time.Second):
		t.Fatal("upload did not start")
	}
	if err = e.ConnectionInterrupted(); err != nil {
		t.Fatal(err)
	}
	close(f.release)
	select {
	case err = <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("upload did not finish")
	}
	paused, err := e.Store.Paused()
	if err != nil || paused {
		t.Fatal("interruption changed Live")
	}
	if stateOf(t, e, id) != "cancelled" || len(f.Sent) != 0 {
		t.Fatal("old upload submitted")
	}
}
