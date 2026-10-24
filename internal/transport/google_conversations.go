package transport

// Conversation discovery and identity. Discovery hints (cached details for
// listing) are kept separate from live identity lookups, which authorize
// sends, captures and history reads.

import (
	"context"
	"errors"
	"sync"
	"time"

	"go.mau.fi/mautrix-gmessages/pkg/libgm/gmproto"
)

func peer(c *gmproto.Conversation) string {
	ids := map[string]bool{}
	for _, p := range c.GetParticipants() {
		if p.GetIsMe() || !p.GetIsVisible() {
			continue
		}
		id := p.GetID().GetParticipantID()
		if id == "" {
			return ""
		}
		ids[id] = true
	}
	if len(ids) != 1 {
		return ""
	}
	for id := range ids {
		return id
	}
	return ""
}

func eligible(c *gmproto.Conversation) bool {
	return c != nil && !c.GetIsGroupChat() && !c.GetReadOnly() && peer(c) != "" && c.GetDefaultOutgoingID() != ""
}

func (g *Google) cacheDetail(c *gmproto.Conversation) {
	if c == nil || c.GetConversationID() == "" {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.conversations == nil {
		g.conversations = map[string]*gmproto.Conversation{}
	}
	g.conversations[c.GetConversationID()] = c
	g.cacheDiscoveryDetailLocked(c)
}

func (g *Google) cacheDiscoveryDetailLocked(c *gmproto.Conversation) {
	if g.discoveryDetails == nil {
		g.discoveryDetails = map[string]*gmproto.Conversation{}
	}
	g.discoveryDetails[c.GetConversationID()] = c
	g.hintsDirty.Store(true)
}

func (g *Google) discoveryDetail(id string) *gmproto.Conversation {
	g.mu.Lock()
	defer g.mu.Unlock()
	c := g.discoveryDetails[id]
	if id == "" || c.GetConversationID() != id {
		return nil
	}
	return c
}

func (g *Google) DiscoveryLookup(ctx context.Context, id string) (Conversation, error) {
	if err := ctx.Err(); err != nil {
		return Conversation{}, err
	}
	if c := g.discoveryDetail(id); c != nil {
		label := c.GetName()
		if label == "" {
			if c.GetIsGroupChat() {
				label = "Group conversation"
			} else {
				label = "Individual conversation"
			}
		}
		return withListState(Conversation{
			ID:            id,
			ParticipantID: peer(c),
			Label:         label,
			Protocol:      c.GetType().String(),
			Group:         c.GetIsGroupChat(),
			Writable:      eligible(c),
		}, c), nil
	}
	return g.LookupConversation(ctx, id)
}

func (g *Google) LookupConversation(ctx context.Context, id string) (Conversation, error) {
	if !g.Ready() {
		return Conversation{}, ErrUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	g.countPhone(ctx, phoneLookup)
	c, err := g.client.GetConversation(ctx, id)
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			reason, hard := "reconnecting", false
			if credentialError(err) {
				reason, hard = "login_required", true
			}
			g.noteFailure(reason, hard)
			g.diagnose("conversation_lookup", err)
		}
		return Conversation{}, ErrUnavailable
	}
	if c.GetConversationID() != id {
		return Conversation{}, errors.New("ineligible_conversation")
	}
	g.cacheDetail(c)
	label := c.GetName()
	if label == "" {
		label = "Individual conversation"
	}
	return withListState(Conversation{ID: id, ParticipantID: peer(c), Label: label, Protocol: c.GetType().String(), Group: c.GetIsGroupChat(), Writable: eligible(c)}, c), nil
}

