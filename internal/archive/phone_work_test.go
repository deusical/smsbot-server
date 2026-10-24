package archive

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/deusical/smsbot-server/internal/media"
	"github.com/deusical/smsbot-server/internal/store"
)

func TestPolicyBlockSurvivesObservationsAndWorkerRestarts(t *testing.T) {
	for _, unknownSize := range []bool{false, true} {
		t.Run(map[bool]string{false: "reported-size", true: "unknown-size"}[unknownSize], func(t *testing.T) {
			s, manager := setup(t)
			policy := Policy{Enabled: true, QuotaMiB: 1}
			if err := manager.SetPolicy(policy); err != nil {
				t.Fatal(err)
			}
			file := media.File{Info: media.Info{ID: "part", Size: 2 << 20}, RemoteID: "source", Key: make([]byte, 32)}
			if unknownSize {
				file.Size = 0
				file.SizeUnverified = true
			}
			seed(t, s, []media.File{file})
			host := &repairCountingHost{fileHost: &fileHost{s: s, data: make([]byte, 2<<20)}}
			manager.Host = host
			manager.Tick(context.Background())
			current, err := s.Attachment("chat", store.Hash("chat\x00message"), file.ID)
			if err != nil || current.ArchiveState != "policy_limited" || current.RetryPolicy == "" {
				t.Fatal("attempt did not retain policy block", err)
			}
			if host.opens != map[bool]int{false: 0, true: 1}[unknownSize] {
				t.Fatal("unexpected initial download count", host.opens)
			}
			attempts, repairs, opens := host.authorizations, host.repairs, host.opens
			for range 3 {
				if err := s.SaveObservedFiles("chat", "message", "", "incoming", "Synthetic update", "phone", "read", time.Now(), []media.File{file}); err != nil {
					t.Fatal(err)
				}
				manager = New(s, manager.Root, host)
				manager.Tick(context.Background())
				manager.Tick(context.Background())
			}
			// Growing disk usage must not wake a blocked source either.
			blocker, _ := manager.blobPath(store.ID())
			if err := os.MkdirAll(filepath.Dir(blocker), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(blocker, make([]byte, 1024), 0600); err != nil {
				t.Fatal(err)
			}
			manager.lastGC = time.Now()
			manager.cursor, manager.nextScan = "", time.Time{}
			manager.Tick(context.Background())
			if host.authorizations != attempts || host.repairs != repairs || host.opens != opens {
				t.Fatal("unchanged blocked file performed phone work")
			}
			// A relevant relaxation permits the full original again.
			policy.QuotaMiB = 4
			if err := manager.SetPolicy(policy); err != nil {
				t.Fatal(err)
			}
			manager.cursor, manager.nextScan = "", time.Time{}
			manager.Tick(context.Background())
			current, err = s.Attachment("chat", store.Hash("chat\x00message"), file.ID)
			if err != nil || current.BlobID == "" || current.Size != 2<<20 {
				t.Fatal("relaxed quota did not resume original preservation", err)
			}
		})
	}
}

func TestPolicyRetryRequiresImprovedEffectiveCapacity(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		before, after         Policy
		usedBefore, usedAfter int64
		want                  bool
	}{
		{"unchanged", Policy{QuotaMiB: 2}, Policy{QuotaMiB: 2}, 100, 100, false},
		{"usage-grew", Policy{QuotaMiB: 2}, Policy{QuotaMiB: 2}, 100, 200, false},
		{"space-freed", Policy{QuotaMiB: 2}, Policy{QuotaMiB: 2}, 200, 100, true},
		{"quota-tightened", Policy{QuotaMiB: 2}, Policy{QuotaMiB: 1}, 0, 0, false},
		{"file-limit-tightened", Policy{MaxFileMiB: 2}, Policy{MaxFileMiB: 1}, 0, 0, false},
		{"irrelevant-quota-increase", Policy{MaxFileMiB: 1, QuotaMiB: 4}, Policy{MaxFileMiB: 1, QuotaMiB: 8}, 0, 0, false},
		{"irrelevant-file-increase", Policy{MaxFileMiB: 4, QuotaMiB: 1}, Policy{MaxFileMiB: 8, QuotaMiB: 1}, 0, 0, false},
		{"unlimited-usage", Policy{}, Policy{}, 100, 0, false},
		{"limit-removed", Policy{MaxFileMiB: 1}, Policy{}, 0, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := policyCanRetry(policyToken(tc.before, tc.usedBefore), tc.after, tc.usedAfter); got != tc.want {
				t.Fatalf("retry=%v, want %v", got, tc.want)
			}
		})
	}
}

