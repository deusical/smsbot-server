package collection

import (
	"context"
	"github.com/deusical/smsbot-server/internal/archive"
	"github.com/deusical/smsbot-server/internal/history"
	"github.com/deusical/smsbot-server/internal/store"
	"github.com/deusical/smsbot-server/internal/transport"
	"github.com/deusical/smsbot-server/internal/vault"
	"path/filepath"
	"sync"
	"testing"
)

// setup assembles a collection service with its workers over the offline
// fixture, as internal/app does, without a bot engine.
func setup(t *testing.T) (*Service, *transport.Fixture) {
	t.Helper()
	v, err := vault.New(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	s, err := store.Open(filepath.Join(t.TempDir(), "state.db"), v)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.DB.Close() })
	f := transport.NewFixture()
	svc := New(s, f, new(sync.Mutex))
	svc.History = history.New(s, svc.HistoryHost())
	svc.Archive = archive.New(s, filepath.Join(filepath.Dir(s.Path), "archive"), svc.FileHost())
	if err = svc.Init(); err != nil {
		t.Fatal(err)
	}
	return svc, f
}

func enableInbox(t *testing.T, svc *Service) Settings {
	t.Helper()
	s, _ := svc.Settings()
	s.Enabled = true
	s.LoadOlder = true
	if err := svc.SetSettings(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	s, _ = svc.Settings()
	return s
}

// arm enrolls the fixture conversation and turns sending on, so a test can
// show that collection still never creates command jobs.
func arm(t *testing.T, svc *Service) {
	t.Helper()
	if err := svc.Store.Enroll("demo-chat", "demo-peer"); err != nil {
		t.Fatal(err)
	}
	if err := svc.Store.Pause(false); err != nil {
		t.Fatal(err)
	}
}

// commitStep publishes a's checkpoint for w through the real host, as a
// scheduler step that read a before its fetch would.
func commitStep(svc *Service, w history.Work, a store.Archive, p history.Policy, page *transport.HistoryPage) error {
	c := history.Commit{Work: w, Conversation: a.Conversation, Policy: p, Page: page}
	switch w {
	case history.ImportWork:
		c.Token = a.Generation
		c.Apply = func(r *store.Archive) { r.ImportCheckpoint = a.ImportCheckpoint }
	case history.RecentWork:
		c.Apply = func(r *store.Archive) { r.RecentCheckpoint = a.RecentCheckpoint }
	case history.ViewWork:
		c.Token = a.ViewGeneration
		c.Apply = func(r *store.Archive) { r.ViewCheckpoint = a.ViewCheckpoint }
	}
	return svc.HistoryHost().Commit(context.Background(), c)
}
