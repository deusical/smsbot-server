package transport

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.mau.fi/mautrix-gmessages/pkg/libgm"
	"go.mau.fi/mautrix-gmessages/pkg/libgm/events"
	"go.mau.fi/mautrix-gmessages/pkg/libgm/gmproto"
)

func TestIrrelevantStartupBurstDoesNotOverflow(t *testing.T) {
	g := NewGoogle(nil, "")
	g.ready.Store(true)
	for i := 0; i < 1024; i++ {
		g.enqueue(&events.ListenRecovered{})
		g.enqueue(&libgm.WrappedMessage{IsOld: true, Message: &gmproto.Message{}})
	}
	if g.overflow.Load() || !g.ready.Load() || len(g.updates) != 0 {
		t.Fatal("ignored startup events consumed queue capacity or invalidated readiness")
	}
}

func TestOverflowPausesDrainsAndRecoversWithoutReplayingCommands(t *testing.T) {
	g := NewGoogle(nil, "")
	g.updates = make(chan any, 2)
	g.ready.Store(true)
	g.started = time.Now().Add(-time.Hour)
	g.listed = time.Now()
	g.list = []Conversation{{ID: "synthetic-conversation"}}
	g.conversations["synthetic-conversation"] = &gmproto.Conversation{}
	g.settings = &gmproto.Settings{}
	g.enqueue(&libgm.WrappedMessage{Message: &gmproto.Message{
		MessageID: "synthetic-message", Timestamp: time.Now().UnixMicro(),
		MessageStatus: &gmproto.MessageStatus{Status: gmproto.MessageStatusType_INCOMING_COMPLETE},
		MessageInfo: []*gmproto.MessageInfo{{Data: &gmproto.MessageInfo_MessageContent{
			MessageContent: &gmproto.MessageContent{Content: "!ping"},
		}}},
	}})
	g.enqueue(&gmproto.Settings{})
	// The upstream callback must return even when the consumer has not started.
	done := make(chan struct{})
	go func() {
		g.enqueue(&gmproto.Conversation{})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("receive callback blocked on a full queue")
	}
	if !g.overflow.Load() || !g.ready.Load() {
		t.Fatal("queue gap was not isolated from transport health")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var diagnostics []string
	g.Diagnostic = func(d AuthDiagnostic) {
		diagnostics = append(diagnostics, d.Event)
		if d.Event == "event_queue_drained" {
			cancel()
		}
	}
	pauses := 0
	beforeRecovery := time.Now()
	g.Run(ctx, func(Incoming) { t.Fatal("command from the queue gap was replayed") },
		func() { pauses++ }, func(string, string, string, string) {})
	if g.overflow.Load() || !g.ready.Load() || pauses != 1 || g.started.Before(beforeRecovery) {
		t.Fatalf("incorrect gap recovery: overflow=%v ready=%v pauses=%d", g.overflow.Load(), g.ready.Load(), pauses)
	}
	if !g.listed.IsZero() || len(g.list) != 0 || len(g.conversations) != 0 {
		t.Fatal("conversation identities were retained across the queue gap")
	}
	if len(diagnostics) != 2 || diagnostics[0] != "event_queue_overflow" || diagnostics[1] != "event_queue_drained" {
		t.Fatalf("missing gap diagnostics: %v", diagnostics)
	}
}

func TestDroppedFailureCannotBeRecoveredByDrainingQueue(t *testing.T) {
	for _, event := range []any{
		&events.ListenFatalError{Error: errors.New("synthetic")},
		&events.ListenTemporaryError{Error: errors.New("synthetic")},
		&events.PingFailed{Error: errors.New("synthetic")},
		&events.AccountChange{}, &events.GaiaLoggedOut{},
		&events.PhoneNotResponding{}, &events.NoDataReceived{}, &events.HackySetActiveMayFail{},
	} {
		g := NewGoogle(nil, "")
		g.updates = make(chan any, 1)
		g.enqueue(&gmproto.Settings{})
		g.enqueue(event)
		// Startup completion must not override a failure dropped before Run.
		g.ready.Store(true)
		<-g.updates
		if !g.finishOverflowRecovery() || !g.healthFailure.Load() {
			t.Fatalf("lost dropped failure %T during gap recovery", event)
		}
	}
}

func TestAccountSnapshotDoesNotLatchHealthFailure(t *testing.T) {
	g := NewGoogle(nil, "")
	g.enqueue(&events.AccountChange{IsFake: true})
	if g.healthFailure.Load() {
		t.Fatal("startup account snapshot blocked transport health")
	}
}

func TestHistoricalOutgoingReceiptSurvivesFiltering(t *testing.T) {
	g := NewGoogle(nil, "")
	g.enqueue(&libgm.WrappedMessage{IsOld: true, Message: &gmproto.Message{
		TmpID: "synthetic-transaction", ConversationID: "synthetic-conversation",
		MessageStatus: &gmproto.MessageStatus{Status: gmproto.MessageStatusType_OUTGOING_DELIVERED},
	}})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	receipts := 0
	g.Run(ctx, func(Incoming) { t.Fatal("historical receipt routed as a command") }, func() {},
		func(conv, txn, state, reported string) {
			if conv != "synthetic-conversation" || txn != "synthetic-transaction" || state != "delivered" {
				t.Fatal("receipt identity or state changed")
			}
			receipts++
			cancel()
		})
	if receipts != 1 {
		t.Fatal("historical correlated receipt was discarded")
	}
}

func TestOverflowCannotClearUntilQueueIsEmpty(t *testing.T) {
	g := NewGoogle(nil, "")
	g.updates = make(chan any, 1)
	g.enqueue(&gmproto.Settings{})
	g.enqueue(&gmproto.Settings{})
	if g.finishOverflowRecovery() || !g.overflow.Load() {
		t.Fatal("gap cleared with buffered events still pending")
	}
	<-g.updates
	if !g.finishOverflowRecovery() || g.overflow.Load() {
		t.Fatal("gap did not clear after draining")
	}
}