func TestChangedBlockedSourceAndSmallerFilesRemainEligible(t *testing.T) {
	s, manager := setup(t)
	policy := Policy{Enabled: true, QuotaMiB: 1}
	if err := manager.SetPolicy(policy); err != nil {
		t.Fatal(err)
	}
	file := media.File{Info: media.Info{ID: "part", Size: 2 << 20}, RemoteID: "source"}
	seed(t, s, []media.File{file})
	host := &fileHost{s: s, data: []byte("small")}
	manager.Host = host
	manager.Tick(context.Background())
	file.RemoteID, file.Size = "replacement", 5
	if err := s.SaveObservedFiles("chat", "message", "", "incoming", "Synthetic update", "phone", "read", time.Now(), []media.File{file}); err != nil {
		t.Fatal(err)
	}
	manager.cursor, manager.nextScan = "", time.Time{}
	manager.Tick(context.Background())
	current, err := s.Attachment("chat", store.Hash("chat\x00message"), file.ID)
	if err != nil || current.BlobID == "" || host.opens != 1 {
		t.Fatal("smaller changed source did not resume preservation", err)
	}
}

func TestLegacyRepeatedFailuresSkipPhoneUntilCooldownOrSourceChange(t *testing.T) {
	s, manager := setup(t)
	file := media.File{Info: media.Info{ID: "part", ArchiveState: "failed"}, ActionID: "action", MessageID: "upstream", Failures: 18, RetryAt: time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano)}
	seed(t, s, []media.File{file})
	host := &repairCountingHost{fileHost: &fileHost{s: s, data: []byte("small")}}
	for range 3 {
		manager = New(s, manager.Root, host)
		manager.Tick(context.Background())
	}
	if host.repairs != 0 || host.authorizations != 0 || host.opens != 0 {
		t.Fatal("legacy repeated failure performed phone work")
	}
	file.RemoteID, file.RetryAt, file.Failures = "newly-uploaded-original", "", 0
	file.ArchiveState = ""
	if err := s.SaveObservedFiles("chat", "message", "", "incoming", "Synthetic update", "phone", "read", time.Now(), []media.File{file}); err != nil {
		t.Fatal(err)
	}
	manager.cursor, manager.nextScan = "", time.Time{}
	manager.Tick(context.Background())
	current, err := s.Attachment("chat", store.Hash("chat\x00message"), file.ID)
	if err != nil || current.BlobID == "" {
		t.Fatal("changed source did not bypass old failure cooldown", err)
	}
}

func TestObservedModernFailureKeepsRetryVersionAndEventuallyRetries(t *testing.T) {
	s, manager := setup(t)
	file := media.File{Info: media.Info{ID: "part", Size: 5, ArchiveState: "failed"}, RemoteID: "source", Failures: 14}
	file.BackoffOriginal(time.Now().Add(-25 * time.Hour))
	seed(t, s, []media.File{file})
	observed := file
	observed.RetryAt, observed.ArchiveState, observed.Failures, observed.RetryBackoffVersion = "", "", 0, 0
	if err := s.SaveObservedFiles("chat", "message", "", "incoming", "Synthetic update", "phone", "read", time.Now(), []media.File{observed}); err != nil {
		t.Fatal(err)
	}
	current, err := s.Attachment("chat", store.Hash("chat\x00message"), file.ID)
	if err != nil || current.RetryBackoffVersion != 1 {
		t.Fatal("observation erased retry schedule version", err)
	}
	host := &fileHost{s: s, data: []byte("small")}
	manager.Host = host
	manager.Tick(context.Background())
	current, err = s.Attachment("chat", store.Hash("chat\x00message"), file.ID)
	if err != nil || current.BlobID == "" || host.opens != 1 {
		t.Fatal("expired modern cooldown did not retry", err)
	}
}
