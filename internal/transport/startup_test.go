package transport

import (
	"context"
	"testing"
	"time"

	"go.mau.fi/mautrix-gmessages/pkg/libgm/events"
)

// An unreachable phone at startup leaves the service running and unready,
// recovering with backoff, instead of exiting.
func TestStartupWithUnreachablePhoneRecoversInBackground(t *testing.T) {
	t.Parallel()
	g := NewGoogle(nil, "")
	if err := g.startUnready(context.DeadlineExceeded); err != nil {
		t.Fatal("startup failed for an unreachable phone:", err)
	}
	status := g.ConnectionStatus()
	if g.Ready() || status.State != "phone_unreachable" || status.RetryAt == "" {
		t.Fatalf("want unready with a scheduled retry, got %+v", status)
	}
	if _, _, ok := g.outages.since(0); ok {
		t.Fatal("outage reported before it ended")
	}
	g.recoveryProbe = func(context.Context) error { return nil }
	results := make(chan recoveryResult, 1)
	g.startRecovery(context.Background(), time.Now().Add(3*time.Second), results)
	if recovered, _ := g.finishRecovery(<-results, time.Now()); !recovered {
		t.Fatal("startup outage did not recover")
	}
	if g.ConnectionStatus().State != "connected" {
		t.Fatal("recovered startup not connected")
	}
}

func TestStartupWithRejectedCredentialsStartsUnready(t *testing.T) {
	t.Parallel()
	g := NewGoogle(nil, "")
	if err := g.startUnready(events.ErrInvalidCredentials); err != nil {
		t.Fatal("startup failed on rejected credentials:", err)
	}
	status := g.ConnectionStatus()
	if g.Ready() || status.State != "login_required" || status.RetryAt != "" {
		t.Fatalf("want unready with login_required and no retry, got %+v", status)
	}
}
