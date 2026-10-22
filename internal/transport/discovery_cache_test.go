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
func TestDiscoveryCacheKeepsPagesUntilActivity(t *testing.T) {
	var cache conversationPageCache
	calls := 0
	fetch := func(context.Context) (ConversationPage, error) { calls++; return ConversationPage{}, nil }
	cache.get(context.Background(), "", fetch)
	ageCachedPage(&cache, time.Hour)
	p, _ := cache.get(context.Background(), "", fetch)
	if calls != 1 || p.Stale {
		t.Fatal("an idle page was read from the phone again or marked stale")
	}
}
func TestDiscoveryCacheSpacesReadsAfterActivityAndMarksStalePages(t *testing.T) {
	var cache conversationPageCache
	calls := 0
	fetch := func(context.Context) (ConversationPage, error) { calls++; return ConversationPage{}, nil }
	cache.get(context.Background(), "", fetch)
	cache.markChanged(time.Now().Add(time.Millisecond))
	p, _ := cache.get(context.Background(), "", fetch)
	if calls != 1 || !p.Stale {
		t.Fatal("activity inside the spacing gap read the phone or hid staleness")
	}
	ageCachedPage(&cache, listInvalidateGap)
	p, _ = cache.get(context.Background(), "", fetch)
	if calls != 2 || p.Stale {
		t.Fatal("activity after the spacing gap was not read from the phone")
	}
	p, _ = cache.get(context.Background(), "", fetch)
	if calls != 2 || p.Stale {
		t.Fatal("a current page was read again")
	}
}
func TestDiscoveryCacheActivityDuringReadLeavesPageStale(t *testing.T) {
	var cache conversationPageCache
	calls := 0
	first, _ := cache.get(context.Background(), "", func(context.Context) (ConversationPage, error) {
		calls++
		cache.markChanged(time.Now().Add(time.Millisecond))
		return ConversationPage{}, nil
	})
	if !first.Stale {
		t.Fatal("the reading caller was not told about activity during its read")
	}
	p, _ := cache.get(context.Background(), "", func(context.Context) (ConversationPage, error) { calls++; return ConversationPage{}, nil })
	if calls != 1 || !p.Stale {
		t.Fatal("activity during a read was absorbed into the cached page")
	}
}
func ageCachedPage(cache *conversationPageCache, by time.Duration) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	p := cache.pages[""]
	p.at = p.at.Add(-by)
	cache.pages[""] = p
	if !cache.changedAt.IsZero() {
		cache.changedAt = cache.changedAt.Add(-by)
	}
}

func TestDiscoveryFailureSpacingCancellationAndInvalidation(t *testing.T) {
	var cache conversationPageCache
	var calls atomic.Int32
	fetch := func(context.Context) (ConversationPage, error) {
		calls.Add(1)
		return ConversationPage{}, errors.New("synthetic_failure")
	}
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := cache.get(context.Background(), "", fetch); err == nil {
				t.Error("failure hidden")
			}
		}()
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatal("failed read was repeated by waiters")
	}
	cache.markChanged(time.Now())
	cache.get(context.Background(), "", fetch)
	if calls.Load() != 1 {
		t.Fatal("activity bypassed failure spacing")
	}
	cache.mu.Lock()
	cache.failures[""] = time.Now().Add(-time.Second)
	cache.mu.Unlock()
	cache.get(context.Background(), "", fetch)
	if calls.Load() != 2 {
		t.Fatal("expired failure was not retried")
	}
	cache.invalidate()
	cache.get(context.Background(), "", fetch)
	if calls.Load() != 3 {
		t.Fatal("hard invalidation did not allow retry")
	}
	cache.invalidate()
	ctx, cancel := context.WithCancel(context.Background())
	cache.get(ctx, "", func(context.Context) (ConversationPage, error) { cancel(); return ConversationPage{}, context.Canceled })
	cache.get(context.Background(), "", fetch)
	if calls.Load() != 4 {
		t.Fatal("caller cancellation poisoned shared cache")
	}
}
func TestDiscoveryCacheRetriesPartialPages(t *testing.T) {
	var cache conversationPageCache
	calls := 0
	fetch := func(context.Context) (ConversationPage, error) { calls++; return ConversationPage{Partial: true}, nil }
	cache.get(context.Background(), "", fetch)
	ageCachedPage(&cache, listInvalidateGap)
	if p, _ := cache.get(context.Background(), "", fetch); calls != 1 || !p.Partial || p.Stale {
		t.Fatal("a partial page was re-read before its retry gap")
	}
	ageCachedPage(&cache, partialRetryGap)
	cache.get(context.Background(), "", fetch)
	if calls != 2 {
		t.Fatal("a partial page was kept as complete")
	}
	cache.markChanged(time.Now().Add(time.Millisecond))
	ageCachedPage(&cache, listInvalidateGap)
	cache.get(context.Background(), "", fetch)
	if calls != 3 {
		t.Fatal("activity on a partial page waited for the longer retry gap")
	}
}
