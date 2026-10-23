package store

import (
	"fmt"
	"github.com/deusical/smsbot-server/internal/vault"
	"path/filepath"
	"testing"
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
