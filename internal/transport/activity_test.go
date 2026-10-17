package transport

import (
	"testing"
	"time"

	"go.mau.fi/mautrix-gmessages/pkg/libgm/gmproto"
)

func TestTypingReportsOnlyKnownIndividualConversationsAndExpires(t *testing.T) {
	g := NewGoogle(nil, "")
	g.conversations["person"] = &gmproto.Conversation{ConversationID: "person"}
	g.conversations["group"] = &gmproto.Conversation{ConversationID: "group", IsGroupChat: true}
	for _, id := range []string{"person", "group", "unknown"} {
		g.noteTyping(&gmproto.TypingData{ConversationID: id, Type: gmproto.TypingTypes_STARTED_TYPING})
	}
	if got := g.TypingConversations(); len(got) != 1 || got[0] != "person" {
		t.Fatalf("typing %v", got)
	}
	g.noteTyping(&gmproto.TypingData{ConversationID: "person", Type: gmproto.TypingTypes_STOPPED_TYPING})
	if got := g.TypingConversations(); len(got) != 0 {
		t.Fatalf("stopped typing still reported: %v", got)
	}
	g.noteTyping(&gmproto.TypingData{ConversationID: "person", Type: gmproto.TypingTypes_STARTED_TYPING})
	g.activity.typingMu.Lock()
	g.activity.typing["person"] = time.Now().Add(-time.Second)
	g.activity.typingMu.Unlock()
	if got := g.TypingConversations(); len(got) != 0 {
		t.Fatalf("expired typing still reported: %v", got)
	}
}

func TestActivityDropsCachedListsAtMostOncePerGap(t *testing.T) {
	g := NewGoogle(nil, "")
	g.noteActivity()
	if !g.activity.listStale.Swap(false) {
		t.Fatal("first activity should mark the cached list stale")
	}
	g.noteActivity()
	g.noteActivity()
	if g.activity.listStale.Load() {
		t.Fatal("activity inside the gap invalidated the list again")
	}
	if g.ActivityRevision() != 3 {
		t.Fatalf("revision %d", g.ActivityRevision())
	}
}
