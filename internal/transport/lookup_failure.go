package transport

import (
	"cmp"
	"context"
	"errors"
	"time"

	"go.mau.fi/mautrix-gmessages/pkg/libgm/gmproto"
)

// lookupRetryPause is how long a conversation lookup waits before its one retry.
const lookupRetryPause = 400 * time.Millisecond

// fetchConversation asks the phone for one conversation and asks once more if
// the first try fails for a reason that is not a cancelled request or a
// credential problem. A lookup only reads, so repeating it is always safe.
func fetchConversation(ctx context.Context, id string, pause time.Duration, get func(context.Context, string) (*gmproto.Conversation, error)) (*gmproto.Conversation, error) {
	c, err := get(ctx, id)
	if err == nil || errors.Is(err, context.Canceled) || credentialError(err) || ctx.Err() != nil {
		return c, err
	}
	select {
	case <-ctx.Done():
		return nil, err
	case <-time.After(pause):
	}
	return get(ctx, id)
}

// lookupFailure decides what a failed conversation lookup says about the
// connection. One conversation the phone did not answer, while the session is
// otherwise up, must not take the whole phone offline: that stopped every send
// until reconnection. Credential errors still mark offline immediately; a
// lookup timeout triggers a coalesced readiness probe off the caller's path and
// only marks offline if that probe fails.
func lookupFailure(err error) (reason string, hard, markOffline bool) {
	if credentialError(err) {
		return "login_required", true, true
	}
	return "", false, false
}

func (g *Google) bugleDefaultProbe() func(context.Context) (*gmproto.IsBugleDefaultResponse, error) {
	if g.probeBugleDefault != nil {
		return g.probeBugleDefault
	}
	if g.client == nil {
		return func(context.Context) (*gmproto.IsBugleDefaultResponse, error) {
			return nil, errors.New("client_uninitialized")
		}
	}
	return g.client.IsBugleDefault
}

// probeReadinessOnTimeout runs at most one off-path readiness probe when a
// conversation lookup times out, marking the phone offline only if it fails.
func (g *Google) probeReadinessOnTimeout() {
	if !g.SessionUp() || !g.probeInflight.CompareAndSwap(false, true) {
		return
	}
	g.recoveryWG.Add(1)
	go func() {
		defer g.recoveryWG.Done()
		defer g.probeInflight.Store(false)
		if !g.SessionUp() {
			return
		}
		ctx, cancel := context.WithTimeout(WithPhonePurpose(context.Background(), "readiness"), cmp.Or(g.probeTimeout, 10*time.Second))
		defer cancel()
		g.countPhone(ctx, phoneProbe)
		resp, err := g.bugleDefaultProbe()(ctx)
		probeErr := err
		if probeErr == nil && !resp.GetSuccess() {
			probeErr = errors.New("phone_unreachable")
		}
		g.diagnose("phone_readiness", probeErr)
		if probeErr != nil && g.SessionUp() {
			if credentialError(probeErr) {
				g.noteFailure("login_required", true)
			} else {
				g.noteFailure("reconnecting", false)
			}
		}
	}()
}
