package transport

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestDiscoveryCacheCoalescesRequestsAndClonesResults(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
func TestDiscoveryCacheKeepsPagesUntilActivity(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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

// pagedInbox serves a fixed chain of pages and counts phone reads.
type pagedInbox struct {
	pages map[string]ConversationPage
	reads map[string]int
}

func newPagedInbox(n int) *pagedInbox {
	b := &pagedInbox{pages: map[string]ConversationPage{}, reads: map[string]int{}}
	for i := range n {
		key, next := fmt.Sprint("p", i), fmt.Sprint("p", i+1)
		if i == 0 {
			key = ""
		}
		if i == n-1 {
			next = ""
		}
		b.pages[key] = ConversationPage{Conversations: []Conversation{{ID: fmt.Sprint("c", i), LastMessageAt: "2026-10-01T00:00:00Z"}}, NextCursor: next}
	}
	return b
}

// pass walks the whole chain like a discovery pass and returns phone reads.
func (b *pagedInbox) pass(t *testing.T, cache *conversationPageCache) int {
	t.Helper()
	before := 0
	for _, n := range b.reads {
		before += n
	}
	cursor := ""
	for range len(b.pages) + 1 {
		key := cursor
		page, err := cache.get(context.Background(), key, func(context.Context) (ConversationPage, error) {
			b.reads[key]++
			return b.pages[key], nil
		})
		if err != nil || page.Stale {
			t.Fatal("pass got an unusable page", err)
		}
		if cursor = page.NextCursor; cursor == "" {
			break
		}
	}
	after := 0
	for _, n := range b.reads {
		after += n
	}
	return after - before
}

func TestUnchangedPageConfirmsTheRestOfTheInbox(t *testing.T) {
	t.Parallel()
	var cache conversationPageCache
	inbox := newPagedInbox(16)
	if n := inbox.pass(t, &cache); n != 16 {
		t.Fatalf("first pass read %d pages", n)
	}
	// Activity in the front conversation changes only the first page.
	cache.markChanged(time.Now())
	backdate(&cache, listInvalidateGap)
	first := inbox.pages[""]
	first.Conversations = []Conversation{{ID: "c0", LastMessageAt: "2026-10-24T00:00:00Z"}}
	inbox.pages[""] = first
	if n := inbox.pass(t, &cache); n != 2 {
		t.Fatalf("want the changed page and one unchanged page, read %d", n)
	}
	// A reconnect makes everything stale; an unchanged front confirms the rest.
	cache.invalidate()
	backdate(&cache, listInvalidateGap)
	if n := inbox.pass(t, &cache); n != 1 {
		t.Fatalf("reconnect with no changes read %d pages", n)
	}
}

func TestMovedConversationRereadsUpToWhereItLeft(t *testing.T) {
	t.Parallel()
	var cache conversationPageCache
	inbox := newPagedInbox(8)
	inbox.pass(t, &cache)
	cache.markChanged(time.Now())
	backdate(&cache, listInvalidateGap)
	// A conversation from page 4 moves to the front: pages 0-4 shift.
	for i := 0; i <= 4; i++ {
		key := fmt.Sprint("p", i)
		if i == 0 {
			key = ""
		}
		p := inbox.pages[key]
		p.Conversations = []Conversation{{ID: fmt.Sprint("shifted", i)}}
		inbox.pages[key] = p
	}
	if n := inbox.pass(t, &cache); n != 6 {
		t.Fatalf("want pages 0-5 read, read %d", n)
	}
}

func TestPagesAreReadAtLeastDaily(t *testing.T) {
	t.Parallel()
	var cache conversationPageCache
	inbox := newPagedInbox(4)
	inbox.pass(t, &cache)
	cache.mu.Lock()
	for k, p := range cache.pages {
		p.read = p.read.Add(-pageRecheckAfter)
		cache.pages[k] = p
	}
	cache.mu.Unlock()
	cache.invalidate()
	backdate(&cache, listInvalidateGap)
	if n := inbox.pass(t, &cache); n != 4 {
		t.Fatalf("pages unread for a day were confirmed: read %d", n)
	}
}

// backdate ages every cached page and the activity mark.
func backdate(cache *conversationPageCache, by time.Duration) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	for k, p := range cache.pages {
		p.at = p.at.Add(-by)
		cache.pages[k] = p
	}
	cache.changedAt = cache.changedAt.Add(-by)
	cache.resetAt = cache.resetAt.Add(-by)
}

func TestDiscoveryCacheServesStalePageOnFailureAndShortBackoff(t *testing.T) {
	t.Parallel()
	var cache conversationPageCache
	calls := 0
	fetchOK := func(context.Context) (ConversationPage, error) {
		calls++
		return ConversationPage{Conversations: []Conversation{{ID: "c1"}}}, nil
	}
	p, err := cache.get(context.Background(), "", fetchOK)
	if err != nil || len(p.Conversations) != 1 || p.Stale {
		t.Fatalf("initial get failed: %v", err)
	}

	// Invalidate via activity so next get will try to refetch
	cache.markChanged(time.Now())
	backdate(&cache, listInvalidateGap)

	failCalls := 0
	fetchFail := func(context.Context) (ConversationPage, error) {
		failCalls++
		return ConversationPage{}, errors.New("temporary_rpc_error")
	}

	// Fetch fails: it should serve the stale cached page rather than failing
	p2, err2 := cache.get(context.Background(), "", fetchFail)
	if err2 != nil {
		t.Fatalf("expected stale page on failure, got err: %v", err2)
	}
	if !p2.Stale || len(p2.Conversations) != 1 || p2.Conversations[0].ID != "c1" {
		t.Fatalf("expected stale page with c1, got %+v", p2)
	}
	if failCalls != 1 {
		t.Fatalf("expected 1 failCall, got %d", failCalls)
	}

	// Inside failure backoff, it should continue serving stale page without calling fetch again
	p3, err3 := cache.get(context.Background(), "", fetchFail)
	if err3 != nil || !p3.Stale {
		t.Fatalf("expected stale page during backoff, got %v, %+v", err3, p3)
	}
	if failCalls != 1 {
		t.Fatalf("fetch was called again during backoff: %d", failCalls)
	}
}
