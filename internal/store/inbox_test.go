package store

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/deusical/smsbot-server/internal/vault"
)

func inboxStore(t *testing.T) *Store {
	t.Helper()
	v, e := vault.New(make([]byte, 32))
	if e != nil {
		t.Fatal(e)
	}
	s, e := Open(filepath.Join(t.TempDir(), "test.db"), v)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.DB.Close() })
	return s
}
func TestMessagePageStableCursorAndBoundedSearch(t *testing.T) {
	t.Parallel()
	s := inboxStore(t)
	for i := 0; i < 5; i++ {
		id := fmt.Sprintf("fixture-%d", i)
		if e := s.SaveMessage(id, "fixture-chat", "incoming", fmt.Sprintf("synthetic %d", i)); e != nil {
			t.Fatal(e)
		}
	}
	if _, e := s.DB.Exec("UPDATE messages SET at='2026-01-01T00:00:00.000000000Z'"); e != nil {
		t.Fatal(e)
	}
	first, e := s.MessagePage("fixture-chat", "", "", 2)
	if e != nil || len(first.Messages) != 2 || first.Messages[0].ID != "fixture-4" || first.NextCursor == "" {
		t.Fatalf("first: %+v %v", first, e)
	}
	next, e := s.MessagePage("fixture-chat", first.NextCursor, "synthetic 0", 2)
	if e != nil || len(next.Messages) != 0 || next.NextCursor == "" {
		t.Fatalf("bounded search: %+v %v", next, e)
	}
	last, e := s.MessagePage("fixture-chat", next.NextCursor, "synthetic 0", 2)
	if e != nil || len(last.Messages) != 1 || last.NextCursor != "" {
		t.Fatalf("last: %+v %v", last, e)
	}
	if _, e = s.MessagePage("fixture-chat", "bad", "", 2); e == nil {
		t.Fatal("bad cursor accepted")
	}
	if _, e = s.MessagePage("fixture-chat", "", "", 101); e == nil {
		t.Fatal("unbounded page accepted")
	}
	other, e := s.MessagePage("other-chat", "", "", 2)
	if e != nil || len(other.Messages) != 0 {
		t.Fatal("conversation leaked")
	}
}
func TestRetentionPreservesDedupAndUncertainMarkers(t *testing.T) {
	t.Parallel()
	s := inboxStore(t)
	if e := s.SaveMessage("fixture-old", "fixture-chat", "incoming", "synthetic old"); e != nil {
		t.Fatal(e)
	}
	if _, e := s.Enqueue("fixture-job", "fixture-chat", "ping", Plan{Reply: "synthetic", Participant: "fixture-peer"}); e != nil {
		t.Fatal(e)
	}
	if e := s.Submitting("fixture-job", "fixture-txn"); e != nil {
		t.Fatal(e)
	}
	if e := s.Recover(); e != nil {
		t.Fatal(e)
	}
	if _, e := s.DB.Exec("UPDATE messages SET at='2000-01-01T00:00:00Z'; UPDATE jobs SET created='2000-01-01T00:00:00Z'"); e != nil {
		t.Fatal(e)
	}
	n, e := s.Retention(30)
	if e != nil || n != 1 {
		t.Fatalf("retention: %d %v", n, e)
	}
	j, e := s.Jobs()
	if e != nil || len(j) != 1 || j[0].State != "unknown_submission" || j[0].TransactionID != "fixture-txn" {
		t.Fatalf("evidence lost: %+v %v", j, e)
	}
	added, e := s.Enqueue("fixture-job", "fixture-chat", "ping", Plan{})
	if e != nil || added {
		t.Fatal("dedup lost")
	}
}

