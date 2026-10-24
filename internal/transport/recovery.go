package transport

import (
	"context"
	"errors"
	"go.mau.fi/mautrix-gmessages/pkg/libgm/events"
	"go.mau.fi/mautrix-gmessages/pkg/libgm/gmproto"
	"math/rand/v2"
	"sync"
	"time"
)

type ConnectionStatus struct {
	State   string `json:"state"`
	RetryAt string `json:"retry_at,omitempty"`
}
type ConnectionReporter interface{ ConnectionStatus() ConnectionStatus }
type recoveryControl struct {
	mu            sync.Mutex
	epoch         uint64
	reason        string
	hard          bool
	inflight      bool
	inflightEpoch uint64
	attempts      int
	next          time.Time
	lastAttempt   time.Time
}
type recoveryResult struct {
	epoch uint64
	err   error
}
type connectionEvent struct{ event any }

var errRecoveryStorage = errors.New("session_save_failed")

func credentialError(err error) bool {
	if errors.Is(err, events.ErrInvalidCredentials) || errors.Is(err, events.ErrRequestedEntityNotFound) || errors.Is(err, events.ErrCallerNoPermission) {
		return true
	}
	var h events.HTTPError
	if errors.As(err, &h) && h.Resp != nil && (h.Resp.StatusCode == 401 || h.Resp.StatusCode == 403 || h.Resp.StatusCode == 404) {
		return true
	}
	var r events.RequestError
	if errors.As(err, &r) {
		if r.Data != nil && (r.Data.GetType() == 16 || r.Data.GetType() == 7) {
			return true
		}
		return r.HTTP != nil && r.HTTP.Resp != nil && (r.HTTP.Resp.StatusCode == 401 || r.HTTP.Resp.StatusCode == 403 || r.HTTP.Resp.StatusCode == 404)
	}
	return false
}

// Return only fixed identifiers and typed errors; no upstream free text is exposed.
func connectionFailure(event any) (name, reason string, hard bool, err error, ok bool) {
	ok = true
	switch e := event.(type) {
	case *events.ListenTemporaryError:
		name, reason, err = "listen_temporary", "reconnecting", e.Error
	case *events.PingFailed:
		name, reason, err = "ping_failed", "reconnecting", e.Error
	case *events.ListenFatalError:
		name, reason, hard, err = "listen_fatal", "connection_error", true, e.Error
	case *events.PhoneNotResponding:
		name, reason = "phone_not_responding", "phone_unreachable"
	case *events.NoDataReceived:
		name, reason = "no_data_received", "reconnecting"
	case *events.GaiaLoggedOut:
		name, reason, hard = "google_logged_out", "login_required", true
	case *events.HackySetActiveMayFail:
		name, reason, hard = "active_session_warning", "session_conflict", true
	case *events.AccountChange:
		if e.IsFake {
			return "", "", false, nil, false
		}
		name, reason, hard = "account_change", "account_changed", true
	default:
		return "", "", false, nil, false
	}
	if credentialError(err) {
		reason = "login_required"
		hard = true
	}
	if err == nil {
		err = errors.New("unavailable")
	}
	return
}
func (g *Google) noteFailure(reason string, hard bool) {
	g.identityEpoch.Add(1)
	r := &g.recovery
	r.mu.Lock()
	r.epoch++
	if !r.hard {
		r.reason = reason
		r.hard = hard
	}
	// A later temporary event can never clear an authentication/session blocker.
	if r.next.IsZero() {
		r.next = time.Now().Add(2 * time.Second)
	}
	g.healthFailure.Store(true)
	g.ready.Store(false)
	r.mu.Unlock()
	g.outages.begin(time.Now())
	g.wakeRecovery(false)
}
func (g *Google) wakeRecovery(recovered bool) {
	if recovered {
		r := &g.recovery
		r.mu.Lock()
		if !r.hard && time.Since(r.lastAttempt) >= 2*time.Second {
			r.next = time.Now()
		}
		r.mu.Unlock()
	}
	select {
	case g.recoveryWake <- struct{}{}:
	default:
	}
}
func (g *Google) ConnectionStatus() ConnectionStatus {
	r := &g.recovery
	r.mu.Lock()
	defer r.mu.Unlock()
	if g.healthFailure.Load() {
		state := r.reason
		if state == "" {
			state = "reconnecting"
		}
		status := ConnectionStatus{State: state}
		if !r.hard && !r.next.IsZero() {
			status.RetryAt = r.next.UTC().Format(time.RFC3339Nano)
		}
		return status
	}
	if g.overflow.Load() {
		return ConnectionStatus{State: "receive_overload"}
	}
	if g.ready.Load() && (g.client == nil || g.client.IsConnected()) {
		return ConnectionStatus{State: "connected"}
	}
	return ConnectionStatus{State: "reconnecting"}
}
func (g *Google) failureSnapshot() (bool, bool, uint64) {
	r := &g.recovery
	r.mu.Lock()
	defer r.mu.Unlock()
	return g.healthFailure.Load(), r.hard, r.epoch
}
func recoveryDelay(attempt int) time.Duration {
	base := min(300*time.Second, 5*time.Second*time.Duration(1<<min(max(attempt-1, 0), 6)))
	return min(300*time.Second, base+time.Duration(rand.Int64N(int64(base/4)+1)))
}
func (g *Google) startRecovery(ctx context.Context, now time.Time, results chan<- recoveryResult) {
	ctx = WithPhonePurpose(ctx, "readiness")
	if g.client == nil && g.recoveryProbe == nil {
		return
	}
	r := &g.recovery
	r.mu.Lock()
	if !g.healthFailure.Load() || r.hard || r.inflight || now.Before(r.next) {
		r.mu.Unlock()
		return
	}
	epoch := r.epoch
	r.inflight = true
	r.inflightEpoch = epoch
	r.attempts++
	attempt := r.attempts
	r.lastAttempt = now
	r.next = time.Time{}
	r.mu.Unlock()
	go func() {
		call, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		var err error
		if g.recoveryProbe != nil {
			err = g.recoveryProbe(call)
		} else {
			// libgm already retries its stream. Re-arm it only after repeated failed
			// checks with no stream, and keep its lifetime tied to the service context.
			if !g.client.IsConnected() && attempt >= 3 {
				if current, hard, version := g.failureSnapshot(); current && !hard && version == epoch {
					err = g.client.Reconnect(ctx)
				} else {
					err = context.Canceled
				}
			}
			if err == nil {
				var responding bool
				g.countPhone(ctx, phoneProbe)
				response, probeErr := g.client.IsBugleDefault(call)
				responding = probeErr == nil && response.GetSuccess()
				if probeErr != nil {
					err = probeErr
				} else if !responding || !g.client.IsConnected() {
					err = errors.New("phone_unreachable")
				}
			}
			if err == nil {
				if failed, hard, version := g.failureSnapshot(); !failed || hard || version != epoch {
					err = context.Canceled
				} else if g.Save() != nil {
					err = errRecoveryStorage
				}
			}
		}
		select {
		case results <- recoveryResult{epoch, err}:
		case <-ctx.Done():
			r.mu.Lock()
			if r.inflightEpoch == epoch {
				r.inflight = false
			}
			r.mu.Unlock()
		}
	}()
}

