package history

import (
	"context"
	"github.com/deusical/smsbot-server/internal/store"
	"github.com/deusical/smsbot-server/internal/transport"
	"github.com/deusical/smsbot-server/internal/vault"
	"path/filepath"
	"testing"
	"time"
)

// catchUpHost lists conversations with phone-reported last activity and
// reports one missed-capture window per token.
type catchUpHost struct {
	*retryHost
	list   []transport.Conversation
	since  time.Time
	token  uint64
	pages  int // pages remaining in the current discovery pass
	served int
}

func (h *catchUpHost) Discover(context.Context) ([]transport.Conversation, error) {
	h.served++
	if h.pages > 0 {
		h.pages--
	}
	return h.list, nil
}
func (h *catchUpHost) DiscoveryPending() bool { return h.pages > 0 }
func (h *catchUpHost) MissedSince(token uint64) (time.Time, uint64, bool) {
	if token < h.token {
		return h.since, h.token, true
	}
	return time.Time{}, token, false
}

func TestOutageCatchUpScansOnlyConversationsActiveSinceIt(t *testing.T) {
	v, _ := vault.New(make([]byte, 32))
	s, err := store.Open(filepath.Join(t.TempDir(), "state.db"), v)
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	now := time.Now().UTC()
	outage := now.Add(-20 * time.Minute)
	conv := func(id string, last time.Time) transport.Conversation {
		return transport.Conversation{ID: id, ParticipantID: "peer", Writable: true, LastMessageAt: last.Format(time.RFC3339)}
	}
	active, quiet := conv("active", now.Add(-10*time.Minute)), conv("quiet", outage.Add(-time.Hour))
	for _, c := range []transport.Conversation{active, quiet} {
		a := store.NewArchive(c)
		a.State = "available_pages_exhausted"
		a.LastSync = outage.Add(-48 * time.Hour).Format(time.RFC3339Nano)
		if err = s.SaveArchive(a, 0); err != nil {
			t.Fatal(err)
		}
	}
	h := &catchUpHost{retryHost: &retryHost{s: s, fail: map[string]bool{}}, list: []transport.Conversation{active, quiet}, since: outage, token: 1}
	worker := New(s, h)
	worker.nextDiscovery = now.Add(time.Hour) // the outage, not the schedule, starts discovery
	worker.nextFallback = now.Add(time.Hour)
	worker.Tick(context.Background())
	if h.served != 1 {
		t.Fatal("outage did not start a discovery pass")
	}
	a, _ := s.Archive("active")
	want := outage.Add(-30 * time.Minute).Format(time.RFC3339Nano)
	// The scan paged back to before the outage and completed in this turn.
	if a.RecentSince != want || a.RecentStarted != "" || parseTime(a.LastSync).Before(outage) {
		t.Fatalf("active conversation not caught up: %+v", a.RecentCheckpoint)
	}
	if len(h.calls) != 1 {
		t.Fatalf("want one recent page for the active conversation, got %v", h.calls)
	}
	q, _ := s.Archive("quiet")
	if q.RecentStarted != "" || q.LastSync != outage.Add(-48*time.Hour).Format(time.RFC3339Nano) {
		t.Fatal("quiet conversation was scanned")
	}
	// The window closed with the pass; the same token causes no new work.
	worker.nextDiscovery = now.Add(time.Hour)
	worker.Tick(context.Background())
	if h.served != 1 || len(h.calls) != 1 || !worker.catchUpSince.IsZero() {
		t.Fatal("finished catch-up repeated")
	}
}

func TestCatchUpWindowSpansAWholePass(t *testing.T) {
	v, _ := vault.New(make([]byte, 32))
	s, err := store.Open(filepath.Join(t.TempDir(), "state.db"), v)
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	h := &catchUpHost{retryHost: &retryHost{s: s, fail: map[string]bool{}}, since: time.Now(), token: 1, pages: 3}
	worker := New(s, h)
	worker.nextFallback = time.Now().Add(time.Hour)
	// A pass already under way when the outage ended does not count: the
	// window stays open until a pass that started after it finishes.
	h.pages = 2
	worker.Tick(context.Background()) // continues the earlier pass (pending before the call)
	if worker.catchUpSince.IsZero() {
		t.Fatal("window closed by a pass that began before the outage ended")
	}
	for i := 0; i < 5 && !worker.catchUpSince.IsZero(); i++ {
		worker.nextDiscovery = time.Time{}
		worker.Tick(context.Background())
	}
	if !worker.catchUpSince.IsZero() {
		t.Fatal("window never closed")
	}
}
