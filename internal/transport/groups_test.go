package transport

import (
	"context"
	"testing"
	"time"

	"go.mau.fi/mautrix-gmessages/pkg/libgm/gmproto"
)

func group(id string, people ...string) *gmproto.Conversation {
	c := &gmproto.Conversation{ConversationID: id, DefaultOutgoingID: "self", IsGroupChat: true}
	c.Participants = append(c.Participants, &gmproto.Participant{ID: &gmproto.SmallInfo{ParticipantID: "me"}, IsMe: true, IsVisible: true})
	for _, p := range people {
		c.Participants = append(c.Participants, &gmproto.Participant{ID: &gmproto.SmallInfo{ParticipantID: p}, FullName: "Name " + p, IsVisible: true})
	}
	return c
}

func TestAGroupHasAFixedIdentityAndANamedRoster(t *testing.T) {
	c := group("g1", "b", "a", "c")
	listed := listConversation(c)
	if !listed.Group || listed.ParticipantID != "group:g1" || !listed.Writable || !listed.Collectable() || listed.Label != "Group conversation" {
		t.Fatalf("group projection: %+v", listed)
	}
	if len(listed.Members) != 3 || listed.Members[0].ID != "a" || listed.Members[0].Name != "Name a" || listed.Roster != GroupRoster([]string{"c", "a", "b"}) {
		t.Fatalf("members/roster: %+v", listed)
	}
	// People joining or leaving change the roster, not the identity.
	joined := listConversation(group("g1", "a", "b", "c", "d"))
	left := listConversation(group("g1", "a", "b"))
	if joined.ParticipantID != listed.ParticipantID || left.ParticipantID != listed.ParticipantID || joined.Roster == listed.Roster || left.Roster == listed.Roster || joined.Roster == left.Roster {
		t.Fatalf("identity %q/%q roster %q/%q/%q", joined.ParticipantID, left.ParticipantID, listed.Roster, joined.Roster, left.Roster)
	}
	// Two groups are never the same, whoever is in them.
	if other := listConversation(group("g2", "a", "b", "c")); other.ParticipantID == listed.ParticipantID || other.Roster != listed.Roster {
		t.Fatalf("other group: %+v", other)
	}
	if detail := detailConversation("g1", c); detail.ParticipantID != "group:g1" || detail.Roster != listed.Roster || detail.Label != "Group conversation" {
		t.Fatalf("detail: %+v", detail)
	}
}

func TestGroupsThatCannotBeReadAreNotSelectable(t *testing.T) {
	left := group("g1", "a", "b")
	left.ReadOnly = true
	if got := listConversation(left); got.ParticipantID != "" || got.Collectable() {
		t.Fatalf("read-only group: %+v", got)
	}
	empty := group("g2")
	if got := listConversation(empty); got.ParticipantID != "" || got.Collectable() {
		t.Fatalf("group with nobody in it: %+v", got)
	}
	noID := group("g3", "a")
	noID.Participants[1].ID.ParticipantID = ""
	if got := listConversation(noID); got.ParticipantID != "" {
		t.Fatalf("group with an unidentifiable member: %+v", got)
	}
}

func TestAGroupIsSendableButNothingElseActsInIt(t *testing.T) {
	c := group("g1", "a", "b")
	// Typing, read marking and reactions stay with individual conversations.
	if !readable(c) || eligible(c) {
		t.Fatalf("readable=%t eligible=%t", readable(c), eligible(c))
	}
}

func TestPeopleJoiningAGroupKeepTheSendBaselineAndPauseNothing(t *testing.T) {
	c := group("g1", "a", "b")
	g := NewGoogle(nil, "")
	g.cacheDetail(c)
	if g.rememberConversationEvent(group("g1", "a", "b", "c")) {
		t.Fatal("a member joining paused the bot")
	}
	// The baseline a send compares against is still the group as verified.
	if roster(g.conversations["g1"]) != roster(c) {
		t.Fatal("an event moved the send baseline")
	}
	// A verified lookup replaces it.
	g.cacheDetail(group("g1", "a", "b", "c"))
	if roster(g.conversations["g1"]) == roster(c) {
		t.Fatal("a lookup did not refresh the baseline")
	}
}

func TestASendToAGroupNeedsTheRosterTheOwnerSaw(t *testing.T) {
	ctx := context.Background()
	if _, given := expectedRoster(ctx); given {
		t.Fatal("a roster out of nowhere")
	}
	if got, given := expectedRoster(WithRoster(ctx, "r")); !given || got != "r" {
		t.Fatalf("roster %q %t", got, given)
	}
	// The offline group refuses a send without it, with the wrong one, or after people changed.
	f := NewFixture()
	roster := f.demoGroup().Roster
	for name, tc := range map[string]struct {
		ctx context.Context
		ok  bool
	}{
		"none":    {ctx, false},
		"empty":   {WithRoster(ctx, ""), false},
		"wrong":   {WithRoster(ctx, "group:other"), false},
		"correct": {WithRoster(ctx, roster), true},
	} {
		_, err := f.Send(tc.ctx, "demo-group", "txn-"+name, "hi")
		if (err == nil) != tc.ok {
			t.Errorf("%s: %v", name, err)
		}
	}
	f.SetGroupMembers("demo-member-a", "demo-member-b", "demo-member-c")
	if _, err := f.Send(WithRoster(ctx, roster), "demo-group", "txn-late", "hi"); err == nil {
		t.Fatal("a send went to people the owner never saw")
	}
	if f.demoGroup().Roster == roster {
		t.Fatal("the roster did not change when people joined")
	}
}

func TestGroupMessagesRecordWhoSentThem(t *testing.T) {
	m := &gmproto.Message{MessageID: "m1", ParticipantID: "b"}
	if v := conversationSnapshot(group("g1", "a", "b"), m, time.Now()); v.Sender != "b" {
		t.Fatalf("sender %q", v.Sender)
	}
	// Individual conversations keep their records unchanged.
	if v := conversationSnapshot(individual("chat"), m, time.Now()); v.Sender != "" {
		t.Fatalf("sender %q in an individual conversation", v.Sender)
	}
}
