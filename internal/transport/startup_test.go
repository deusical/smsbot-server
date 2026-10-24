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

func TestStartupWithRejectedCredentialsStops(t *testing.T) {
	g := NewGoogle(nil, "")
	if g.startUnready(events.ErrInvalidCredentials) == nil {
		t.Fatal("rejected credentials started the service")
	}
}
