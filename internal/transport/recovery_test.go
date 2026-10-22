package transport

import (
	"context"
	"errors"
	"go.mau.fi/mautrix-gmessages/pkg/libgm"
	"go.mau.fi/mautrix-gmessages/pkg/libgm/events"
	"go.mau.fi/mautrix-gmessages/pkg/libgm/gmproto"
	"sync/atomic"
	"testing"
	"time"
)

func TestVerifiedRecoverySingleFlightAndFreshness(t *testing.T) {
	g := NewGoogle(nil, "")
	g.ready.Store(true)
	g.started = time.Now().Add(-time.Hour)
	g.conversations["synthetic"] = &gmproto.Conversation{}
	g.settings = &gmproto.Settings{}
	release := make(chan struct{})
	var calls atomic.Int32
	g.recoveryProbe = func(ctx context.Context) error {
		if phonePurpose(ctx) != "readiness" {
			t.Error("recovery probe purpose missing")
		}
		if _, bounded := ctx.Deadline(); !bounded {
			t.Error("recovery probe deadline missing")
		}
		calls.Add(1)
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	g.noteFailure("reconnecting", false)
	results := make(chan recoveryResult, 2)
	now := time.Now().Add(3 * time.Second)
	g.startRecovery(context.Background(), now, results)
	g.startRecovery(context.Background(), now, results)
	close(release)
	result := <-results
	if calls.Load() != 1 {
		t.Fatal("overlapping probes")
	}
	before := time.Now()
	recovered, hard := g.finishRecovery(result, before)
	if !recovered || hard || g.healthFailure.Load() || !g.ready.Load() {
		t.Fatal("verified recovery did not restore health")
	}
	if g.settings == nil {
		t.Fatal("recovery discarded SIM configuration")
	}
	if g.started.Before(before) || len(g.conversations) != 0 {
		t.Fatal("recovery retained old freshness or identity")
	}
	if g.ConnectionStatus().State != "connected" {
		t.Fatal("wrong connection status")
	}
}
func TestStaleRecoveryCannotOverrideNewFailure(t *testing.T) {
	for _, hard := range []bool{false, true} {
		g := NewGoogle(nil, "")
		g.noteFailure("reconnecting", false)
		_, _, epoch := g.failureSnapshot()
		g.noteFailure("login_required", hard)
		recovered, _ := g.finishRecovery(recoveryResult{epoch: epoch}, time.Now())
		if recovered || !g.healthFailure.Load() || g.ready.Load() {
			t.Fatal("stale success restored readiness")
		}
	}
}
func TestRecoveryBackoffAndHardBlockers(t *testing.T) {
	for _, err := range []error{errors.New("synthetic transient"), events.ErrInvalidCredentials, errRecoveryStorage} {
		g := NewGoogle(nil, "")
		g.noteFailure("reconnecting", false)
		_, _, epoch := g.failureSnapshot()
		g.recovery.attempts = 2
		now := time.Now()
		recovered, hard := g.finishRecovery(recoveryResult{epoch, err}, now)
		if recovered {
			t.Fatal("failed probe restored readiness")
		}
		if err == events.ErrInvalidCredentials || err == errRecoveryStorage {
			if !hard {
				t.Fatal("hard error allowed retry")
			}
			g.noteFailure("reconnecting", false)
			g.wakeRecovery(true)
			if !g.recovery.hard {
				t.Fatal("recovery hint cleared blocker")
			}
		} else if hard || g.recovery.next.Before(now.Add(10*time.Second)) || g.recovery.next.After(now.Add(13*time.Second)) {
			t.Fatal("retry backoff missing")
		}
	}
}
func TestRunRecoveryKeepsIntentAndRejectsGapCommands(t *testing.T) {
	g := NewGoogle(nil, "")
	g.ready.Store(true)
	g.started = time.Now().Add(-time.Hour)
	g.recoveryProbe = func(context.Context) error { return nil }
	g.enqueue(&events.PingFailed{Error: errors.New("synthetic")})
	g.enqueue(&libgm.WrappedMessage{Message: &gmproto.Message{
		MessageID: "synthetic-gap", ConversationID: "synthetic-conversation", Timestamp: time.Now().UnixMicro(),
		MessageStatus: &gmproto.MessageStatus{Status: gmproto.MessageStatusType_INCOMING_COMPLETE},
		MessageInfo:   []*gmproto.MessageInfo{{Data: &gmproto.MessageInfo_MessageContent{MessageContent: &gmproto.MessageContent{Content: "!ping"}}}},
	}})
	g.enqueue(&events.ListenRecovered{})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	interruptions, pauses := 0, 0
	g.Diagnostic = func(d AuthDiagnostic) {
		if d.Event == "connection_recovered" {
			cancel()
		}
	}
	g.Run(ctx, func(m Incoming) {
		if !m.Historical {
			t.Fatal("gap command routed")
		}
	}, func() { pauses++ }, func(string, string, string, string) {}, func() error { interruptions++; return nil })
	if ctx.Err() != context.Canceled || pauses != 0 || interruptions != 1 || g.healthFailure.Load() {
		t.Fatalf("recovery lost live intent: pauses=%d interruptions=%d", pauses, interruptions)
	}
}
func TestRecoveryProbeStopsOnShutdown(t *testing.T) {
	g := NewGoogle(nil, "")
	exited := make(chan struct{})
	g.recoveryProbe = func(ctx context.Context) error { <-ctx.Done(); close(exited); return ctx.Err() }
	g.noteFailure("reconnecting", false)
	ctx, cancel := context.WithCancel(context.Background())
	g.startRecovery(ctx, time.Now().Add(3*time.Second), make(chan recoveryResult))
	cancel()
	select {
	case <-exited:
	case <-time.After(time.Second):
		t.Fatal("probe survived shutdown")
	}
}

func TestCredentialFailuresUseCodesAndRemainBlocked(t *testing.T) {
	for _, code := range []int64{7, 16} {
		err := events.RequestError{Data: &gmproto.ErrorResponse{Type: code, Message: "synthetic variant"}}
		if !credentialError(err) {
			t.Fatal("authentication code treated as temporary")
		}
	}
	g := NewGoogle(nil, "")
	g.updates = make(chan any, 1)
	g.enqueue(&gmproto.Settings{})
	g.enqueue(&events.GaiaLoggedOut{}) // failure cannot fit in the event queue
	g.enqueue(&events.ListenRecovered{})
	var calls atomic.Int32
	g.recoveryProbe = func(context.Context) error { calls.Add(1); return nil }
	g.startRecovery(context.Background(), time.Now().Add(time.Hour), make(chan recoveryResult, 1))
	if calls.Load() != 0 || !g.healthFailure.Load() || g.ConnectionStatus().State != "login_required" {
		t.Fatal("dropped logout was overridden")
	}
}

func TestRecoveryFreshnessStartsAfterIdentityInvalidation(t *testing.T) {
	g := NewGoogle(nil, "")
	g.noteFailure("reconnecting", false)
	_, _, epoch := g.failureSnapshot()
	before := time.Now()
	g.listMu.Lock()
	entered, done := make(chan struct{}), make(chan bool, 1)
	go func() { close(entered); ok, _ := g.finishRecovery(recoveryResult{epoch: epoch}, before); done <- ok }()
	<-entered
	released := time.Now()
	g.listMu.Unlock()
	if !<-done || g.started.Before(released) {
		t.Fatal("freshness cutoff preceded recovery completion")
	}
}

func TestNewTransientEventCannotHideProbeStorageFailure(t *testing.T) {
	g := NewGoogle(nil, "")
	g.noteFailure("reconnecting", false)
	_, _, epoch := g.failureSnapshot()
	g.noteFailure("phone_unreachable", false)
	recovered, hard := g.finishRecovery(recoveryResult{epoch: epoch, err: errRecoveryStorage}, time.Now())
	if recovered || !hard || g.ConnectionStatus().State != "storage_error" {
		t.Fatal("stale probe hid session persistence failure")
	}
}
