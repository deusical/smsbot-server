package store

import (
	"strconv"
	"testing"
)

// changedOrigin returns the origin the change feed reports for id after cursor,
// or "" when the feed does not mention it.
func changedOrigin(t *testing.T, s *Store, cursor int64, id string) string {
	t.Helper()
	changes, err := s.Changes("fixture-chat", strconv.FormatInt(cursor, 10), "")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range changes.Messages {
		if m.ID == id {
			return m.Origin
		}
	}
	return ""
}

func latestChange(t *testing.T, s *Store) int64 {
	t.Helper()
	var n int64
	if err := s.DB.QueryRow("SELECT coalesce(max(seq),0) FROM message_changes").Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// A dashboard that already shows a message learns when who sent it or who may
// see it changes, not only when its delivery status does.
func TestMessageMetadataChangesReachTheChangeFeed(t *testing.T) {
	t.Parallel()
	s := inboxStore(t)
	// A submitted message is saved with its sender at once.
	if err := s.SaveSubmitted("synthetic-sent", "fixture-chat", "synthetic text", nil, "web", true); err != nil {
		t.Fatal(err)
	}
	if got := changedOrigin(t, s, 0, "synthetic-sent"); got != "web" {
		t.Fatalf("new message reported with origin %q", got)
	}

	cursor := latestChange(t, s)
	if err := s.SaveSubmitted("synthetic-sent", "fixture-chat", "synthetic text", nil, "bot", true); err != nil {
		t.Fatal(err)
	}
	if got := changedOrigin(t, s, cursor, "synthetic-sent"); got != "bot" {
		t.Fatalf("sender change reported as %q", got)
	}

	cursor = latestChange(t, s)
	if err := s.PublishCommandMessage("synthetic-sent"); err != nil {
		t.Fatal(err)
	}
	if latestChange(t, s) == cursor {
		t.Fatal("visibility change not on the change feed")
	}

	// Saving the same metadata again adds nothing.
	cursor = latestChange(t, s)
	if err := s.PublishCommandMessage("synthetic-sent"); err != nil {
		t.Fatal(err)
	}
	if latestChange(t, s) != cursor {
		t.Fatal("unchanged metadata added a change")
	}
}
