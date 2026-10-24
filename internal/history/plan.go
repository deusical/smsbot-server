package history

import (
	"context"
	"github.com/deusical/smsbot-server/internal/store"
	"github.com/deusical/smsbot-server/internal/transport"
	"sort"
	"time"
)

// Optional host contracts. A host implements only the ones it supports.
type (
	// GapRepairer repairs at most one capture-gap page per turn.
	GapRepairer interface {
		RepairCaptureGap(context.Context) error
	}
	// GapSchedule reports when the next capture-gap repair is eligible.
	GapSchedule interface {
		NextGapAttempt() (time.Time, bool)
	}
	// DiscoveryPager reports an unfinished paged discovery pass.
	DiscoveryPager interface {
		DiscoveryPending() bool
	}
	// CatchUpSource reports when live capture may have missed messages: after
	// a connection outage, or (token 0) while the service was not running.
	// It returns the earliest such time since token and the next token.
	CatchUpSource interface {
		MissedSince(token uint64) (since time.Time, next uint64, missed bool)
	}
)

// missedActivity reports whether the phone lists activity in c at or after
// since (allowing for the list's one-second resolution), so messages may
// have been missed there.
func missedActivity(c transport.Conversation, since time.Time) bool {
	if since.IsZero() {
		return false
	}
	at, err := time.Parse(time.RFC3339, c.LastMessageAt)
	return err == nil && !at.Before(since.Add(-2*time.Minute))
}

// catchUp asks the recent workflow to page back to shortly before since. An
// unfinished recent scan is extended instead of restarted.
func catchUp(c transport.Conversation, p Policy, since, now time.Time) Commit {
	from := since.Add(-30 * time.Minute)
	return Commit{Work: RecentWork, Conversation: c, Policy: p, Apply: func(r *store.Archive) {
		cp := &r.RecentCheckpoint
		if cp.RecentStarted == "" {
			cp.RecentStarted = now.UTC().Format(time.RFC3339Nano)
			cp.RecentSince = from.UTC().Format(time.RFC3339Nano)
			cp.RecentCursor = ""
			return
		}
		if current := parseTime(cp.RecentSince); current.IsZero() || from.Before(current) {
			cp.RecentSince = from.UTC().Format(time.RFC3339Nano)
		}
	}}
}

// pending reports whether a's checkpoint for w has work of its own, outside
// the routine recent sweep, and when that work is next eligible. This one
// definition drives both step selection and the worker's sleep.
func pending(w Work, a store.Archive) (time.Time, bool) {
	switch w {
	case ViewWork:
		return parseTime(a.RefreshRetryAt), activeState(a.RefreshState)
	case ImportWork:
		return parseTime(a.RetryAt), activeState(a.State)
	case RecentWork:
		return parseTime(a.RecentRetryAt), a.RecentStarted != "" || a.RecentRetryAt != ""
	}
	return time.Time{}, false
}

// dueAt reports whether w has pending work for a that is eligible at now.
func dueAt(now time.Time, w Work, a store.Archive) bool {
	at, ok := pending(w, a)
	return ok && !now.Before(at)
}

// selectPriority picks this turn's owner-driven steps from allowed records:
// the most recently viewed section, and the least recently attempted explicit
// import (ties by ID). Each gets its own bounded opportunity.
func selectPriority(now time.Time, archives []store.Archive, allowed func(store.Archive) bool) (view, owner *store.Archive) {
	for i := range archives {
		a := &archives[i]
		if !allowed(*a) {
			continue
		}
		if dueAt(now, ViewWork, *a) && (view == nil || a.FocusAt > view.FocusAt) {
			view = a
		}
		if isExplicitArchive(*a) && dueAt(now, ImportWork, *a) && (owner == nil || a.LastAttemptAt < owner.LastAttemptAt || (a.LastAttemptAt == owner.LastAttemptAt && a.Conversation.ID < owner.Conversation.ID)) {
			owner = a
		}
	}
	return view, owner
}

// rotate returns the first record after the cursor, in ID order and wrapping,
// that wants a step. Separate cursors keep the recent sweep and background
// imports from trapping one another in the same conversations.
func rotate(sorted []store.Archive, after string, wants func(store.Archive) bool) (store.Archive, bool) {
	start := sort.Search(len(sorted), func(i int) bool { return sorted[i].Conversation.ID > after })
	for n := 0; n < len(sorted); n++ {
		a := sorted[(start+n)%len(sorted)]
		if wants(a) {
			return a, true
		}
	}
	return store.Archive{}, false
}

// wantsRecent: an unfinished or retrying recent check, or any eligible record
// when the routine sweep is due.
func wantsRecent(now time.Time, a store.Archive, sweep bool) bool {
	at, ok := pending(RecentWork, a)
	return !now.Before(at) && (ok || sweep)
}

// wantsBackground: an automatic import with work due. Explicit imports are
// served by selectPriority instead.
func wantsBackground(now time.Time, a store.Archive) bool {
	return !isExplicitArchive(a) && dueAt(now, ImportWork, a)
}

// pendingDeadlines lists when each allowed record's pending work is next eligible.
func pendingDeadlines(archives []store.Archive, allowed func(store.Archive) bool) []time.Time {
	var out []time.Time
	for _, a := range archives {
		if !allowed(a) {
			continue
		}
		for _, w := range []Work{ViewWork, ImportWork, RecentWork} {
			if at, ok := pending(w, a); ok {
				out = append(out, at)
			}
		}
	}
	return out
}
