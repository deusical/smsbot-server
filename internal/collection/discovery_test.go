package collection

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

// Turning collection on finds the chosen conversation at once, instead of at the
// next scheduled discovery up to 15 minutes later. A save that collects nothing
// new does not spend a phone read on discovery.
func TestCollectingANewConversationDiscoversItAtOnce(t *testing.T) {
	t.Parallel()
	svc, _ := setup(t)
	ctx := context.Background()
	// Startup's discovery happens before anything is collected.
	svc.History.Tick(ctx)
	if _, err := svc.Store.Archive("demo-chat"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("archived before collection was turned on", err)
	}
	s := enableInbox(t, svc)
	svc.History.Tick(ctx)
	if _, err := svc.Store.Archive("demo-chat"); err != nil {
		t.Fatal("not archived on the tick after collection was turned on:", err)
	}

	// Forget the record, then save a change that collects nothing new.
	if _, err := svc.Store.DB.Exec("DELETE FROM archive_conversations"); err != nil {
		t.Fatal(err)
	}
	s, _ = svc.Settings()
	s.ArchiveRetentionDays = 30
	if err := svc.SetSettings(ctx, s); err != nil {
		t.Fatal(err)
	}
	svc.History.Tick(ctx)
	if _, err := svc.Store.Archive("demo-chat"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("an unrelated settings save ran discovery early", err)
	}
}

func TestCollectsMore(t *testing.T) {
	t.Parallel()
	off := Settings{}
	all := Settings{Enabled: true, Scope: "all"}
	one := Settings{Enabled: true, Scope: "selected", Conversations: []Binding{{Conversation: "a", Participant: "p"}}}
	two := Settings{Enabled: true, Scope: "selected", Conversations: []Binding{{Conversation: "a", Participant: "p"}, {Conversation: "b", Participant: "q"}}}
	for _, tc := range []struct {
		name           string
		previous, next Settings
		want           bool
	}{
		{"turned on", off, one, true},
		{"widened to all", one, all, true},
		{"conversation added", one, two, true},
		{"conversation removed", two, one, false},
		{"narrowed from all", all, one, false},
		{"unchanged", one, one, false},
		{"turned off", one, off, false},
	} {
		if got := collectsMore(tc.previous, tc.next); got != tc.want {
			t.Errorf("%s: %t, want %t", tc.name, got, tc.want)
		}
	}
}
