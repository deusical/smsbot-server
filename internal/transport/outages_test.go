package transport

import (
	"testing"
	"time"
)

func TestOutageLogReportsEarliestUnseenStart(t *testing.T) {
	var l outageLog
	if l.end() {
		t.Fatal("ended an outage that never began")
	}
	base := time.Date(2026, 10, 24, 10, 0, 0, 0, time.UTC)
	l.begin(base)
	l.begin(base.Add(time.Minute)) // a later failure keeps the original start
	if !l.end() {
		t.Fatal("outage not recorded")
	}
	l.begin(base.Add(time.Hour))
	l.end()
	since, latest, ok := l.since(0)
	if !ok || !since.Equal(base) || latest != 2 {
		t.Fatalf("want earliest start and seq 2, got %v %d %v", since, latest, ok)
	}
	if since, _, ok = l.since(1); !ok || !since.Equal(base.Add(time.Hour)) {
		t.Fatal("seen outage reported again")
	}
	if _, _, ok = l.since(2); ok {
		t.Fatal("reported an outage after the latest")
	}
	// Folding old entries never loses the earliest unseen start.
	for i := 0; i < 40; i++ {
		l.begin(base.Add(time.Duration(i+2) * time.Hour))
		l.end()
	}
	if since, _, _ = l.since(0); !since.Equal(base) {
		t.Fatal("folded outages lost the earliest start", since)
	}
}
