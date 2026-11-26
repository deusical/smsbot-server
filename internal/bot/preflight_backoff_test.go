package bot

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/deusical/smsbot-server/internal/collection"
	"github.com/deusical/smsbot-server/internal/store"
	"github.com/deusical/smsbot-server/internal/transport"
	"github.com/deusical/smsbot-server/internal/vault"
)

func TestPreflightBackoffSkipsBlockedConversationAndPreservesOrdering(t *testing.T) {
	t.Parallel()
	v, _ := vault.New(make([]byte, 32))
	s, err := store.Open(filepath.Join(t.TempDir(), "test.db"), v)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	f := transport.NewFixture()
	control := new(sync.Mutex)
	c := collection.New(s, f, control)
	e := New(s, f, c, control)

	now := time.Now()
	e.now = func() time.Time { return now }

	_ = s.Pause(false)
	_ = s.Enroll("demo-chat", "demo-peer")
	_ = s.Enroll("demo-sms", "demo-sms-peer")

	// Enqueue Job 1 for demo-chat
	_, err = s.Enqueue("job-a1", "demo-chat", "!ping", store.Plan{Participant: "demo-peer", Manual: true})
	if err != nil {
		t.Fatal(err)
	}
	// Enqueue Job 2 for demo-sms
	_, err = s.Enqueue("job-b1", "demo-sms", "!ping", store.Plan{Participant: "demo-sms-peer", Manual: true})
	if err != nil {
		t.Fatal(err)
	}
	// Enqueue Job 3 for demo-chat (later than job-a1)
	_, err = s.Enqueue("job-a2", "demo-chat", "!ping", store.Plan{Participant: "demo-peer", Manual: true})
	if err != nil {
		t.Fatal(err)
	}

	// Back off job-a1 in demo-chat
	e.backoffPreflight("job-a1", "demo-chat")

	// nextReady should skip demo-chat (both job-a1 and job-a2) and pick job-b1 from demo-sms!
	j, _, _, _, ready, err := e.nextReady(context.Background())
	if err != nil {
		t.Fatalf("nextReady error: %v", err)
	}
	if !ready {
		t.Fatal("expected nextReady to be ready with job-b1")
	}
	if j.ID != "job-b1" || j.ConversationID != "demo-sms" {
		t.Fatalf("expected job-b1, got %s in %s", j.ID, j.ConversationID)
	}

	// Mark job-b1 completed, like Dispatch does
	_ = s.Complete(j.ID, "submitted", "")

	// Now only demo-chat jobs remain (job-a1 and job-a2). demo-chat is still backed off.
	_, _, _, _, ready, err = e.nextReady(context.Background())
	if err != nil {
		t.Fatalf("nextReady error: %v", err)
	}
	if ready {
		t.Fatal("expected no ready job while demo-chat is backed off")
	}

	// Advance time past backoff duration (30 seconds)
	now = now.Add(35 * time.Second)

	// Now job-a1 should be picked (preserving order: job-a1 before job-a2)
	j, _, _, _, ready, err = e.nextReady(context.Background())
	if err != nil {
		t.Fatalf("nextReady error: %v", err)
	}
	if !ready {
		t.Fatal("expected job-a1 to be ready after backoff expired")
	}
	if j.ID != "job-a1" {
		t.Fatalf("expected job-a1, got %s", j.ID)
	}
}
