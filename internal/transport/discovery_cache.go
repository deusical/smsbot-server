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
	failures   map[string]time.Time
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
	c.failures = nil
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
		if until := c.failures[key]; time.Now().Before(until) {
			c.mu.Unlock()
			return ConversationPage{}, errors.New("discovery_unavailable")
		}
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
		if generation == c.generation {
			if err != nil && ctx.Err() == nil {
				if c.failures == nil {
					c.failures = map[string]time.Time{}
				}
				if len(c.failures) >= 64 {
					for old := range c.failures {
						delete(c.failures, old)
						break
					}
				}
				c.failures[key] = time.Now().Add(partialRetryGap)
			} else if err == nil {
				delete(c.failures, key)
			}
		}
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
