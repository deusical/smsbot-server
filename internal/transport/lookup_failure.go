package transport

import (
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
// connection. One conversation the phone did not answer, while the connection is
// otherwise up, must not take the whole phone offline: that stopped every send
// until reconnection. A login problem, a dropped connection or a phone that did
// not answer in time still do.
func lookupFailure(err error, connected bool) (reason string, hard, markOffline bool) {
	switch {
	case credentialError(err):
		return "login_required", true, true
	case !connected, errors.Is(err, context.DeadlineExceeded):
		return "reconnecting", false, true
	}
	return "", false, false
}
