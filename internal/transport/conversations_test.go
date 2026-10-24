package transport

import (
	"testing"

	"go.mau.fi/mautrix-gmessages/pkg/libgm/gmproto"
	"google.golang.org/protobuf/proto"
)

func individual(id string) *gmproto.Conversation {
	return &gmproto.Conversation{
		ConversationID: id, DefaultOutgoingID: "self",
		Participants: []*gmproto.Participant{{ID: &gmproto.SmallInfo{ParticipantID: "peer"}, IsVisible: true}},
	}
}

// A list record carries everything listing needs, so no per-conversation
// lookup is made; groups and read-only conversations are labelled as such.
func TestListConversationProjectsListRecord(t *testing.T) {
	c := individual("chat-1")
	c.Name = "Synthetic Name"
	c.LastMessageTimestamp = 1_700_000_000_000_000
	got := listConversation(c)
	if got.ID != "chat-1" || got.ParticipantID != "peer" || got.Label != "Synthetic Name" || !got.Writable || got.Group || got.LastMessageAt == "" {
		t.Fatalf("unexpected projection: %+v", got)
	}
	group := individual("group-1")
	group.IsGroupChat = true
	if g := listConversation(group); g.Writable || !g.Group || g.Label != "Group conversation" {
		t.Fatalf("group listed as writable: %+v", g)
	}
	readOnly := individual("ro-1")
	readOnly.ReadOnly = true
	if r := listConversation(readOnly); r.Writable || r.Label != "Individual conversation" {
		t.Fatalf("read-only listed as writable: %+v", r)
	}
}

func TestConversationEventKeepsSendBaselineOnIdentityChange(t *testing.T) {
	g := NewGoogle(nil, "")
	c := individual("changed")
	g.cacheDetail(c)
	changed := proto.Clone(c).(*gmproto.Conversation)
	changed.IsGroupChat = true
	if !g.rememberConversationEvent(changed) {
		t.Fatal("identity change did not require pause")
	}
	if g.conversations["changed"] != c {
		t.Fatal("identity change replaced send baseline")
	}
	renamed := proto.Clone(c).(*gmproto.Conversation)
	renamed.Name = "Synthetic new label"
	if g.rememberConversationEvent(renamed) || g.conversations["changed"] != renamed {
		t.Fatal("ordinary update not applied")
	}
}
