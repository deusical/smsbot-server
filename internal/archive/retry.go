package archive

import (
	"github.com/deusical/smsbot-server/internal/media"
	"time"
)

// Original-file retry policy. Transient failures stay responsive; sustained
// failures back off to at most one attempt a day instead of waking the phone
// every 21 minutes forever. The descriptor fields live in media.File; their
// meaning is defined only here.

// RetryDelay is the wait after the given number of consecutive failures.
func RetryDelay(failures int) time.Duration {
	return min(time.Duration(1<<min(max(failures, 0), 15))*5*time.Second, 24*time.Hour)
}

// Backoff records one more failure and schedules the next attempt.
func Backoff(f *media.File, now time.Time) {
	f.Failures++
	f.RetryAt = now.Add(RetryDelay(f.Failures)).UTC().Format(time.RFC3339Nano)
	f.RetryBackoffVersion = 1
}

// RetryAt is when the file may next cause phone work. Deadlines saved before
// the daily cooldown (schedule version 0) adopt it locally, without a final
// phone attempt or a metadata rewrite. No deadline means eligible now.
func RetryAt(f media.File) time.Time {
	at, err := time.Parse(time.RFC3339Nano, f.RetryAt)
	if err != nil {
		return time.Time{}
	}
	if f.RetryBackoffVersion == 0 && f.ArchiveState == "failed" && f.Failures > 8 {
		at = at.Add(RetryDelay(f.Failures) - 1280*time.Second)
	}
	return at
}
