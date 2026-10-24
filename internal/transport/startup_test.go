package transport

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/deusical/smsbot-server/internal/vault"
	"go.mau.fi/mautrix-gmessages/pkg/libgm/events"
	"go.mau.fi/mautrix-gmessages/pkg/libgm/gmproto"
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

func TestDiscoveryHintsSurviveRestartEncrypted(t *testing.T) {
	v, _ := vault.New(make([]byte, 32))
	path := filepath.Join(t.TempDir(), "google-session.enc")
	g := NewGoogle(v, path)
	g.cacheDetail(&gmproto.Conversation{ConversationID: "c1", Name: "Synthetic Name", DefaultOutgoingID: "me", Participants: []*gmproto.Participant{{IsVisible: true, ID: &gmproto.SmallInfo{ParticipantID: "peer"}}}})
	if err := g.saveHints(); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(g.hintsPath())
	if len(raw) == 0 || strings.Contains(string(raw), "Synthetic Name") {
		t.Fatal("hints missing or stored in plain text")
	}
	if g.hintsDirty.Load() {
		t.Fatal("saved hints still dirty")
	}

	restarted := NewGoogle(v, path)
	restarted.loadHints() // not connected: a phone lookup would fail
	c, err := restarted.DiscoveryLookup(context.Background(), "c1")
	if err != nil || c.Label != "Synthetic Name" || c.ParticipantID != "peer" || !c.Writable {
		t.Fatalf("restored hint not used: %+v %v", c, err)
	}
	// Hints never authorize: the live identity cache stays empty.
	if len(restarted.conversations) != 0 {
		t.Fatal("restored hints became verified identities")
	}

	other, _ := vault.New(append(make([]byte, 31), 1))
	foreign := NewGoogle(other, path)
	foreign.loadHints()
	if foreign.discoveryDetail("c1") != nil {
		t.Fatal("hints opened with another key")
	}
}

func TestPairingDropsEarlierHints(t *testing.T) {
	v, _ := vault.New(make([]byte, 32))
	path := filepath.Join(t.TempDir(), "google-session.enc")
	g := NewGoogle(v, path)
	g.cacheDetail(&gmproto.Conversation{ConversationID: "c1"})
	if err := g.saveHints(); err != nil {
		t.Fatal(err)
	}
	if err := g.Pair(context.Background(), "not cookies", func(string) {}); err == nil {
		t.Fatal("pairing accepted invalid cookies")
	}
	if _, err := os.Stat(g.hintsPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("earlier account's hints kept")
	}
}
