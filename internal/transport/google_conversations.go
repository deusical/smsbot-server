package transport

// Conversation discovery and identity. Lists are built from the phone's list
// pages, whose records carry the same identity fields as a single-conversation
// lookup; sends, captures and history reads still verify identity live.

import (
	"context"
	"errors"
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
}

// listConversation projects a phone conversation record for listing.
func listConversation(c *gmproto.Conversation) Conversation {
	label := c.GetName()
	if label == "" {
		if c.GetIsGroupChat() {
			label = "Group conversation"
		} else {
			label = "Individual conversation"
		}
	}
	return withListState(Conversation{
		ID:            c.GetConversationID(),
		ParticipantID: peer(c),
		Label:         label,
		Protocol:      c.GetType().String(),
		Group:         c.GetIsGroupChat(),
		Writable:      eligible(c),
	}, c)
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
	// The list records are fresh phone reads: they also become the identity
	// baselines that sends compare against their own live lookup.
	out := []Conversation{}
	g.mu.Lock()
	if g.conversations == nil {
		g.conversations = map[string]*gmproto.Conversation{}
	}
	for _, c := range res.GetConversations() {
		if c.GetConversationID() == "" {
			continue
		}
		g.conversations[c.GetConversationID()] = c
		out = append(out, listConversation(c))
	}
	g.mu.Unlock()
	g.list = out
	g.listed = time.Now()
	return append([]Conversation(nil), out...), nil
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
