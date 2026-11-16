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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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

func TestEventLoopWaitsForRecoveryProbeBeforeSessionReplacement(t *testing.T) {
	t.Parallel()
	g := NewGoogle(nil, "")
	entered, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	g.recoveryProbe = func(ctx context.Context) error {
		close(entered)
		<-ctx.Done()
		close(canceled)
		<-release // model cleanup that finishes after cancellation
		return ctx.Err()
	}
	g.noteFailure("reconnecting", false)
	g.recovery.mu.Lock()
	g.recovery.next = time.Time{}
	g.recovery.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		g.Run(ctx, func(Incoming) {}, func() {}, func(string, string, string, string) {})
		close(done)
	}()
	<-entered
	cancel()
	<-canceled
	select {
	case <-done:
		close(release)
		t.Fatal("event loop left an old session recovery probe running")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("event loop did not finish after probe cleanup")
	}
}

func TestCredentialFailuresUseCodesAndRemainBlocked(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
	g := NewGoogle(nil, "")
	g.noteFailure("reconnecting", false)
	_, _, epoch := g.failureSnapshot()
	g.noteFailure("phone_unreachable", false)
	recovered, hard := g.finishRecovery(recoveryResult{epoch: epoch, err: errRecoveryStorage}, time.Now())
	if recovered || !hard || g.ConnectionStatus().State != "storage_error" {
		t.Fatal("stale probe hid session persistence failure")
	}
}

func TestReconnectGraceIgnoresBriefBlipAndReportsSustainedDisconnect(t *testing.T) {
	t.Parallel()
	t.Run("brief_rollover_blip", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		g := NewGoogle(nil, "")
		g.ready.Store(true)
		g.recoveryInterval = 5 * time.Millisecond
		// Two separate blips of (disconnectGraceTicks - 1) consecutive !IsConnected
		// checks separated by a connected check must reset the counter and never fail.
		var checks atomic.Int32
		g.connected = func() bool {
			n := int(checks.Add(1))
			switch {
			case n <= disconnectGraceTicks-1:
				return false
			case n == disconnectGraceTicks:
				return true
			case n <= 2*disconnectGraceTicks-1:
				return false
			default:
				cancel()
				return true
			}
		}
		var diagnostics []AuthDiagnostic
		g.Diagnostic = func(d AuthDiagnostic) { diagnostics = append(diagnostics, d) }
		interruptions, pauses := 0, 0
		g.Run(ctx, func(Incoming) {}, func() { pauses++ }, func(string, string, string, string) {}, func() error { interruptions++; return nil })
		if g.healthFailure.Load() || !g.ready.Load() || interruptions != 0 || pauses != 0 || len(diagnostics) != 0 {
			t.Fatalf("brief stream rollover triggered failure: healthFailure=%v ready=%v interruptions=%d pauses=%d diagnostics=%+v",
				g.healthFailure.Load(), g.ready.Load(), interruptions, pauses, diagnostics)
		}
	})

	t.Run("sustained_disconnect", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		g := NewGoogle(nil, "")
		g.ready.Store(true)
		g.recoveryInterval = 5 * time.Millisecond
		var checks atomic.Int32
		g.connected = func() bool {
			checks.Add(1)
			return false
		}
		var emitted AuthDiagnostic
		interruptions, pauses := 0, 0
		g.Diagnostic = func(d AuthDiagnostic) {
			if d.Event == "stream_disconnected" {
				emitted = d
				cancel()
			}
		}
		g.Run(ctx, func(Incoming) {}, func() { pauses++ }, func(string, string, string, string) {}, func() error { interruptions++; return nil })
		if int(checks.Load()) < disconnectGraceTicks {
			t.Fatalf("failed after %d checks, want at least %d", checks.Load(), disconnectGraceTicks)
		}
		if !g.healthFailure.Load() || g.ready.Load() || g.ConnectionStatus().State != "reconnecting" {
			t.Fatalf("sustained disconnect did not enter reconnecting state: healthFailure=%v ready=%v status=%+v",
				g.healthFailure.Load(), g.ready.Load(), g.ConnectionStatus())
		}
		if interruptions != 1 || pauses != 0 {
			t.Fatalf("unexpected interruption handling: interruptions=%d pauses=%d", interruptions, pauses)
		}
		if emitted.Event != "stream_disconnected" || emitted.Success || emitted.HTTPStatus != 0 || emitted.Category != 0 {
			t.Fatalf("unexpected diagnostic: %+v", emitted)
		}
	})

	t.Run("lookup_failure_during_blip_keeps_session_up_until_grace_expires", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		g := NewGoogle(nil, "")
		g.ready.Store(true)
		g.recoveryInterval = 5 * time.Millisecond
		g.lookupPause = time.Millisecond
		g.getConversation = func(context.Context, string) (*gmproto.Conversation, error) {
			return nil, errors.New("synthetic transient lookup failure")
		}

		tick1 := make(chan struct{})
		var tick1Closed atomic.Bool
		var allowSustainedDrop atomic.Bool
		var runChecks atomic.Int32
		g.connected = func() bool {
			n := int(runChecks.Add(1))
			if tick1Closed.CompareAndSwap(false, true) {
				close(tick1)
			}
			if !allowSustainedDrop.Load() {
				// Stay within grace (< disconnectGraceTicks) while the lookup runs;
				// reset once if the scheduler stalls before allowSustainedDrop is set.
				return n%disconnectGraceTicks == 0
			}
			return false
		}

		dropped := make(chan AuthDiagnostic, 1)
		g.Diagnostic = func(d AuthDiagnostic) {
			if d.Event == "stream_disconnected" {
				select {
				case dropped <- d:
				default:
				}
				cancel()
			}
		}

		var interruptions, pauses atomic.Int32
		done := make(chan struct{})
		go func() {
			defer close(done)
			g.Run(ctx, func(Incoming) {}, func() { pauses.Add(1) }, func(string, string, string, string) {}, func() error {
				interruptions.Add(1)
				return nil
			})
		}()

		<-tick1
		_, err := g.LookupConversation(context.Background(), "c1")
		if !errors.Is(err, ErrLookupFailed) || errors.Is(err, ErrUnavailable) {
			t.Fatalf("blip lookup returned %v, want ErrLookupFailed", err)
		}
		if g.healthFailure.Load() || !g.ready.Load() || !g.SessionUp() || interruptions.Load() != 0 || pauses.Load() != 0 {
			t.Fatalf("blip lookup bypassed grace: healthFailure=%v ready=%v sessionUp=%v interruptions=%d pauses=%d",
				g.healthFailure.Load(), g.ready.Load(), g.SessionUp(), interruptions.Load(), pauses.Load())
		}

		allowSustainedDrop.Store(true)
		<-done
		select {
		case d := <-dropped:
			if d.Success {
				t.Fatalf("unexpected success diagnostic: %+v", d)
			}
		default:
			t.Fatal("sustained disconnect did not emit stream_disconnected")
		}
		if !g.healthFailure.Load() || g.ready.Load() || g.SessionUp() || interruptions.Load() != 1 || pauses.Load() != 0 {
			t.Fatalf("sustained disconnect state wrong: healthFailure=%v ready=%v sessionUp=%v interruptions=%d pauses=%d",
				g.healthFailure.Load(), g.ready.Load(), g.SessionUp(), interruptions.Load(), pauses.Load())
		}
		if _, err := g.LookupConversation(context.Background(), "c1"); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("post-drop lookup returned %v, want ErrUnavailable", err)
		}
	})
}
