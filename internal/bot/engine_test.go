package bot

import (
	"context"
	"github.com/deusical/smsbot-server/internal/store"
	"github.com/deusical/smsbot-server/internal/transport"
	"github.com/deusical/smsbot-server/internal/vault"
	"path/filepath"
	"testing"
	"time"
)

func setup(t *testing.T) (*Engine, *transport.Fixture) {
	t.Helper()
	v, e := vault.New(make([]byte, 32))
	if e != nil {
		t.Fatal(e)
	}
	s, e := store.Open(filepath.Join(t.TempDir(), "state.db"), v)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.DB.Close() })
	f := transport.NewFixture()
	engine := New(s, f)
	return engine, f
}
func incoming() transport.Incoming {
	return transport.Incoming{ID: "test-message", ConversationID: "demo-chat", ParticipantID: "demo-peer", Text: "!ping", At: time.Now()}
}
func arm(t *testing.T, e *Engine) {
	t.Helper()
	if err := e.Store.Enroll("demo-chat", "demo-peer"); err != nil {
		t.Fatal(err)
	}
	if err := e.Pause(false); err != nil {
		t.Fatal(err)
	}
}
func TestAccessBoundaries(t *testing.T) {
	tests := []struct {
		name string
		edit func(*transport.Incoming)
	}{
		{"outgoing", func(m *transport.Incoming) { m.Outgoing = true }}, {"historical", func(m *transport.Incoming) { m.Historical = true }},
		{"stale", func(m *transport.Incoming) { m.At = time.Now().Add(-3 * time.Minute) }}, {"future", func(m *transport.Incoming) { m.At = time.Now().Add(time.Minute) }},
		{"wrong_peer", func(m *transport.Incoming) { m.ParticipantID = "other-peer" }}, {"wrong_chat", func(m *transport.Incoming) { m.ConversationID = "other-chat" }},
		{"ordinary_text", func(m *transport.Incoming) { m.Text = "ordinary fixture" }}, {"ping_arguments", func(m *transport.Incoming) { m.Text = "!ping wrong" }},
		{"group", func(m *transport.Incoming) { m.ConversationID = "demo-group"; m.ParticipantID = "demo-peer" }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e, _ := setup(t)
			arm(t, e)
			m := incoming()
			tc.edit(&m)
			ok, err := e.Accept(context.Background(), m)
			if err != nil || ok {
				t.Fatalf("accepted=%t error=%v", ok, err)
			}
			jobs, _ := e.Store.Jobs()
			if len(jobs) != 0 {
				t.Fatal("ignored message persisted")
			}
		})
	}
}
func TestPausedAndUnenrolled(t *testing.T) {
	e, _ := setup(t)
	m := incoming()
	ok, err := e.Accept(context.Background(), m)
	if ok || err != nil {
		t.Fatal("accepted while paused")
	}
	if err = e.Pause(false); err != nil {
		t.Fatal(err)
	}
	ok, err = e.Accept(context.Background(), m)
	if ok || err != nil {
		t.Fatal("accepted unenrolled")
	}
}
func TestDedupAndDispatch(t *testing.T) {
	e, f := setup(t)
	arm(t, e)
	m := incoming()
	for i := 0; i < 2; i++ {
		ok, err := e.Accept(context.Background(), m)
		if err != nil || ok != (i == 0) {
			t.Fatalf("attempt %d: %t %v", i, ok, err)
		}
	}
	if err := e.Dispatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := e.Dispatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(f.Sent) != 1 {
		t.Fatal("duplicate sends")
	}
	jobs, _ := e.Store.Jobs()
	if len(jobs) != 1 || jobs[0].State != "submitted" {
		t.Fatal(jobs)
	}
	messages, _ := e.Store.Messages("demo-chat")
	if len(messages) != 2 {
		t.Fatal("missing retained exchange")
	}
}
func TestPauseCancelsQueued(t *testing.T) {
	e, f := setup(t)
	arm(t, e)
	e.Accept(context.Background(), incoming())
	if err := e.Pause(true); err != nil {
		t.Fatal(err)
	}
	e.Pause(false)
	e.Dispatch(context.Background())
	if len(f.Sent) != 0 {
		t.Fatal("sent paused request")
	}
	jobs, _ := e.Store.Jobs()
	if jobs[0].State != "cancelled" {
		t.Fatal(jobs)
	}
}
func TestUnenrollCancelsQueued(t *testing.T) {
	e, f := setup(t)
	arm(t, e)
	e.Accept(context.Background(), incoming())
	e.Unenroll("demo-chat")
	e.Store.Enroll("demo-chat", "demo-peer")
	e.Dispatch(context.Background())
	if len(f.Sent) != 0 {
		t.Fatal("replayed after reenrollment")
	}
}
func TestUnknownNotRetried(t *testing.T) {
	e, f := setup(t)
	arm(t, e)
	e.Accept(context.Background(), incoming())
	f.Fail = "unknown"
	if err := e.Dispatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.Fail = ""
	e.Dispatch(context.Background())
	jobs, _ := e.Store.Jobs()
	if jobs[0].State != "unknown_submission" || len(f.Sent) != 0 {
		t.Fatal("uncertain send retried")
	}
}
func TestCrashRecovery(t *testing.T) {
	e, f := setup(t)
	arm(t, e)
	e.Accept(context.Background(), incoming())
	jobs, _ := e.Store.Jobs()
	e.Store.Submitting(jobs[0].ID, "fixture-transaction")
	if err := e.Store.Recover(); err != nil {
		t.Fatal(err)
	}
	e.Dispatch(context.Background())
	jobs, _ = e.Store.Jobs()
	if jobs[0].State != "unknown_submission" || len(f.Sent) != 0 {
		t.Fatal("crashed send retried")
	}
}
func TestExpiredQueue(t *testing.T) {
	e, f := setup(t)
	arm(t, e)
	e.Accept(context.Background(), incoming())
	e.now = func() time.Time { return time.Now().Add(3 * time.Minute) }
	e.Dispatch(context.Background())
	jobs, _ := e.Store.Jobs()
	if jobs[0].State != "expired" || len(f.Sent) != 0 {
		t.Fatal("stale request sent")
	}
}
func TestIdentityChange(t *testing.T) {
	e, f := setup(t)
	arm(t, e)
	e.Accept(context.Background(), incoming())
	e.Store.Enroll("demo-chat", "new-peer")
	e.Dispatch(context.Background())
	if len(f.Sent) != 0 {
		t.Fatal("identity change did not stop send")
	}
}

