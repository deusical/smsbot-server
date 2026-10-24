package transport

// Sending text and files.

import (
	"context"
	"time"

	"github.com/deusical/smsbot-server/internal/media"
	"go.mau.fi/mautrix-gmessages/pkg/libgm/gmproto"
)

func (g *Google) Send(ctx context.Context, id, txn, text string) (Receipt, error) {
	return g.sendParts(ctx, id, txn, text, nil)
}

func (g *Google) SendFiles(ctx context.Context, id, txn, text string, files []media.File) (Receipt, error) {
	return g.sendParts(ctx, id, txn, text, files)
}

func (g *Google) sendParts(ctx context.Context, id, txn, text string, files []media.File, replyTo ...string) (Receipt, error) {
	if !g.Ready() {
		return Receipt{}, ErrNotSubmitted
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	parts := []*gmproto.MessageInfo{}
	if text != "" {
		parts = append(parts, &gmproto.MessageInfo{Data: &gmproto.MessageInfo_MessageContent{MessageContent: &gmproto.MessageContent{Content: text}}})
	}
	for _, file := range files {
		if ctx.Err() != nil || file.RemoteID == "" || len(file.Key) != 32 {
			return Receipt{}, ErrNotSubmitted
		}
		uploaded := outboundMedia(file)
		parts = append(parts, &gmproto.MessageInfo{Data: &gmproto.MessageInfo_MediaContent{MediaContent: uploaded}})
	}
	if ctx.Err() != nil {
		return Receipt{}, ErrNotSubmitted
	}

	g.countPhone(ctx, phoneLookup)
	c, e := g.client.GetConversation(ctx, id)
	if e != nil {
		return Receipt{}, ErrNotSubmitted
	}
	g.mu.Lock()
	known := g.conversations[id]
	settings := g.settings
	g.mu.Unlock()
	if !eligible(c) || known == nil || peer(c) != peer(known) || c.GetDefaultOutgoingID() != known.GetDefaultOutgoingID() {
		return Receipt{}, ErrNotSubmitted
	}
	sim := c.GetSimCard().GetSIMData().GetSIMPayload()
	if sim == nil && len(settings.GetSIMCards()) == 1 {
		sim = settings.GetSIMCards()[0].GetSIMData().GetSIMPayload()
	}
	if sim == nil {
		return Receipt{}, ErrNotSubmitted
	}
	if !g.Ready() {
		return Receipt{}, ErrNotSubmitted
	}
	payload := &gmproto.MessagePayload{ConversationID: id, ParticipantID: c.GetDefaultOutgoingID(), TmpID: txn, TmpID2: txn, MessageInfo: parts}
	req := &gmproto.SendMessageRequest{ConversationID: id, SIMPayload: sim, TmpID: txn, MessagePayload: payload}
	if len(replyTo) > 0 {
		if !rcsProtocol(c.GetType().String()) {
			return Receipt{}, ErrNotSubmitted
		}
		req.Reply = &gmproto.ReplyPayload{MessageID: replyTo[0]}
		req.ForceRCS = true
	}
	g.countPhone(ctx, phoneSend)
	res, e := g.client.SendMessage(ctx, req)
	if e != nil {
		return Receipt{}, ErrUnknown
	}
	if res.GetStatus() != gmproto.SendMessageResponse_SUCCESS {
		return Receipt{}, ErrNotSubmitted
	}
	return Receipt{State: "submitted", MessageID: txn}, nil
}
