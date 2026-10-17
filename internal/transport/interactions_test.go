package transport

import (
	"go.mau.fi/mautrix-gmessages/pkg/libgm/gmproto"
	"google.golang.org/protobuf/proto"
	"testing"
	"time"
)

func TestInteractionNormalization(t *testing.T) {
	if CanonicalEmoji("❤") != CanonicalEmoji("❤️") {
		t.Fatal("equivalent emoji forms differ")
	}
	for wire, protocol := range map[int64]string{0: "unknown", 1: "SMS", 2: "MMS", 3: "MMS", 4: "RCS", 99: "unknown"} {
		m := &gmproto.Message{MessageID: "synthetic-wire", Type: wire, ReplyMessage: &gmproto.ReplyMessage{MessageID: "synthetic-original"}, Reactions: []*gmproto.ReactionEntry{{Data: &gmproto.ReactionData{Type: gmproto.EmojiType_LIKE}, ParticipantIDs: []string{"synthetic-self"}}}}
		v := interactionSnapshot(m, "synthetic-self", time.Now())
		if v.Protocol != protocol || v.ReplyTo != "synthetic-original" || len(v.Reactions) != 1 || v.Reactions[0].Emoji != "👍" {
			t.Fatal("normalized interaction incorrect", protocol)
		}
	}
	m := &gmproto.Message{MessageID: "synthetic", Reactions: []*gmproto.ReactionEntry{{Data: &gmproto.ReactionData{Type: gmproto.EmojiType_EMOTIFY, CustomEmoji: &gmproto.CustomEmojiData{}}}}}
	if v := interactionSnapshot(m, "", time.Now()); !v.Reactions[0].Unsupported {
		t.Fatal("image reaction falsely supported")
	}
	if v := interactionSnapshot(&gmproto.Message{}, "", time.Now()); v.Reactions == nil {
		t.Fatal("empty full snapshot not explicit")
	}
}

func TestConversationEventsPopulateOwnerAndPreserveIdentityBaseline(t *testing.T) {
	g := &Google{}
	c := &gmproto.Conversation{ConversationID: "synthetic-conv", DefaultOutgoingID: "synthetic-self", Participants: []*gmproto.Participant{{ID: &gmproto.SmallInfo{ParticipantID: "synthetic-peer"}, IsVisible: true}}}
	if g.rememberConversationEvent(c) {
		t.Fatal("first eligible identity reported as change")
	}
	owner, err := g.InteractionOwner("synthetic-conv", "synthetic-peer")
	if err != nil || owner != "synthetic-self" {
		t.Fatal("event did not populate owner", err)
	}
	if _, err = g.InteractionOwner("synthetic-conv", "different-peer"); err == nil {
		t.Fatal("wrong peer accepted")
	}
	changed := proto.Clone(c).(*gmproto.Conversation)
	changed.DefaultOutgoingID = "different-self"
	if !g.rememberConversationEvent(changed) {
		t.Fatal("changed self identity did not require pause")
	}
	owner, err = g.InteractionOwner("synthetic-conv", "synthetic-peer")
	if err != nil || owner != "synthetic-self" {
		t.Fatal("changed identity replaced send baseline")
	}
	grouped := proto.Clone(c).(*gmproto.Conversation)
	grouped.IsGroupChat = true
	if !g.rememberConversationEvent(grouped) {
		t.Fatal("group transition did not require pause")
	}
}
