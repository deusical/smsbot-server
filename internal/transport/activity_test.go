package transport

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.mau.fi/mautrix-gmessages/pkg/libgm/gmproto"
)

func TestTypingReportsOnlyKnownIndividualConversationsAndExpires(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
	g := NewGoogle(nil, "")
	before := g.ActivityRevision()
	g.invalidateIdentities()
	if g.ActivityRevision() == before {
		t.Fatal("reconnect or overflow cleared the list without telling dashboards to read it again")
	}
}

func TestBackwardClockDoesNotLockOutForcedRefresh(t *testing.T) {
	t.Parallel()
	g := NewGoogle(nil, "")
	now := time.Now()
	if !g.forceDiscoveryRefresh(now) || !g.forceDiscoveryRefresh(now.Add(-time.Hour)) {
		t.Fatal("a clock stepped backwards locked out forced refreshes")
	}
}

func TestMarkReadUsesLatestMessageIDAndFallsBackWhenEmpty(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	through := time.Unix(1_700_000_000, 0).UTC()

	t.Run("latest_message_id_skips_fetch_and_recheck", func(t *testing.T) {
		t.Parallel()
		g := NewGoogle(nil, "")
		g.ready.Store(true)
		g.connected = func() bool { return true }
		c := individual("c1")
		c.Unread = true
		c.LatestMessageID = "msg-latest"
		c.LastMessageTimestamp = through.UnixMicro()

		var gets, fetches, marks int
		var markedID string
		err := g.markReadWith(ctx, "c1", "peer", through,
			func(context.Context, string) (*gmproto.Conversation, error) {
				gets++
				return c, nil
			},
			func(context.Context, string, int64, *gmproto.Cursor) (*gmproto.ListMessagesResponse, error) {
				fetches++
				return &gmproto.ListMessagesResponse{}, nil
			},
			func(_ context.Context, _, messageID string) error {
				marks++
				markedID = messageID
				return nil
			},
		)
		if err != nil {
			t.Fatalf("markReadWith failed: %v", err)
		}
		if gets != 1 || fetches != 0 || marks != 1 || markedID != "msg-latest" {
			t.Fatalf("gets=%d fetches=%d marks=%d markedID=%q", gets, fetches, marks, markedID)
		}
	})

	t.Run("latest_message_id_rejects_newer_message", func(t *testing.T) {
		t.Parallel()
		g := NewGoogle(nil, "")
		g.ready.Store(true)
		g.connected = func() bool { return true }
		c := individual("c1")
		c.Unread = true
		c.LatestMessageID = "msg-newer"
		c.LastMessageTimestamp = through.Add(5 * time.Second).UnixMicro()

		var fetches, marks int
		err := g.markReadWith(ctx, "c1", "peer", through,
			func(context.Context, string) (*gmproto.Conversation, error) { return c, nil },
			func(context.Context, string, int64, *gmproto.Cursor) (*gmproto.ListMessagesResponse, error) {
				fetches++
				return &gmproto.ListMessagesResponse{}, nil
			},
			func(context.Context, string, string) error {
				marks++
				return nil
			},
		)
		if !errors.Is(err, ErrNewerMessage) || fetches != 0 || marks != 0 {
			t.Fatalf("err=%v fetches=%d marks=%d, want ErrNewerMessage without fetch or mark", err, fetches, marks)
		}
	})

	t.Run("empty_latest_message_id_falls_back_to_fetch_and_recheck", func(t *testing.T) {
		t.Parallel()
		g := NewGoogle(nil, "")
		g.ready.Store(true)
		g.connected = func() bool { return true }
		c := individual("c1")
		c.Unread = true

		var gets, fetches, marks int
		var markedID string
		err := g.markReadWith(ctx, "c1", "peer", through,
			func(context.Context, string) (*gmproto.Conversation, error) {
				gets++
				return c, nil
			},
			func(context.Context, string, int64, *gmproto.Cursor) (*gmproto.ListMessagesResponse, error) {
				fetches++
				return &gmproto.ListMessagesResponse{Messages: []*gmproto.Message{
					{ConversationID: "c1", MessageID: "msg-fetched", Timestamp: through.UnixMicro()},
				}}, nil
			},
			func(_ context.Context, _, messageID string) error {
				marks++
				markedID = messageID
				return nil
			},
		)
		if err != nil {
			t.Fatalf("fallback markReadWith failed: %v", err)
		}
		if gets != 2 || fetches != 1 || marks != 1 || markedID != "msg-fetched" {
			t.Fatalf("gets=%d fetches=%d marks=%d markedID=%q", gets, fetches, marks, markedID)
		}
	})

	t.Run("fallback_identity_change_refuses_mark_read", func(t *testing.T) {
		t.Parallel()
		g := NewGoogle(nil, "")
		g.ready.Store(true)
		g.connected = func() bool { return true }
		first := individual("c1")
		first.Unread = true
		second := individual("c1")
		second.Unread = true
		second.Participants[0].ID.ParticipantID = "other-peer"

		gets, marks := 0, 0
		err := g.markReadWith(ctx, "c1", "peer", through,
			func(context.Context, string) (*gmproto.Conversation, error) {
				gets++
				if gets == 1 {
					return first, nil
				}
				return second, nil
			},
			func(context.Context, string, int64, *gmproto.Cursor) (*gmproto.ListMessagesResponse, error) {
				return &gmproto.ListMessagesResponse{Messages: []*gmproto.Message{
					{ConversationID: "c1", MessageID: "msg-fetched", Timestamp: through.UnixMicro()},
				}}, nil
			},
			func(context.Context, string, string) error {
				marks++
				return nil
			},
		)
		if err == nil || err.Error() != "conversation_identity_changed" || marks != 0 {
			t.Fatalf("err=%v marks=%d, want conversation_identity_changed without marking read", err, marks)
		}
	})
}
