package store

import (
	"path/filepath"
	"testing"

	"github.com/deusical/smsbot-server/internal/vault"
)

func TestCompleteWithReasonKeepsOnlyFixedCodes(t *testing.T) {
	v, _ := vault.New(make([]byte, 32))
	s, err := Open(filepath.Join(t.TempDir(), "test.db"), v)
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	for _, id := range []string{"job-sim", "job-free-text", "job-none"} {
		if ok, err := s.Enqueue(id, "fixture-chat", "!manual", Plan{}); err != nil || !ok {
			t.Fatal("enqueue", err)
		}
	}
	if err = s.CompleteWithReason("job-sim", "failed", "not_submitted", "sim_missing"); err != nil {
		t.Fatal(err)
	}
	if err = s.CompleteWithReason("job-free-text", "failed", "not_submitted", "Upstream said: synthetic private detail"); err != nil {
		t.Fatal(err)
	}
	if err = s.CompleteWithReason("job-none", "failed", "not_submitted", ""); err != nil {
		t.Fatal(err)
	}
	jobs, err := s.Jobs()
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]Job{}
	for _, j := range jobs {
		got[j.ID] = j
	}
	if j := got["job-sim"]; j.State != "failed" || j.Code != "not_submitted" || j.Reason != "sim_missing" {
		t.Fatalf("reason not kept: %+v", j)
	}
	if j := got["job-free-text"]; j.State != "failed" || j.Reason != "" {
		t.Fatalf("free text stored as a reason: %+v", j)
	}
	if j := got["job-none"]; j.State != "failed" || j.Reason != "" {
		t.Fatalf("empty reason invented: %+v", j)
	}
}
