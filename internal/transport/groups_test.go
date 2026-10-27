package transport

import (
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
	if !listed.Group || listed.ParticipantID != "group:g1" || listed.Writable || !listed.Collectable() || listed.Label != "Group conversation" {
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

func TestAGroupIsReadableButNotYetSendable(t *testing.T) {
	c := group("g1", "a", "b")
	if !readable(c) || eligible(c) {
		t.Fatalf("readable=%t eligible=%t", readable(c), eligible(c))
	}
	// A group changing under the bot's feet must not pause everything.
	g := NewGoogle(nil, "")
	g.cacheDetail(c)
	if g.rememberConversationEvent(group("g1", "a", "b", "c")) {
		t.Fatal("a member joining paused the bot")
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
