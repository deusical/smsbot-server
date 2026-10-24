package media

import (
	"encoding/json"
	"testing"
	"time"
)

func TestOriginalFailureCooldownAndLegacyDeadlines(t *testing.T) {
	now := time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		failures int
		delay    time.Duration
	}{
		{1, 10 * time.Second}, {8, 1280 * time.Second}, {9, 2560 * time.Second},
		{14, 81920 * time.Second}, {15, 24 * time.Hour}, {180, 24 * time.Hour},
	} {
		if got := OriginalRetryDelay(tc.failures); got != tc.delay {
			t.Fatalf("failures=%d delay=%v want=%v", tc.failures, got, tc.delay)
		}
	}
	legacy := File{Info: Info{ArchiveState: "failed"}, Failures: 180, RetryAt: now.Add(1280 * time.Second).Format(time.RFC3339Nano)}
	if !legacy.OriginalRetryAt().Equal(now.Add(24 * time.Hour)) {
		t.Fatal("legacy failures still use the old capped deadline")
	}
	modern := File{Info: Info{ArchiveState: "failed"}, Failures: 179}
	modern.BackoffOriginal(now)
	if modern.RetryBackoffVersion != 1 || !modern.OriginalRetryAt().Equal(now.Add(24*time.Hour)) {
		t.Fatal("new backoff was extended twice")
	}
	encoded, err := json.Marshal(modern)
	var restored File
	if err != nil || json.Unmarshal(encoded, &restored) != nil || !restored.OriginalRetryAt().Equal(now.Add(24*time.Hour)) {
		t.Fatal("persisted retry schedule changed after reload")
	}
	if (File{Failures: 180}).OriginalRetryAt() != (time.Time{}) {
		t.Fatal("missing retry timestamp blocked a source")
	}
}
