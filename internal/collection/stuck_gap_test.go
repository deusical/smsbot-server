package collection

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/deusical/smsbot-server/internal/store"
	"github.com/deusical/smsbot-server/internal/transport"
)

// lookupOnly answers identity lookups and counts them; history reads fail.
type lookupOnly struct {
	*transport.Fixture
	lookups int
	result  func(id string) (transport.Conversation, error)
}

func (l *lookupOnly) LookupConversation(_ context.Context, id string) (transport.Conversation, error) {
	l.lookups++
	return l.result(id)
}
func (l *lookupOnly) FetchHistory(context.Context, string, string, string) (transport.HistoryPage, error) {
	return transport.HistoryPage{}, errors.New("unexpected history read")
}

func stuckGap(t *testing.T, svc *Service) {
	t.Helper()
	at := time.Now().Add(-time.Hour)
	g := store.CaptureGap{Kind: "live_capture", Conversation: "group-chat", Since: at.UTC().Format(time.RFC3339Nano), Until: at.Add(time.Minute).UTC().Format(time.RFC3339Nano), Reason: "phone_unavailable", State: "retrying", Generation: 1}
	if err := svc.Store.SaveCaptureGap(g); err != nil {
		t.Fatal(err)
	}
}

// A gap in a conversation collection does not apply to closes after one
// lookup instead of retrying every few minutes forever.
func TestUncollectableGapSettlesWithoutRetrying(t *testing.T) {
	svc, f := setup(t)
	enableInbox(t, svc)
	source := &lookupOnly{Fixture: f, result: func(id string) (transport.Conversation, error) {
		return transport.Conversation{ID: id, ParticipantID: "", Group: true, Writable: true}, nil
	}}
	svc.Transport = source
	stuckGap(t, svc)
	if !svc.InboxCaptureGap() {
		t.Fatal("seeded gap not pending")
	}
	if err := svc.HistoryHost().RepairCaptureGap(context.Background()); err != nil {
		t.Fatal(err)
	}
	gaps, _ := svc.Store.CaptureGaps()
	if len(gaps) != 1 || gaps[0].State != "not_collected" || gaps[0].CheckedAt == "" {
		t.Fatalf("gap not settled: %+v", gaps)
	}
	if svc.InboxCaptureGap() {
		t.Fatal("settled gap still reported as pending")
	}
	if _, pending := svc.HistoryHost().NextGapAttempt(); pending {
		t.Fatal("settled gap still schedules repair")
	}
	for range 3 {
		_ = svc.HistoryHost().RepairCaptureGap(context.Background())
	}
	if source.lookups != 1 {
		t.Fatalf("settled gap kept waking the phone: %d lookups", source.lookups)
	}
}

// Repeated lookup failures back off toward a daily retry.
func TestFailingGapLookupsBackOffToDaily(t *testing.T) {
	svc, f := setup(t)
	enableInbox(t, svc)
	svc.Transport = &lookupOnly{Fixture: f, result: func(string) (transport.Conversation, error) {
		return transport.Conversation{}, errors.New("conversation_unavailable")
	}}
	now := time.Now()
	svc.SetClock(func() time.Time { return now })
	stuckGap(t, svc)
	gaps, _ := svc.Store.CaptureGaps()
	g := gaps[0]
	g.Failures = 30
	if err := svc.Store.SaveCaptureGap(g); err != nil {
		t.Fatal(err)
	}
	if err := svc.HistoryHost().RepairCaptureGap(context.Background()); err != nil {
		t.Fatal(err)
	}
	gaps, _ = svc.Store.CaptureGaps()
	retry, _ := time.Parse(time.RFC3339Nano, gaps[0].RetryAt)
	if gaps[0].State != "retrying" || retry.Sub(now) < 23*time.Hour {
		t.Fatalf("sustained failure still retries soon: %+v", gaps[0])
	}
}
