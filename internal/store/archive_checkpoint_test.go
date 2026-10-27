package store

import (
	"encoding/json"
	"errors"
	"github.com/deusical/smsbot-server/internal/transport"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/deusical/smsbot-server/internal/vault"
)

func newCheckpointStore(t *testing.T) *Store {
	v, _ := vault.New(make([]byte, 32))
	s, err := Open(filepath.Join(t.TempDir(), "state.db"), v)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.DB.Close() })
	return s
}

// The checkpoint split is internal: stored and API JSON keep the flat keys
// written before it, plus only the new optional view generation.
func TestArchiveJSONKeepsFlatLegacyKeys(t *testing.T) {
	b, err := json.Marshal(Archive{})
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err = json.Unmarshal(b, &fields); err != nil {
		t.Fatal(err)
	}
	got := []string{}
	for k := range fields {
		got = append(got, k)
	}
	sort.Strings(got)
	// Keys without omitempty, as serialized by the flat record before the split.
	want := "checked,conversation,cursor,duplicates,failures,generation,imported,last_sync,oldest,pages,recent_cursor,recent_failures,recent_since,recent_started,since,skipped,state,updated"
	if strings.Join(got, ",") != want {
		t.Fatalf("archive JSON keys changed:\n got %s\nwant %s", strings.Join(got, ","), want)
	}
	legacy := `{"scan_revision":4,"request_source":"owner","requested_at":"r","checked":3,"pages":2,"updated":1,"refresh_since":"a","refresh_until":"b","refresh_cursor":"c","refresh_state":"running","focus_at":"f","recent_retry_at":"x","recent_failures":1,"recent_cursor":"rc","recent_since":"rs","recent_started":"st","retry_at":"y","failures":2,"duplicates":5,"skipped":6,"conversation":{"id":"chat"},"cursor":"cur","since":"s","state":"running","imported":7,"oldest":"o","last_sync":"l","generation":9}`
	var a Archive
	if err = json.Unmarshal([]byte(legacy), &a); err != nil {
		t.Fatal(err)
	}
	if a.ScanRevision != 4 || a.Cursor != "cur" || a.RecentCursor != "rc" || a.RefreshCursor != "c" || a.FocusAt != "f" || a.Skipped != 6 || a.Generation != 9 || a.ViewGeneration != 0 || a.LastSync != "l" || a.Imported != 7 {
		t.Fatalf("legacy payload lost fields: %+v", a)
	}
	again, _ := json.Marshal(a)
	var round Archive
	if err = json.Unmarshal(again, &round); err != nil || !reflect.DeepEqual(round, a) {
		t.Fatal("legacy payload did not round-trip", err)
	}
}

func TestUpdateArchiveIsScopedAndAtomic(t *testing.T) {
	s := newCheckpointStore(t)
	c := transport.Conversation{ID: "chat", ParticipantID: "peer", Writable: true}
	if _, err := s.UpdateArchive("chat", 0, func(a *Archive, exists bool) error {
		if exists {
			t.Fatal("phantom record")
		}
		return ErrArchiveMissing
	}); !errors.Is(err, ErrArchiveMissing) {
		t.Fatal("missing record error not returned", err)
	}
	if _, err := s.Archive("chat"); err == nil {
		t.Fatal("failed update created a record")
	}
	if _, err := s.UpdateArchive("chat", 0, func(a *Archive, exists bool) error { *a = NewArchive(c); return nil }); err != nil {
		t.Fatal(err)
	}
	// Each workflow writes only its own checkpoint onto the current record.
	if _, err := s.UpdateArchive("chat", 0, func(a *Archive, _ bool) error { a.RecentCursor = "recent"; return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateArchive("chat", 0, func(a *Archive, _ bool) error { a.Cursor = "import"; a.RefreshState = "running"; return nil }); err != nil {
		t.Fatal(err)
	}
	a, err := s.Archive("chat")
	if err != nil || a.RecentCursor != "recent" || a.Cursor != "import" || a.RefreshState != "running" || a.State != "queued" {
		t.Fatalf("checkpoint update lost another workflow's progress: %+v %v", a, err)
	}
	if _, err = s.UpdateArchive("chat", 0, func(a *Archive, _ bool) error { a.Cursor = "lost"; return errors.New("synthetic") }); err == nil {
		t.Fatal("failed update reported success")
	}
	if _, err = s.UpdateArchive("chat", 0, func(a *Archive, _ bool) error { a.Conversation.ID = "other"; return nil }); err == nil {
		t.Fatal("update re-keyed a record")
	}
	if a, _ = s.Archive("chat"); a.Cursor != "import" {
		t.Fatal("failed update changed the record")
	}
}