func (g *Google) Conversations(ctx context.Context) ([]Conversation, error) {
	g.listMu.Lock()
	defer g.listMu.Unlock()
	if !g.Ready() {
		return nil, errors.New("phone connection unavailable")
	}
	if !g.activity.listStale.Swap(false) && time.Since(g.listed) < 30*time.Second {
		return append([]Conversation(nil), g.list...), nil
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	g.countPhone(ctx, phoneList)
	res, e := g.client.ListConversations(ctx, &gmproto.ListConversationsRequest{Count: 25, Folder: gmproto.ListConversationsRequest_INBOX})
	if e != nil {
		return nil, errors.New("conversation refresh failed")
	}
	out := []Conversation{}
	ids := make([]string, len(res.GetConversations()))
	for i, summary := range res.GetConversations() {
		ids[i] = summary.GetConversationID()
	}
	var fetchedMu sync.Mutex
	newFetched := make(map[string]bool)
	fetchDetail := func(ctx context.Context, id string) (*gmproto.Conversation, error) {
		if cached := g.discoveryDetail(id); cached != nil {
			return cached, nil
		}
		g.countPhone(ctx, phoneLookup)
		c, err := g.client.GetConversation(ctx, id)
		if err != nil {
			return nil, err
		}
		fetchedMu.Lock()
		newFetched[id] = true
		fetchedMu.Unlock()
		return c, nil
	}
	details, e := conversationDetails(ctx, ids, fetchDetail)
	if e != nil {
		return nil, errors.New("conversation identity refresh failed")
	}
	summaries := res.GetConversations()
	for i, c := range details {
		label := c.GetName()
		if label == "" {
			if c.GetIsGroupChat() {
				label = "Group conversation"
			} else {
				label = "Individual conversation"
			}
		}
		conv := Conversation{
			ID:            c.GetConversationID(),
			ParticipantID: peer(c),
			Label:         label,
			Protocol:      c.GetType().String(),
			Group:         c.GetIsGroupChat(),
			Writable:      eligible(c),
		}
		conv = withListState(conv, c)
		if i < len(summaries) && summaries[i] != nil {
			conv = withListState(conv, summaries[i])
		}
		out = append(out, conv)
	}
	// Publish identities only after the entire refresh succeeds.
	g.mu.Lock()
	if g.conversations == nil {
		g.conversations = map[string]*gmproto.Conversation{}
	}
	for _, c := range details {
		id := c.GetConversationID()
		if newFetched[id] {
			g.conversations[id] = c
			g.cacheDiscoveryDetailLocked(c)
		}
	}
	g.mu.Unlock()
	g.list = out
	g.listed = time.Now()
	return append([]Conversation(nil), out...), nil
}

// Fetch independent details with bounded concurrency while preserving inbox order.
func conversationDetails(ctx context.Context, ids []string, fetch func(context.Context, string) (*gmproto.Conversation, error)) ([]*gmproto.Conversation, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	results := make([]*gmproto.Conversation, len(ids))
	errs := make([]error, len(ids))
	jobs := make(chan int, len(ids))
	for i := range ids {
		jobs <- i
	}
	close(jobs)
	var workers sync.WaitGroup
	for worker := 0; worker < min(4, len(ids)); worker++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for i := range jobs {
				if ctx.Err() != nil {
					return
				}
				c, err := fetch(ctx, ids[i])
				if err == nil && (c == nil || ids[i] == "" || c.GetConversationID() != ids[i]) {
					err = errors.New("conversation identity mismatch")
				}
				if err != nil {
					errs[i] = err
					cancel()
					return
				}
				results[i] = c
			}
		}()
	}
	workers.Wait()
	for _, err := range errs {
		if err != nil {
			return nil, err
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return results, nil
}

// withListState copies the phone's ordering hints. The library reports the last
// message time in microseconds; smaller values are treated as milliseconds.
func withListState(out Conversation, c *gmproto.Conversation) Conversation {
	out.Unread = c.GetUnread()
	out.Pinned = c.GetPinned()
	if ts := c.GetLastMessageTimestamp(); ts > 0 {
		var t time.Time
		if ts > 1e14 {
			t = time.UnixMicro(ts)
		} else {
			t = time.UnixMilli(ts)
		}
		out.LastMessageAt = t.UTC().Format(time.RFC3339)
	}
	return out
}