func TestRetentionSkipsAlreadyClearedJobPlans(t *testing.T) {
	t.Parallel()
	s := inboxStore(t)
	if _, err := s.Enqueue("job-cleared", "fixture-chat", "ping", Plan{Reply: "a", Participant: "fixture-peer"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Complete("job-cleared", "submitted", "ok"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Enqueue("job-stale-plan", "fixture-chat", "ping", Plan{Reply: "b", Participant: "fixture-peer"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Enqueue("job-active", "fixture-chat", "ping", Plan{Reply: "c", Participant: "fixture-peer"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(`
		UPDATE jobs SET created='2000-01-01T00:00:00Z' WHERE id='job-cleared';
		UPDATE jobs SET state='failed',created='2000-01-01T00:00:00Z' WHERE id='job-stale-plan';
		UPDATE jobs SET created='2000-01-01T00:00:00Z' WHERE id='job-active';
		CREATE TEMP TABLE job_updates(n INTEGER NOT NULL);
		CREATE TEMP TRIGGER count_job_updates AFTER UPDATE ON jobs BEGIN INSERT INTO job_updates VALUES(1); END;
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Retention(30); err != nil {
		t.Fatal(err)
	}
	var updates int
	if err := s.DB.QueryRow("SELECT count(*) FROM job_updates").Scan(&updates); err != nil || updates != 1 {
		t.Fatalf("first retention updated %d job rows, want 1 (err=%v)", updates, err)
	}
	var stalePlan, activePlan string
	if err := s.DB.QueryRow("SELECT plan FROM jobs WHERE id='job-stale-plan'").Scan(&stalePlan); err != nil || stalePlan != "" {
		t.Fatalf("stale plan not cleared: %q %v", stalePlan, err)
	}
	if err := s.DB.QueryRow("SELECT plan FROM jobs WHERE id='job-active'").Scan(&activePlan); err != nil || activePlan == "" {
		t.Fatalf("active queued plan cleared: %v", err)
	}
	if _, err := s.Retention(30); err != nil {
		t.Fatal(err)
	}
	if err := s.DB.QueryRow("SELECT count(*) FROM job_updates").Scan(&updates); err != nil || updates != 1 {
		t.Fatalf("second retention rewrote empty-plan rows: total updates=%d, want 1 (err=%v)", updates, err)
	}
}

func TestRetentionPruningSessionsChangesObservedEventsNoJobsDeletion(t *testing.T) {
	t.Parallel()
	s := inboxStore(t)

	// 1. Sessions in credentials table
	now := time.Now().Unix()
	if _, err := s.DB.Exec("INSERT INTO credentials VALUES('hash-expired', 'expired', 'session', ?)", now-100); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec("INSERT INTO credentials VALUES('hash-valid', 'valid', 'session', ?)", now+1000); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec("INSERT INTO credentials VALUES('hash-remembered', 'remembered', 'session', 0)"); err != nil {
		t.Fatal(err)
	}

	// 2. messages and observed_events
	if _, err := s.DB.Exec("INSERT INTO archive_conversations VALUES('archived-chat', 'payload', 0)"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec("INSERT INTO messages VALUES('msg-1', 'archived-chat', 'incoming', '2026-01-01T00:00:00Z', 'enc')"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec("INSERT INTO message_alias VALUES('event-referenced', 'msg-1')"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec("INSERT INTO observed_events VALUES('event-orphan')"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec("INSERT INTO observed_events VALUES('event-referenced')"); err != nil {
		t.Fatal(err)
	}

	// 3. message_changes
	if _, err := s.DB.Exec("INSERT INTO message_changes(conversation, message) VALUES('unarchived-chat', 'msg-unarchived')"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec("INSERT INTO message_changes(conversation, message) VALUES('archived-chat', 'msg-1')"); err != nil {
		t.Fatal(err)
	}

	// 4. Jobs: verify jobs table is NOT pruned (only plan cleared for old non-active jobs)
	if _, err := s.Enqueue("job-keep", "archived-chat", "ping", Plan{Reply: "hello", Participant: "p1"}); err != nil {
		t.Fatal(err)
	}

	// Run retention
	if _, err := s.Retention(30); err != nil {
		t.Fatalf("Retention: %v", err)
	}

	// Verify expired session pruned, valid and remembered kept
	var count int
	s.DB.QueryRow("SELECT count(*) FROM credentials WHERE hash='hash-expired'").Scan(&count)
	if count != 0 {
		t.Fatal("expired session not pruned")
	}
	s.DB.QueryRow("SELECT count(*) FROM credentials WHERE hash='hash-valid'").Scan(&count)
	if count != 1 {
		t.Fatal("valid session pruned")
	}
	s.DB.QueryRow("SELECT count(*) FROM credentials WHERE hash='hash-remembered'").Scan(&count)
	if count != 1 {
		t.Fatal("remembered session pruned")
	}

	// Verify orphan observed_event pruned, referenced kept
	s.DB.QueryRow("SELECT count(*) FROM observed_events WHERE id='event-orphan'").Scan(&count)
	if count != 0 {
		t.Fatal("orphan observed_event not pruned")
	}
	s.DB.QueryRow("SELECT count(*) FROM observed_events WHERE id='event-referenced'").Scan(&count)
	if count != 1 {
		t.Fatal("referenced observed_event pruned")
	}

	// Verify unarchived message_changes pruned
	s.DB.QueryRow("SELECT count(*) FROM message_changes WHERE conversation='unarchived-chat'").Scan(&count)
	if count != 0 {
		t.Fatalf("unarchived message_changes not pruned: count=%d", count)
	}
	s.DB.QueryRow("SELECT count(*) FROM message_changes WHERE conversation='archived-chat'").Scan(&count)
	if count == 0 {
		t.Fatalf("archived message_changes pruned: count=%d", count)
	}

	// Verify jobs table is NOT pruned
	s.DB.QueryRow("SELECT count(*) FROM jobs WHERE id='job-keep'").Scan(&count)
	if count != 1 {
		t.Fatal("jobs row was deleted")
	}
}
