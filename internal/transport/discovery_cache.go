package transport

import (
	"context"
	"errors"
	"slices"
	"sync"
	"time"
)

type cachedConversationPage struct {
	page ConversationPage
	at   time.Time // when the page was last known current
	read time.Time // when the page was last read from the phone
}

// pageRecheckAfter bounds how long a page may be confirmed without being read:
// changes that do not reorder the list (a rename or deletion further down)
// are picked up within a day.
const pageRecheckAfter = 24 * time.Hour

// Pages that are not partial stay valid until the phone reports activity. Phone reads after
// activity are spaced by listInvalidateGap; a page older than the latest
// activity is returned marked stale within that gap so callers ask again.
//
// The list is ordered by recent activity, and activity only moves a
// conversation to the front. So when a re-read page comes back identical to
// its cached copy, with the same next cursor, every later page is unchanged
// too: those are confirmed without asking the phone.
type conversationPageCache struct {
	mu         sync.Mutex
	pages      map[string]cachedConversationPage
	flights    map[string]chan struct{}
	failures   map[string]time.Time
	generation uint64
	changedAt  time.Time
	resetAt    time.Time // pages from before this must be read again before use
}

func cloneConversationPage(p ConversationPage) ConversationPage {
	p.Conversations = append([]Conversation{}, p.Conversations...)
	return p
}

// invalidate makes every page stale, so the next pass re-reads from the front.
// The old pages stay only as the comparison that lets an unchanged tail be
// confirmed.
func (c *conversationPageCache) invalidate() {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	if now.After(c.changedAt) {
		c.changedAt = now
	}
	c.resetAt = now
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
		if cached, ok := c.pages[key]; ok && cached.at.After(c.resetAt) {
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
			old, had := c.pages[key]
			c.pages[key] = cachedConversationPage{cloneConversationPage(page), started, started}
			if had && samePage(old.page, page) {
				c.confirmAfterLocked(page.NextCursor, started)
			}
		}
		out := cloneConversationPage(page)
		out.Stale = err == nil && !started.After(c.changedAt)
		close(done)
		c.mu.Unlock()
		return out, err
	}
}

func samePage(a, b ConversationPage) bool {
	return !a.Partial && !b.Partial && a.NextCursor == b.NextCursor && slices.EqualFunc(a.Conversations, b.Conversations, func(x, y Conversation) bool { return x.Same(y) })
}

// confirmAfterLocked marks the cached pages following cursor current as of at,
// stopping at a missing page or one not read from the phone recently.
func (c *conversationPageCache) confirmAfterLocked(cursor string, at time.Time) {
	for range len(c.pages) {
		p, ok := c.pages[cursor]
		if cursor == "" || !ok || p.page.Partial || time.Since(p.read) >= pageRecheckAfter {
			return
		}
		if at.After(p.at) {
			p.at = at
			c.pages[cursor] = p
		}
		cursor = p.page.NextCursor
	}
}
