package transport

import (
	"context"
	"errors"
	"fmt"
	"go.mau.fi/mautrix-gmessages/pkg/libgm/gmproto"
	"google.golang.org/protobuf/proto"
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

func TestDiscoveryReuses317DetailsAcrossReconciliationAndReconnect(t *testing.T) {
	g := NewGoogle(nil, "")
	ids := make([]string, 317)
	for i := range ids {
		id := fmt.Sprintf("synthetic-%d", i)
		ids[i] = id
		g.cacheDetail(&gmproto.Conversation{
			ConversationID: id, DefaultOutgoingID: "self",
			Participants: []*gmproto.Participant{{ID: &gmproto.SmallInfo{ParticipantID: "peer"}, IsVisible: true}},
		})
	}
	for range 4 {
		g.noteActivity() // Ordinary message activity must not discard details.
		g.forceDiscoveryRefresh(time.Now().Add(time.Hour))
		conversations, partial := discoveryConversations(context.Background(), ids, g.DiscoveryLookup)
		if partial || len(conversations) != len(ids) {
			t.Fatal("unchanged discovery required phone detail reads")
		}
	}
	// Discovery hints cannot authorize a protected lookup without the phone.
	if _, err := g.LookupConversation(context.Background(), ids[0]); !errors.Is(err, ErrUnavailable) {
		t.Fatal("discovery hint bypassed live lookup")
	}
	if _, err := g.DiscoveryLookup(context.Background(), "new-conversation"); !errors.Is(err, ErrUnavailable) {
		t.Fatal("unknown ID did not require live lookup")
	}
	// A reconnect re-verifies identities but keeps listing hints, so the
	// next discovery pass does not re-read every conversation from the phone.
	g.invalidateIdentities()
	if c, err := g.DiscoveryLookup(context.Background(), ids[0]); err != nil || c.ParticipantID != "peer" {
		t.Fatal("reconnect discarded discovery hints", err)
	}
	if _, err := g.LookupConversation(context.Background(), ids[0]); !errors.Is(err, ErrUnavailable) {
		t.Fatal("reconnect left a verified identity usable without the phone")
	}
	g.mu.Lock()
	verified := len(g.conversations)
	g.mu.Unlock()
	if verified != 0 {
		t.Fatal("reconnect kept verified identities")
	}
}

func TestDiscoveryInvalidatesOnlyChangedConversation(t *testing.T) {
	g := NewGoogle(nil, "")
	c := &gmproto.Conversation{
		ConversationID: "changed", DefaultOutgoingID: "self",
		Participants: []*gmproto.Participant{{ID: &gmproto.SmallInfo{ParticipantID: "peer"}, IsVisible: true}},
	}
	g.cacheDetail(c)
	unrelated := proto.Clone(c).(*gmproto.Conversation)
	unrelated.ConversationID = "unrelated"
	g.cacheDetail(unrelated)
	changed := proto.Clone(c).(*gmproto.Conversation)
	changed.IsGroupChat = true
	if !g.rememberConversationEvent(changed) {
		t.Fatal("identity change did not require pause")
	}
	if g.discoveryDetail("changed") != nil || g.conversations["changed"] != c {
		t.Fatal("identity change reused hint or replaced send baseline")
	}
	if _, err := g.DiscoveryLookup(context.Background(), "unrelated"); err != nil {
		t.Fatal("unrelated conversation was invalidated", err)
	}
	// An ordinary conversation event updates display hints immediately.
	unrelated = proto.Clone(unrelated).(*gmproto.Conversation)
	unrelated.Name = "Synthetic new label"
	if g.rememberConversationEvent(unrelated) {
		t.Fatal("label update changed identity")
	}
	got, err := g.DiscoveryLookup(context.Background(), "unrelated")
	if err != nil || got.Label != unrelated.Name {
		t.Fatal("conversation update did not refresh display", err)
	}
}
