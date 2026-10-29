package transport

// Conversation discovery and identity. Lists are built from the phone's list
// pages, whose records carry the same identity fields as a single-conversation
// lookup; sends, captures and history reads still verify identity live.

import (
	"context"
	"errors"
	"sort"
	"time"

	"go.mau.fi/mautrix-gmessages/pkg/libgm/gmproto"
)

// members lists the visible people in a conversation other than the owner,
// by ID. It is nil if any of them has no ID, which makes identity unknowable.
func members(c *gmproto.Conversation) []Member {
	seen := map[string]bool{}
	var out []Member
	for _, p := range c.GetParticipants() {
		if p.GetIsMe() || !p.GetIsVisible() {
			continue
		}
		id := p.GetID().GetParticipantID()
		if id == "" {
			return nil
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		name := p.GetFullName()
		if name == "" {
			name = p.GetFirstName()
		}
		out = append(out, Member{ID: id, Name: name})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// peer is a conversation's exact identity: the other person's participant ID, or
// for a group "group:" and the conversation's own ID. It is "" when it can't be
// known, such as an individual conversation with several people in it.
func peer(c *gmproto.Conversation) string {
	m := members(c)
	if c.GetIsGroupChat() {
		if len(m) == 0 || c.GetConversationID() == "" {
			return ""
		}
		return "group:" + c.GetConversationID()
	}
	if len(m) != 1 {
		return ""
	}
	return m[0].ID
}

// roster names exactly who is in a group, or is "" for anything else.
func roster(c *gmproto.Conversation) string {
	m := groupMembers(c)
	if len(m) == 0 {
		return ""
	}
	ids := make([]string, len(m))
	for i := range m {
		ids[i] = m[i].ID
	}
	return GroupRoster(ids)
}

// readable is a conversation whose messages may be read and saved: identity is
// exactly known and it has an outgoing SIM, in a group or not.
func readable(c *gmproto.Conversation) bool {
	return c != nil && !c.GetReadOnly() && peer(c) != "" && c.GetDefaultOutgoingID() != ""
}

// eligible is an individual conversation the bot may act in, and the one
// typing and read marking apply to. A group can be sent to, and reacted in, once
// its roster is checked (see sendParts and React), but nothing else here acts for it.
func eligible(c *gmproto.Conversation) bool {
	return readable(c) && !c.GetIsGroupChat()
}

// listedIdentity is the identity shown for a conversation. A group that can't be
// read has none, so it is never selectable.
func listedIdentity(c *gmproto.Conversation) string {
	if c.GetIsGroupChat() && !readable(c) {
		return ""
	}
	return peer(c)
}

func groupMembers(c *gmproto.Conversation) []Member {
	if !c.GetIsGroupChat() {
		return nil
	}
	return members(c)
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

func conversationLabel(c *gmproto.Conversation) string {
	if label := c.GetName(); label != "" {
		return label
	}
	if c.GetIsGroupChat() {
		return "Group conversation"
	}
	return "Individual conversation"
}

// listConversation projects a phone conversation record for listing.
func listConversation(c *gmproto.Conversation) Conversation {
	label := conversationLabel(c)
	return withListState(Conversation{
		ID:            c.GetConversationID(),
		ParticipantID: listedIdentity(c),
		Members:       groupMembers(c),
		Roster:        roster(c),
		Label:         label,
		Protocol:      c.GetType().String(),
		Group:         c.GetIsGroupChat(),
		Writable:      readable(c),
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
	return detailConversation(id, c), nil
}

// CachedConversation projects the last phone record seen for id, from a
// lookup, the conversation list or a phone update.
func (g *Google) CachedConversation(id string) (Conversation, bool) {
	if !g.Ready() {
		return Conversation{}, false
	}
	g.mu.Lock()
	c := g.conversations[id]
	g.mu.Unlock()
	if c == nil || c.GetConversationID() != id {
		return Conversation{}, false
	}
	return detailConversation(id, c), true
}

func detailConversation(id string, c *gmproto.Conversation) Conversation {
	label := conversationLabel(c)
	return withListState(Conversation{ID: id, ParticipantID: listedIdentity(c), Members: groupMembers(c), Roster: roster(c), Label: label, Protocol: c.GetType().String(), Group: c.GetIsGroupChat(), Writable: readable(c)}, c)
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
