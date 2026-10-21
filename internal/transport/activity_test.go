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

func TestEveryActivityMarksDiscoveryChanged(t *testing.T) {
	g := NewGoogle(nil, "")
	g.noteActivity()
	g.discovery.mu.Lock()
	first := g.discovery.changedAt
	g.discovery.mu.Unlock()
	time.Sleep(2 * time.Millisecond)
	g.noteActivity()
	g.discovery.mu.Lock()
	second := g.discovery.changedAt
	g.discovery.mu.Unlock()
	if first.IsZero() || !second.After(first) {
		t.Fatal("activity inside the list gap was not recorded for discovery")
	}
}
func TestForcedDiscoveryRefreshIsSpacedAcrossClients(t *testing.T) {
	g := NewGoogle(nil, "")
	if g.RequestConversationRefresh() {
		t.Fatal("refresh honoured while the phone is unavailable")
	}
	now := time.Now()
	if !g.forceDiscoveryRefresh(now) {
		t.Fatal("first forced refresh refused")
	}
	if g.forceDiscoveryRefresh(now.Add(30 * time.Second)) {
		t.Fatal("forced refresh honoured inside the spacing gap")
	}
	if !g.forceDiscoveryRefresh(now.Add(conversationRefreshGap)) {
		t.Fatal("forced refresh refused after the spacing gap")
	}
	if g.ActivityRevision() != 0 {
		t.Fatal("a forced refresh changed the activity revision")
	}
}

func TestClearedIdentitiesAdvanceTheListRevision(t *testing.T) {
	g := NewGoogle(nil, "")
	before := g.ActivityRevision()
	g.invalidateIdentities()
	if g.ActivityRevision() == before {
		t.Fatal("reconnect or overflow cleared the list without telling dashboards to read it again")
	}
}

func TestBackwardClockDoesNotLockOutForcedRefresh(t *testing.T) {
	g := NewGoogle(nil, "")
	now := time.Now()
	if !g.forceDiscoveryRefresh(now) || !g.forceDiscoveryRefresh(now.Add(-time.Hour)) {
		t.Fatal("a clock stepped backwards locked out forced refreshes")
	}
}
