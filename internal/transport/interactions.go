package transport

import (
	"context"
	"strings"
	"time"

	"github.com/deusical/smsbot-server/internal/media"
	"go.mau.fi/mautrix-gmessages/pkg/libgm/gmproto"
	"go.mau.fi/util/variationselector"
)

// MessageInteraction is an authoritative snapshot when non-nil. Wire identities
// and participant IDs are sensitive and must only be persisted encrypted.
type MessageInteraction struct {
	UpstreamID string `json:"upstream_id"`
	Protocol   string `json:"protocol"`
	OwnerID    string `json:"owner_id"`
	ReplyTo    string `json:"reply_to,omitempty"`
	// Sender is the participant who sent the message, which tells people apart
	// in a group.
	Sender     string     `json:"sender,omitempty"`
	Reactions  []Reaction `json:"reactions"`
	ObservedAt time.Time  `json:"observed_at"`
}
type Reaction struct {
	Emoji        string   `json:"emoji"`
	Participants []string `json:"participants"`
	Unsupported  bool     `json:"unsupported,omitempty"`
}
type ReplySender interface {
	SendReply(context.Context, string, string, string, string, []media.File) (Receipt, error)
}
type Reactor interface {
	React(context.Context, string, string, string, string, string) error
}

// InteractionOwnerSource resolves the owner after the capture worker has looked
// up and validated the conversation. No RPC runs on the transport control loop.
type InteractionOwnerSource interface {
	InteractionOwner(string, string) (string, error)
}

func (g *Google) InteractionOwner(conv, expectedPeer string) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	c := g.conversations[conv]
	if !readable(c) || c.GetConversationID() != conv || peer(c) != expectedPeer {
		return "", ErrUnavailable
	}
	return c.GetDefaultOutgoingID(), nil
}

// A changed identity must retain the old baseline until pause and the next
// verified lookup. Publishing it early could undermine the transport send check.
func (g *Google) rememberConversationEvent(c *gmproto.Conversation) bool {
	g.identityEpoch.Add(1)
	g.mu.Lock()
	defer g.mu.Unlock()
	old := g.conversations[c.GetConversationID()]
	changed := old != nil && eligible(old) && (!eligible(c) || peer(old) != peer(c) || old.GetDefaultOutgoingID() != c.GetDefaultOutgoingID())
	// People joining or leaving a group don't pause everything, but the send
	// baseline stays as it was until a verified lookup replaces it.
	moved := old != nil && old.GetIsGroupChat() && roster(old) != roster(c)
	if !changed && !moved && c.GetConversationID() != "" {
		if g.conversations == nil {
			g.conversations = map[string]*gmproto.Conversation{}
		}
		g.conversations[c.GetConversationID()] = c
	}
	return changed
}

func CanonicalEmoji(s string) string { return variationselector.FullyQualify(s) }

func rcsProtocol(s string) bool { return strings.Contains(strings.ToUpper(s), "RCS") }

// conversationSnapshot is the interaction record for a message in c. Who sent
// it is kept for groups, where it is the only way to tell people apart.
func conversationSnapshot(c *gmproto.Conversation, m *gmproto.Message, observed time.Time) *MessageInteraction {
	v := interactionSnapshot(m, c.GetDefaultOutgoingID(), observed)
	if c.GetIsGroupChat() {
		v.Sender = m.GetParticipantID()
	}
	return v
}

func interactionSnapshot(m *gmproto.Message, owner string, observed time.Time) *MessageInteraction {
	protocol := "unknown"
	switch m.GetType() {
	case 1:
		protocol = "SMS"
	case 2, 3:
		protocol = "MMS"
	case 4:
		protocol = "RCS"
	}
	out := &MessageInteraction{UpstreamID: m.GetMessageID(), Protocol: protocol, OwnerID: owner, ReplyTo: m.GetReplyMessage().GetMessageID(), Reactions: []Reaction{}, ObservedAt: observed}
	for _, r := range m.GetReactions() {
		data := r.GetData()
		emoji := data.GetUnicode()
		if emoji == "" {
			emoji = data.GetType().Unicode()
		}
		out.Reactions = append(out.Reactions, Reaction{Emoji: CanonicalEmoji(emoji), Participants: append([]string(nil), r.GetParticipantIDs()...), Unsupported: data.GetCustomEmoji() != nil || emoji == ""})
	}
	return out
}

func (g *Google) SendReply(ctx context.Context, conv, txn, text, target string, files []media.File) (Receipt, error) {
	if target == "" {
		return Receipt{}, ErrNotSubmitted
	}
	return g.sendParts(ctx, conv, txn, text, files, target)
}

func (g *Google) React(ctx context.Context, conv, expectedPeer, target, emoji, operation string) error {
	ctx = WithPhonePurpose(ctx, "reaction")
	if !g.Ready() || target == "" {
		return ErrNotSubmitted
	}
	g.countPhone(ctx, phoneLookup)
	c, err := g.client.GetConversation(ctx, conv)
	if err != nil || c.GetConversationID() != conv || !eligible(c) || peer(c) != expectedPeer || !rcsProtocol(c.GetType().String()) {
		return ErrNotSubmitted
	}
	sim := c.GetSimCard().GetSIMData().GetSIMPayload()
	g.mu.Lock()
	settings := g.settings
	known := g.conversations[conv]
	g.mu.Unlock()
	if known == nil || known.GetDefaultOutgoingID() != c.GetDefaultOutgoingID() {
		return ErrNotSubmitted
	}
	if sim == nil && len(settings.GetSIMCards()) == 1 {
		sim = settings.GetSIMCards()[0].GetSIMData().GetSIMPayload()
	}
	if sim == nil || !g.Ready() {
		return ErrNotSubmitted
	}
	action := gmproto.SendReactionRequest_ADD
	switch operation {
	case "add":
	case "switch":
		action = gmproto.SendReactionRequest_SWITCH
	case "remove":
		action = gmproto.SendReactionRequest_REMOVE
	default:
		return ErrNotSubmitted
	}
	g.countPhone(ctx, phoneReact)
	res, err := g.client.SendReaction(ctx, &gmproto.SendReactionRequest{MessageID: target, ReactionData: gmproto.MakeReactionData(emoji), Action: action, SIMPayload: sim})
	if err != nil {
		return ErrUnknown
	}
	if !res.GetSuccess() {
		return ErrNotSubmitted
	}
	return nil
}

func (f *Fixture) SendReply(ctx context.Context, conv, txn, text, target string, files []media.File) (Receipt, error) {
	if target == "" || (conv != "demo-chat" && conv != "demo-group") {
		return Receipt{}, ErrNotSubmitted
	}
	return f.Send(ctx, conv, txn, text)
}
func (f *Fixture) React(ctx context.Context, conv, expectedPeer, target, emoji, operation string) error {
	if conv != "demo-chat" || expectedPeer != "demo-peer" || target == "" {
		return ErrNotSubmitted
	}
	_, err := f.Send(ctx, conv, "reaction:"+target, operation+":"+emoji)
	return err
}