func TestReceiptsMatchTransactionAndNeverDowngrade(t *testing.T) {
	e, _ := setup(t)
	arm(t, e)
	e.Accept(context.Background(), incoming())
	e.Dispatch(context.Background())
	jobs, _ := e.Store.Jobs()
	j := jobs[0]
	e.Reconcile("another-chat", j.TransactionID, "delivered", "fixture-delivered")
	jobs, _ = e.Store.Jobs()
	if jobs[0].State != "submitted" {
		t.Fatal("cross-conversation receipt matched")
	}
	e.Reconcile(j.ConversationID, j.TransactionID, "read", "fixture-displayed")
	e.Reconcile(j.ConversationID, j.TransactionID, "delivered", "fixture-delivered")
	jobs, _ = e.Store.Jobs()
	if jobs[0].State != "read" {
		t.Fatal("receipt downgraded")
	}
}

func TestKnownRejectionIsFailedAndNeverRetried(t *testing.T) {
	e, f := setup(t)
	arm(t, e)
	e.Accept(context.Background(), incoming())
	f.Fail = "reject"
	e.Dispatch(context.Background())
	f.Fail = ""
	e.Dispatch(context.Background())
	jobs, _ := e.Store.Jobs()
	if jobs[0].State != "failed" || len(f.Sent) != 0 {
		t.Fatal("known rejection retried or mislabeled")
	}
}
