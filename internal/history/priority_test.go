package history

import (
	"context"
	"errors"
	"github.com/deusical/smsbot-server/internal/store"
	"github.com/deusical/smsbot-server/internal/transport"
	"github.com/deusical/smsbot-server/internal/vault"
	"path/filepath"
	"testing"
	"time"
)

type priorityHost struct {
	*retryHost
	discovered bool
	ids        []string
	deny       string
}

func (h *priorityHost) Discover(context.Context) ([]transport.Conversation, error) {
	h.discovered = true
	return nil, errors.New("synthetic discovery unavailable")
}
func (h *priorityHost) Fetch(_ context.Context, c transport.Conversation, cursor string) (transport.HistoryPage, error) {
	h.ids = append(h.ids, c.ID)
	return transport.HistoryPage{NextCursor: "next-" + cursor}, nil
}
func TestExplicitRequestsAdvanceBeforeDiscoveryAndRotate(t *testing.T) {
	v, _ := vault.New(make([]byte, 32))
	s, err := store.Open(filepath.Join(t.TempDir(), "state.db"), v)
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	h := &priorityHost{retryHost: &retryHost{s: s, fail: map[string]bool{}}}
	worker := New(s, h)
	for _, id := range []string{"owner-a", "owner-b"} {
		a := store.Archive{Conversation: transport.Conversation{ID: id, ParticipantID: "peer", Writable: true}, State: "queued", Generation: 1, RequestedAt: time.Now().UTC().Format(time.RFC3339Nano)}
		if err := s.SaveArchive(a, 0); err != nil {
			t.Fatal(err)
		}
	}
	worker.Tick(context.Background())
	worker.Tick(context.Background())
	if len(h.ids) != 2 || h.ids[0] == h.ids[1] {
		t.Fatal("manual imports did not advance fairly before failed discovery", h.ids)
	}
	for _, id := range h.ids {
		a, err := s.Archive(id)
		if err != nil || a.Pages != 1 || a.LastAttemptAt == "" || a.LastProgressAt == "" {
			t.Fatal("manual progress was not persisted")
		}
	}
}

func (h *priorityHost) Policy(c transport.Conversation) (Policy, error) {
	if c.ID == h.deny {
		return Policy{}, errors.New("synthetic access disabled")
	}
	return Policy{}, nil
}
func TestInaccessibleManualJobCannotStarveOtherRequests(t *testing.T) {
	v, _ := vault.New(make([]byte, 32))
	s, err := store.Open(filepath.Join(t.TempDir(), "state.db"), v)
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	h := &priorityHost{retryHost: &retryHost{s: s, fail: map[string]bool{}}, deny: "a-blocked"}
	worker := New(s, h)
	for _, id := range []string{"a-blocked", "b-allowed"} {
		if err := s.SaveArchive(store.Archive{Conversation: transport.Conversation{ID: id, ParticipantID: "peer", Writable: true}, State: "queued", Generation: 1, RequestedAt: time.Now().UTC().Format(time.RFC3339Nano)}, 0); err != nil {
			t.Fatal(err)
		}
	}
	worker.Tick(context.Background())
	if len(h.ids) != 1 || h.ids[0] != "b-allowed" {
		t.Fatal("inaccessible request starved allowed request", h.ids)
	}
}

func TestExistingDateLimitedQueuedImportGetsPriorityOnUpgrade(t *testing.T) {
	v, _ := vault.New(make([]byte, 32))
	s, err := store.Open(filepath.Join(t.TempDir(), "state.db"), v)
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	h := &priorityHost{retryHost: &retryHost{s: s, fail: map[string]bool{}}}
	worker := New(s, h)
	if err := s.SaveArchive(store.Archive{Conversation: transport.Conversation{ID: "legacy-owner-request", ParticipantID: "peer", Writable: true}, State: "queued", Generation: 1, Since: "2026-07-16T07:00:00Z"}, 0); err != nil {
		t.Fatal(err)
	}
	worker.Tick(context.Background())
	a, _ := s.Archive("legacy-owner-request")
	if len(h.ids) != 1 || a.Pages != 1 || a.RequestSource != "owner" {
		t.Fatal("existing dated owner request was left behind routine discovery")
	}
}
