package transport

import (
	"context"
	"errors"
	"fmt"
	"go.mau.fi/mautrix-gmessages/pkg/libgm/gmproto"
	"sync/atomic"
	"testing"
	"time"
)

func TestConversationDetailsBoundedParallelismAndOrder(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ids := make([]string, 25)
	for i := range ids {
		ids[i] = fmt.Sprintf("fixture-%d", i)
	}
	var active, maximum atomic.Int32
	started := make(chan struct{}, 4)
	release := make(chan struct{})
	done := make(chan struct{})
	var result []*gmproto.Conversation
	var resultErr error
	go func() {
		defer close(done)
		result, resultErr = conversationDetails(ctx, ids, func(ctx context.Context, id string) (*gmproto.Conversation, error) {
			current := active.Add(1)
			defer active.Add(-1)
			for old := maximum.Load(); current > old && !maximum.CompareAndSwap(old, current); old = maximum.Load() {
			}
			select {
			case started <- struct{}{}:
			default:
			}
			select {
			case <-release:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			return &gmproto.Conversation{ConversationID: id}, nil
		})
	}()
	for i := 0; i < 4; i++ {
		select {
		case <-started:
		case <-ctx.Done():
			t.Fatal("four independent requests did not overlap")
		}
	}
	close(release)
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("detail fetch did not finish")
	}
	if resultErr != nil {
		t.Fatal(resultErr)
	}
	if maximum.Load() != 4 {
		t.Fatalf("maximum concurrency=%d", maximum.Load())
	}
	for i, c := range result {
		if c.GetConversationID() != ids[i] {
			t.Fatal("inbox order changed")
		}
	}
}

func TestConversationDetailsRejectsIncompleteIdentityAndFailure(t *testing.T) {
	for _, mode := range []string{"mismatch", "nil", "failure"} {
		t.Run(mode, func(t *testing.T) {
			result, err := conversationDetails(context.Background(), []string{"fixture"}, func(context.Context, string) (*gmproto.Conversation, error) {
				switch mode {
				case "mismatch":
					return &gmproto.Conversation{ConversationID: "other-fixture"}, nil
				case "nil":
					return nil, nil
				default:
					return nil, errors.New("synthetic failure")
				}
			})
			if err == nil || result != nil {
				t.Fatal("partial or unverified identities returned")
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := conversationDetails(ctx, []string{"fixture"}, func(context.Context, string) (*gmproto.Conversation, error) {
		t.Fatal("cancelled request started")
		return nil, nil
	}); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation lost")
	}
}

func TestCachedConversationDetailsReusedAndExpired(t *testing.T) {
	g := NewGoogle(nil, "")
	c := &gmproto.Conversation{
		ConversationID:    "chat-1",
		DefaultOutgoingID: "self-1",
		Participants:      []*gmproto.Participant{{ID: &gmproto.SmallInfo{ParticipantID: "peer-1"}, IsVisible: true}},
	}
	g.cacheDetail(c)

	// Fresh cached detail is returned
	got := g.cachedDetail("chat-1")
	if got == nil || got.GetConversationID() != "chat-1" {
		t.Fatalf("expected cached detail for chat-1, got %v", got)
	}

	// Missing detail returns nil
	if g.cachedDetail("unknown") != nil {
		t.Fatal("expected nil for unknown conversation")
	}

	// Expired detail (> 10m) returns nil
	g.mu.Lock()
	g.conversationFetched["chat-1"] = time.Now().Add(-11 * time.Minute)
	g.mu.Unlock()
	if g.cachedDetail("chat-1") != nil {
		t.Fatal("expected nil for expired conversation detail")
	}

	// Group conversation is cached and retrievable
	group := &gmproto.Conversation{
		ConversationID: "group-1",
		IsGroupChat:    true,
		Name:           "Family Group",
	}
	g.cacheDetail(group)
	gotGroup := g.cachedDetail("group-1")
	if gotGroup == nil || gotGroup.GetConversationID() != "group-1" {
		t.Fatalf("expected cached detail for group-1, got %v", gotGroup)
	}

	// Invalidate identities clears both conversations and timestamps
	g.invalidateIdentities()
	if len(g.conversationFetched) != 0 || len(g.conversations) != 0 {
		t.Fatal("invalidateIdentities did not clear cached details and timestamps")
	}
}

func TestDiscoveryLookupReusesCache(t *testing.T) {
	g := NewGoogle(nil, "")
	c := &gmproto.Conversation{
		ConversationID:    "chat-1",
		DefaultOutgoingID: "self-1",
		Participants:      []*gmproto.Participant{{ID: &gmproto.SmallInfo{ParticipantID: "peer-1"}, IsVisible: true}},
	}
	g.cacheDetail(c)

	// When detail is cached, DiscoveryLookup returns it even without an active phone connection
	conv, err := g.DiscoveryLookup(context.Background(), "chat-1")
	if err != nil {
		t.Fatalf("unexpected error from cached DiscoveryLookup: %v", err)
	}
	if conv.ID != "chat-1" || conv.ParticipantID != "peer-1" || !conv.Writable {
		t.Fatalf("unexpected conversation from DiscoveryLookup: %+v", conv)
	}

	// Group detail cached -> DiscoveryLookup returns non-writable group conversation
	group := &gmproto.Conversation{
		ConversationID: "group-1",
		IsGroupChat:    true,
		Name:           "Family Group",
	}
	g.cacheDetail(group)
	groupConv, err := g.DiscoveryLookup(context.Background(), "group-1")
	if err != nil || !groupConv.Group || groupConv.Writable || groupConv.Label != "Family Group" {
		t.Fatalf("unexpected group conversation from DiscoveryLookup: %+v, err: %v", groupConv, err)
	}

	// When detail is missing, DiscoveryLookup falls back to LookupConversation and fails if transport unavailable
	_, err = g.DiscoveryLookup(context.Background(), "missing")
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable for missing detail without phone, got %v", err)
	}
}
