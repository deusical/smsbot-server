package transport

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestDiscoveryCacheCoalescesRequestsAndClonesResults(t *testing.T) {
	var cache conversationPageCache
	var calls atomic.Int32
	start := make(chan struct{})
	var workers sync.WaitGroup
	fetch := func(context.Context) (ConversationPage, error) {
		calls.Add(1)
		<-start
		return ConversationPage{Conversations: []Conversation{{ID: "synthetic-chat", ParticipantID: "synthetic-peer", Writable: true}}}, nil
	}
	for i := 0; i < 16; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			p, err := cache.get(context.Background(), "", fetch)
			if err != nil || len(p.Conversations) != 1 {
				t.Error("discovery failed")
			}
			p.Conversations[0].Enrolled = true
		}()
	}
	close(start)
	workers.Wait()
	p, err := cache.get(context.Background(), "", fetch)
	if err != nil || calls.Load() != 1 || p.Conversations[0].Enrolled {
		t.Fatal("discovery was repeated or callers mutated cache")
	}
	cache.invalidate()
	cache.get(context.Background(), "", fetch)
	if calls.Load() != 2 {
		t.Fatal("cache invalidation did not refetch")
	}
}
func TestDiscoveryCacheWaitHonorsCancellation(t *testing.T) {
	var cache conversationPageCache
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		cache.get(context.Background(), "", func(context.Context) (ConversationPage, error) {
			close(entered)
			<-release
			return ConversationPage{}, nil
		})
	}()
	<-entered
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := cache.get(ctx, "", nil); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled discovery waiter blocked")
	}
	close(release)
	<-done
}
func TestDiscoverySkipsFailureAndMarksPartialWithoutChangingOrder(t *testing.T) {
	p, partial := discoveryConversations(context.Background(), []string{"first", "missing", "group", "last"}, func(_ context.Context, id string) (Conversation, error) {
		if id == "missing" {
			return Conversation{}, errors.New("synthetic_deleted")
		}
		return Conversation{ID: id, ParticipantID: "synthetic-peer", Writable: true, Group: id == "group"}, nil
	})
	if !partial || len(p) != 2 || p[0].ID != "first" || p[1].ID != "last" {
		t.Fatal("one failure hid the page or group exclusion/order was lost")
	}
}
func TestDiscoveryCacheExpires(t *testing.T) {
	var cache conversationPageCache
	calls := 0
	fetch := func(context.Context) (ConversationPage, error) { calls++; return ConversationPage{}, nil }
	cache.get(context.Background(), "", fetch)
	cache.mu.Lock()
	p := cache.pages[""]
	p.at = time.Now().Add(-time.Minute)
	cache.pages[""] = p
	cache.mu.Unlock()
	cache.get(context.Background(), "", fetch)
	if calls != 2 {
		t.Fatal("expired page reused")
	}
}
