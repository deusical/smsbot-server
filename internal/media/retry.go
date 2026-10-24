package media

import "time"

// OriginalRetryDelay keeps transient failures responsive, but sustained failures
// eventually retry daily instead of waking the phone every 21 minutes forever.
func OriginalRetryDelay(failures int) time.Duration {
	return min(time.Duration(1<<min(max(failures, 0), 15))*5*time.Second, 24*time.Hour)
}

func (f *File) BackoffOriginal(now time.Time) {
	f.Failures++
	f.RetryAt = now.Add(OriginalRetryDelay(f.Failures)).UTC().Format(time.RFC3339Nano)
	f.RetryBackoffVersion = 1
}

// OriginalRetryAt also applies the longer cooldown to already persisted failures.
// No phone request or metadata rewrite is needed to upgrade a legacy deadline.
func (f File) OriginalRetryAt() time.Time {
	at, err := time.Parse(time.RFC3339Nano, f.RetryAt)
	if err != nil {
		return time.Time{}
	}
	if f.RetryBackoffVersion == 0 && f.ArchiveState == "failed" && f.Failures > 8 {
		at = at.Add(OriginalRetryDelay(f.Failures) - 1280*time.Second)
	}
	return at
}