// Called by the event loop, so the command freshness cutoff and queue processing
// cannot race. A newer failure invalidates any earlier successful probe.
func (g *Google) finishRecovery(result recoveryResult, now time.Time) (recovered, hard bool) {
	r := &g.recovery
	r.mu.Lock()
	if r.inflightEpoch == result.epoch {
		r.inflight = false
	}
	// A failed session save or rejected credentials remain blockers even if a
	// concurrent temporary event invalidated this probe's success generation.
	if credentialError(result.err) || errors.Is(result.err, errRecoveryStorage) {
		if !r.hard {
			r.hard = true
			r.reason = "login_required"
			if errors.Is(result.err, errRecoveryStorage) {
				r.reason = "storage_error"
			}
			r.epoch++
		}
		g.healthFailure.Store(true)
		g.ready.Store(false)
		r.mu.Unlock()
		return false, true
	}
	valid := r.epoch == result.epoch && !r.hard && g.healthFailure.Load()
	r.mu.Unlock()
	if !valid {
		return false, false
	}
	if result.err == nil {
		g.invalidateIdentities()
	}
	g.queueMu.Lock()
	defer g.queueMu.Unlock()
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.epoch != result.epoch || r.hard {
		return false, false
	}
	if result.err != nil {
		r.next = now.Add(recoveryDelay(r.attempts))
		return false, false
	}
	// Identity invalidation may wait for an in-flight read; exclude that whole gap.
	g.started = time.Now()
	g.healthFailure.Store(false)
	g.ready.Store(true)
	r.reason = ""
	r.attempts = 0
	r.next = time.Time{}
	return true, false
}

// invalidateIdentities makes every authorizing path verify identity live
// again after a connection gap, and makes the discovery pages stale, since
// events may have been missed during the gap. The next pass re-reads pages
// from the front until one comes back unchanged.
func (g *Google) invalidateIdentities() {
	g.identityEpoch.Add(1)
	g.listMu.Lock()
	g.mu.Lock()
	g.listed = time.Time{}
	g.list = nil
	g.conversations = make(map[string]*gmproto.Conversation)
	g.mu.Unlock()
	g.listMu.Unlock()
	g.discovery.invalidate()
	// Dashboards refetch only on a revision change; the cleared list must be read again.
	g.activity.revision.Add(1)
}
