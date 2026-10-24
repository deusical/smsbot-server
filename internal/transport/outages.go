package transport

import (
	"sync"
	"time"
)

// OutageReporter reports finished connection outages: windows in which live
// capture may have missed messages because events were not being received.
// It holds only times, never identifiers.
type OutageReporter interface {
	// OutagesSince returns the earliest start of the outages finished after
	// the given sequence number, and the latest sequence number to pass next.
	OutagesSince(after uint64) (since time.Time, latest uint64, ok bool)
}

type outage struct {
	seq   uint64
	since time.Time
}

// outageLog keeps the start of the current outage and the last finished ones.
type outageLog struct {
	mu       sync.Mutex
	open     time.Time
	seq      uint64
	finished []outage
}

// begin marks the start of an outage; a later failure in the same outage
// keeps the original start.
func (l *outageLog) begin(now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.open.IsZero() {
		l.open = now
	}
}

// end records the open outage as finished, if there is one.
func (l *outageLog) end() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.open.IsZero() {
		return false
	}
	l.seq++
	l.finished = append(l.finished, outage{l.seq, l.open})
	if len(l.finished) > 32 {
		// Fold the oldest into its successor so its start is never lost.
		if l.finished[0].since.Before(l.finished[1].since) {
			l.finished[1].since = l.finished[0].since
		}
		l.finished = l.finished[1:]
	}
	l.open = time.Time{}
	return true
}

func (l *outageLog) since(after uint64) (time.Time, uint64, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	var earliest time.Time
	for _, o := range l.finished {
		if o.seq > after && (earliest.IsZero() || o.since.Before(earliest)) {
			earliest = o.since
		}
	}
	return earliest, l.seq, !earliest.IsZero()
}

func (g *Google) OutagesSince(after uint64) (time.Time, uint64, bool) {
	return g.outages.since(after)
}

// outageEnded records a finished outage and tells the owner, so missed
// messages can be caught up promptly.
func (g *Google) outageEnded() {
	if g.outages.end() && g.Reconnected != nil {
		g.Reconnected()
	}
}
