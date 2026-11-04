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
	d := SendDiagnostic{Reply: len(replyTo) > 0}
	defer func() {
		if g.SendLog != nil {
			d.At = time.Now().UTC().Format(time.RFC3339Nano)
			g.SendLog(d)
		}
	}()
	refused := func(reason string) (Receipt, error) {
		d.Result = reason
		return Receipt{}, refuse(reason)
	}
	if !g.Ready() {
		return refused("not_ready")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	parts, ok := outboundParts(text, files)
	if !ok {
		return refused("invalid_parts")
	}
	if ctx.Err() != nil {
		return refused("context_done")
	}

	g.countPhone(ctx, phoneLookup)
	c, e := g.client.GetConversation(ctx, id)
	if e != nil {
		return refused("conversation_lookup_failed")
	}
	g.mu.Lock()
	known := g.conversations[id]
	settings := g.settings
	g.mu.Unlock()
	d.Protocol = int32(c.GetType())
	// A group is sent to only as the people in it were when last verified: the
	// baseline is kept, not updated, by a roster change seen in an event.
	if !readable(c) {
		return refused("conversation_ineligible")
	}
	if known == nil {
		return refused("baseline_missing")
	}
	if peer(c) != peer(known) {
		return refused("participant_changed")
	}
	if roster(c) != roster(known) {
		return refused("roster_changed")
	}
	if c.GetDefaultOutgoingID() != known.GetDefaultOutgoingID() {
		return refused("outgoing_identity_changed")
	}
	// Whoever is in a group now must be who the sender was shown.
	if want, given := expectedRoster(ctx); c.GetIsGroupChat() && (!given || want == "" || roster(c) != want) {
		return refused("roster_mismatch")
	}
	sim := c.GetSimCard().GetSIMData().GetSIMPayload()
	if sim == nil && len(settings.GetSIMCards()) == 1 {
		sim = settings.GetSIMCards()[0].GetSIMData().GetSIMPayload()
	}
	if sim == nil {
		return refused("sim_missing")
	}
	if !g.Ready() {
		return refused("readiness_changed")
	}
	payload := &gmproto.MessagePayload{ConversationID: id, ParticipantID: c.GetDefaultOutgoingID(), TmpID: txn, TmpID2: txn, MessageInfo: parts}
	req := &gmproto.SendMessageRequest{ConversationID: id, SIMPayload: sim, TmpID: txn, MessagePayload: payload}
	if len(replyTo) > 0 {
		if !rcsProtocol(c.GetType().String()) {
			return refused("reply_protocol_not_rcs")
		}
		req.Reply = &gmproto.ReplyPayload{MessageID: replyTo[0]}
		req.ForceRCS = true
	}
	g.countPhone(ctx, phoneSend)
	res, e := g.client.SendMessage(ctx, req)
	if e != nil {
		d.Result = "submission_unknown"
		return Receipt{}, ErrUnknown
	}
	d.Status = int32(res.GetStatus())
	if res.GetStatus() != gmproto.SendMessageResponse_SUCCESS {
		return refused("upstream_rejected")
	}
	d.Result = "submitted"
	return Receipt{State: "submitted", MessageID: txn}, nil
}

// outboundParts orders files before the caption, as Google Messages Web does.
// A caption placed first makes the phone send it as plain text and drop the
// files while still reporting success.
func outboundParts(text string, files []media.File) ([]*gmproto.MessageInfo, bool) {
	parts := make([]*gmproto.MessageInfo, 0, len(files)+1)
	for _, file := range files {
		if file.RemoteID == "" || len(file.Key) != 32 {
			return nil, false
		}
		parts = append(parts, &gmproto.MessageInfo{Data: &gmproto.MessageInfo_MediaContent{MediaContent: outboundMedia(file)}})
	}
	if text != "" {
		parts = append(parts, &gmproto.MessageInfo{Data: &gmproto.MessageInfo_MessageContent{MessageContent: &gmproto.MessageContent{Content: text}}})
	}
	return parts, len(parts) > 0
}
