package app

import (
	"context"
	"encoding/json"
	"github.com/deusical/smsbot-server/internal/store"
	"github.com/deusical/smsbot-server/internal/transport"
	"github.com/deusical/smsbot-server/internal/vault"
	"path/filepath"
	"testing"
	"time"
)

func openStore(t *testing.T) *store.Store {
	t.Helper()
	v, _ := vault.New(make([]byte, 32))
	s, err := store.Open(filepath.Join(t.TempDir(), "state.db"), v)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.DB.Close() })
	return s
}

func assemble(t *testing.T, s *store.Store) *Service {
	return New(s, transport.NewFixture(), filepath.Join(filepath.Dir(s.Path), "archive"))
}

// Opted-in history is marked for preservation before the legacy expiry pass;
// without collection the same old message expires.
func TestInitPreservesOptedInHistoryBeforeExpiry(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		s := openStore(t)
		old := time.Now().AddDate(0, 0, -30)
		if err := s.SaveObserved("kept-chat", "old-message", "", "incoming", "Synthetic old text", "phone", "", old); err != nil {
			t.Fatal(err)
		}
		settings, _ := json.Marshal(map[string]any{"enabled": enabled, "scope": "all", "conversations": []any{}})
		if err := s.SetSecretSetting("inbox_settings", "inbox-settings", settings); err != nil {
			t.Fatal(err)
		}
		if err := assemble(t, s).Init(); err != nil {
			t.Fatal(err)
		}
		var n int
		if err := s.DB.QueryRow("SELECT count(*) FROM messages WHERE conversation='kept-chat'").Scan(&n); err != nil {
			t.Fatal(err)
		}
		if (n == 1) != enabled {
			t.Fatalf("collection=%v kept=%d", enabled, n)
		}
	}
}

func TestPrepareStorePausesEveryStart(t *testing.T) {
	s := openStore(t)
	if err := s.Pause(false); err != nil {
		t.Fatal(err)
	}
	if err := PrepareStore(s); err != nil {
		t.Fatal(err)
	}
	if paused, err := s.Paused(); err != nil || !paused {
		t.Fatal("start did not pause automation", err)
	}
}

type heldEvents struct {
	started, stopped chan struct{}
}

func (h *heldEvents) Run(ctx context.Context, _ func(transport.Incoming), _ func(), _ func(string, string, string, string), _ ...func() error) {
	close(h.started)
	<-ctx.Done()
	close(h.stopped)
}

// Stop returns only after every worker, including the event loop, has exited.
func TestStopWaitsForEveryWorker(t *testing.T) {
	s := openStore(t)
	svc := assemble(t, s)
	if err := svc.Init(); err != nil {
		t.Fatal(err)
	}
	events := &heldEvents{started: make(chan struct{}), stopped: make(chan struct{})}
	workers := svc.Start(context.Background(), events, nil)
	select {
	case <-events.started:
	case <-time.After(5 * time.Second):
		t.Fatal("event loop did not start")
	}
	done := make(chan struct{})
	go func() { workers.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("workers did not stop")
	}
	select {
	case <-events.stopped:
	default:
		t.Fatal("Stop returned before the event loop exited")
	}
}
