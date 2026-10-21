package transport

import (
	"context"
	"errors"
	"sync"
	"time"
)

type cachedConversationPage struct {
	page ConversationPage
	at   time.Time
}

// Pages that are not partial stay valid until the phone reports activity. Phone reads after
// activity are spaced by listInvalidateGap; a page older than the latest
// activity is returned marked stale within that gap so callers ask again.
type conversationPageCache struct {
	mu         sync.Mutex
	pages      map[string]cachedConversationPage
	flights    map[string]chan struct{}
	generation uint64
	changedAt  time.Time
}

func cloneConversationPage(p ConversationPage) ConversationPage {
	p.Conversations = append([]Conversation{}, p.Conversations...)
	return p
}
func (c *conversationPageCache) invalidate() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pages = nil
	c.generation++
}
func (c *conversationPageCache) markChanged(now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if now.After(c.changedAt) {
		c.changedAt = now
	}
}
func (c *conversationPageCache) get(ctx context.Context, key string, fetch func(context.Context) (ConversationPage, error)) (ConversationPage, error) {
	for {
		if err := ctx.Err(); err != nil {
			return ConversationPage{}, err
		}
		c.mu.Lock()
		if cached, ok := c.pages[key]; ok {
			current := cached.at.After(c.changedAt)
			// A partial page is never kept as complete: it is read again after
			// partialRetryGap, or after listInvalidateGap once activity arrives.
			gap := listInvalidateGap
			if current {
				gap = partialRetryGap
			}
			if current && !cached.page.Partial || time.Since(cached.at) < gap {
				c.mu.Unlock()
				page := cloneConversationPage(cached.page)
				page.Stale = !current
				return page, nil
			}
		}
		if wait, ok := c.flights[key]; ok {
			c.mu.Unlock()
			select {
			case <-ctx.Done():
				return ConversationPage{}, ctx.Err()
			case <-wait:
				continue
			}
		}
		if len(c.flights) >= 64 {
			c.mu.Unlock()
			return ConversationPage{}, errors.New("discovery_busy")
		}
		if c.flights == nil {
			c.flights = map[string]chan struct{}{}
		}
		done := make(chan struct{})
		c.flights[key] = done
		generation := c.generation
		// Activity during the read must leave the page older than that activity.
		started := time.Now()
		c.mu.Unlock()
		page, err := fetch(ctx)
		c.mu.Lock()
		delete(c.flights, key)
		if err == nil && generation == c.generation {
			if c.pages == nil {
				c.pages = map[string]cachedConversationPage{}
			}
			if len(c.pages) >= 64 {
				for oldest := range c.pages {
					delete(c.pages, oldest)
					break
				}
			}
			c.pages[key] = cachedConversationPage{cloneConversationPage(page), started}
		}
		out := cloneConversationPage(page)
		out.Stale = err == nil && !started.After(c.changedAt)
		close(done)
		c.mu.Unlock()
		return out, err
	}
}

// Preserve order and return a visibly partial page when one conversation has
// vanished. Discovery never grants send or capture permissions.
func discoveryConversations(ctx context.Context, ids []string, fetch func(context.Context, string) (Conversation, error)) ([]Conversation, bool) {
	results := make([]Conversation, len(ids))
	valid := make([]bool, len(ids))
	failed := make([]bool, len(ids))
	jobs := make(chan int, len(ids))
	for i := range ids {
		jobs <- i
	}
	close(jobs)
	var workers sync.WaitGroup
	for i := 0; i < min(4, len(ids)); i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for index := range jobs {
				if ctx.Err() != nil {
					failed[index] = true
					continue
				}
				c, err := fetch(ctx, ids[index])
				if err != nil || c.ID != ids[index] {
					failed[index] = true
					continue
				}
				if !c.Group && c.Writable && c.ParticipantID != "" {
					results[index] = c
					valid[index] = true
				}
			}
		}()
	}
	workers.Wait()
	out := []Conversation{}
	partial := false
	for i, c := range results {
		if valid[i] {
			out = append(out, c)
		}
		partial = partial || failed[i]
	}
	return out, partial
}
