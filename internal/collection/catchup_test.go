package collection

import (
	"testing"
	"time"

	"github.com/deusical/smsbot-server/internal/transport"
)

type outageTransport struct {
	*transport.Fixture
	since time.Time
	seq   uint64
}

func (o *outageTransport) OutagesSince(after uint64) (time.Time, uint64, bool) {
	if after < o.seq {
		return o.since, o.seq, true
	}
	return time.Time{}, o.seq, false
}

func TestMissedSinceCoversServiceDowntimeThenOutages(t *testing.T) {
	svc, f := setup(t)
	newest := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
	if err := svc.Store.SaveObserved("chat", "m1", "", "incoming", "Synthetic text", "phone", "", newest.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := svc.Store.SaveObserved("chat", "m2", "", "incoming", "Synthetic text", "phone", "", newest); err != nil {
		t.Fatal(err)
	}
	host := svc.HistoryHost()
	// Service start: anything after the newest saved message may be missing.
	since, next, missed := host.MissedSince(0)
	if !missed || !since.Equal(newest) || next != 1 {
		t.Fatalf("startup window: %v %d %v", since, next, missed)
	}
	if _, next, missed = host.MissedSince(next); missed || next != 1 {
		t.Fatal("no outage, yet capture reported missed")
	}
	outageStart := time.Now().Add(-5 * time.Minute)
	svc.Transport = &outageTransport{Fixture: f, since: outageStart, seq: 3}
	if since, next, missed = host.MissedSince(1); !missed || !since.Equal(outageStart) || next != 4 {
		t.Fatalf("outage window: %v %d %v", since, next, missed)
	}
	if _, _, missed = host.MissedSince(4); missed {
		t.Fatal("handled outage reported again")
	}
}
