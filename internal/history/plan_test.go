package history

import (
	"github.com/deusical/smsbot-server/internal/store"
	"github.com/deusical/smsbot-server/internal/transport"
	"testing"
	"time"
)

func planArchive(id string) store.Archive {
	return store.Archive{Conversation: transport.Conversation{ID: id, ParticipantID: "peer", Writable: true}, Generation: 1}
}

func TestPendingWorkDefinesSelectionAndWake(t *testing.T) {
	now := time.Now()
	later := now.Add(time.Minute).UTC().Format(time.RFC3339Nano)
	idle := planArchive("idle")
	idle.State = "available_pages_exhausted"
	if _, ok := pending(ImportWork, idle); ok || dueAt(now, RecentWork, idle) {
		t.Fatal("finished record has pending work")
	}
	backingOff := planArchive("backoff")
	backingOff.State, backingOff.RetryAt = "retrying", later
	if at, ok := pending(ImportWork, backingOff); !ok || at.IsZero() || dueAt(now, ImportWork, backingOff) {
		t.Fatal("retry deadline not honored")
	}
	// The routine sweep reaches an idle record only when it is due, and never
	// inside another record's retry window.
	if wantsRecent(now, idle, false) || !wantsRecent(now, idle, true) {
		t.Fatal("sweep eligibility wrong")
	}
	retrying := planArchive("recent")
	retrying.RecentRetryAt = later
	if wantsRecent(now, retrying, true) {
		t.Fatal("sweep bypassed a recent retry deadline")
	}
	all := func(store.Archive) bool { return true }
	if got := pendingDeadlines([]store.Archive{idle, backingOff, retrying}, all); len(got) != 2 {
		t.Fatalf("want two pending deadlines, got %d", len(got))
	}
	if got := pendingDeadlines([]store.Archive{backingOff}, func(store.Archive) bool { return false }); len(got) != 0 {
		t.Fatal("disallowed record woke the worker")
	}
}

func TestSelectPriorityPrefersRecentFocusAndOldestAttempt(t *testing.T) {
	now := time.Now()
	view := func(id, focus string) store.Archive {
		a := planArchive(id)
		a.RefreshState, a.FocusAt = "queued", focus
		return a
	}
	owner := func(id, attempt string) store.Archive {
		a := planArchive(id)
		a.State, a.RequestedAt, a.LastAttemptAt = "running", "requested", attempt
		return a
	}
	list := []store.Archive{view("v1", "2"), view("v2", "3"), owner("o2", "1"), owner("o1", "1"), owner("o3", "0")}
	v, o := selectPriority(now, list, func(a store.Archive) bool { return a.Conversation.ID != "o3" })
	if v == nil || v.Conversation.ID != "v2" || o == nil || o.Conversation.ID != "o1" {
		t.Fatal("wrong priority selection")
	}
	if !isExplicitArchive(*o) || wantsBackground(now, *o) {
		t.Fatal("explicit import also offered to the background rotation")
	}
}

func TestRotateWrapsAfterCursor(t *testing.T) {
	sorted := []store.Archive{planArchive("a"), planArchive("b"), planArchive("c")}
	any := func(store.Archive) bool { return true }
	if a, ok := rotate(sorted, "b", any); !ok || a.Conversation.ID != "c" {
		t.Fatal("did not continue after cursor")
	}
	if a, ok := rotate(sorted, "c", any); !ok || a.Conversation.ID != "a" {
		t.Fatal("did not wrap")
	}
	if _, ok := rotate(sorted, "", func(store.Archive) bool { return false }); ok {
		t.Fatal("selected an unwanted record")
	}
}
