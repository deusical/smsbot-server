package bot

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"smsbot-server/internal/store"
	"smsbot-server/internal/transport"
)

type MessageActions struct {
	Reply      bool   `json:"reply"`
	React      bool   `json:"react"`
	Edit       bool   `json:"edit"`
	Reason     string `json:"reason,omitempty"`
	EditReason string `json:"edit_reason"`
	Version    string `json:"version"`
	Pending    string `json:"pending,omitempty"`
}

func isRCS(protocol string) bool { return strings.Contains(strings.ToUpper(protocol), "RCS") }

// Called under e.mu. This is policy, not a replacement for transport rechecks.
func (e *Engine) replyTarget(ctx context.Context, c transport.Conversation, target string) (*transport.MessageInteraction, error) {
	v, err := e.Store.Interaction(c.ID, target)
	if err != nil {
		return nil, err
	}
	if v == nil || v.Protocol != "RCS" || v.UpstreamID == "" || !isRCS(c.Protocol) || c.Group || !c.Writable {
		return nil, errors.New("rcs_action_unavailable")
	}
	if _, ok := e.Transport.(transport.ReplySender); !ok {
		return nil, errors.New("reply_unavailable")
	}
	return v, nil
}
func (e *Engine) actionPolicy(ctx context.Context, conv, target string) (MessageActions, transport.Conversation, error) {
	a := MessageActions{EditReason: "editing_unavailable_in_companion_transport"}
	v, err := e.Store.Interaction(conv, target)
	if err != nil {
		return a, transport.Conversation{}, err
	}
	if v == nil || v.Protocol != "RCS" || v.UpstreamID == "" {
		a.Reason = "message_protocol_unsupported"
		return a, transport.Conversation{}, nil
	}
	a.Version = store.InteractionVersion(v)
	for _, r := range v.Reactions {
		if r.Unsupported {
			for _, actor := range r.Participants {
				if actor == v.OwnerID {
					a.Reason = "unsupported_owner_reaction"
					return a, transport.Conversation{}, nil
				}
			}
		}
	}
	settings, err := e.InboxSettings()
	if err != nil {
		return a, transport.Conversation{}, err
	}
	if !settings.Enabled || !e.Transport.Ready() {
		a.Reason = "collection_or_transport_unavailable"
		return a, transport.Conversation{}, nil
	}
	c, err := e.lookupConversation(ctx, conv)
	if err != nil {
		return a, c, err
	}
	archive, err := e.Store.Archive(conv)
	if err != nil || archive.Conversation.ParticipantID != c.ParticipantID || !settings.Allows(c) || c.Group || !c.Writable || !isRCS(c.Protocol) {
		a.Reason = "conversation_action_unavailable"
		return a, c, nil
	}
	paused, err := e.Store.Paused()
	if err != nil {
		return a, c, err
	}
	if paused {
		a.Reason = "sending_paused"
		return a, c, nil
	}
	if _, ok := e.Transport.(transport.ReplySender); ok {
		a.Reply = true
	}
	if _, ok := e.Transport.(transport.Reactor); ok && v.OwnerID != "" {
		a.React = true
	}
	a.Pending, err = e.Store.ActiveMutation(conv, target)
	if err != nil {
		return a, c, err
	}
	if a.Pending != "" {
		a.React = false
		a.Reason = "reaction_pending"
	}
	return a, c, nil
}
func (e *Engine) MessageActions(ctx context.Context, conv, target string) (MessageActions, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	a, _, err := e.actionPolicy(ctx, conv, target)
	return a, err
}
func validEmoji(s string) bool {
	if !utf8.ValidString(s) || len(s) == 0 || len(s) > 64 {
		return false
	}
	emoji := false
	for _, r := range s {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return false
		}
		if r >= 0x1f000 && r <= 0x1faff || r >= 0x2600 && r <= 0x27ff || r == 0x00a9 || r == 0x00ae || r == 0x203c || r == 0x2049 || r == 0x2122 || r == 0x2139 || r >= 0x2194 && r <= 0x23ff || r == 0x3030 || r == 0x303d || r == 0x3297 || r == 0x3299 {
			emoji = true
		} else if !(r == 0xfe0f || r == 0x200d || r == 0x20e3 || r >= 0xe0020 && r <= 0xe007f || r >= '0' && r <= '9' || r == '#' || r == '*') {
			return false
		}
	}
	return emoji || strings.ContainsRune(s, 0x20e3)
}
func (e *Engine) QueueReaction(ctx context.Context, conv, target, emoji, operation, version, requestID string) (string, error) {
	emoji = transport.CanonicalEmoji(emoji)
	if len(requestID) < 16 || len(requestID) > 100 || len(target) > 128 || !validEmoji(emoji) || (operation != "add" && operation != "switch" && operation != "remove") {
		return "", errors.New("invalid_reaction")
	}
	id := store.Hash("reaction\x00" + conv + "\x00" + requestID)
	e.mu.Lock()
	defer e.mu.Unlock()
	previous, err := e.Store.Mutation(id)
	if err == nil {
		if previous.Target != target || previous.Emoji != emoji || previous.Operation != operation || previous.Version != version {
			return "", errors.New("request_id_reused_with_different_action")
		}
		return id, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	a, c, err := e.actionPolicy(ctx, conv, target)
	if err != nil {
		return "", err
	}
	if !a.React {
		return "", errors.New("reaction_unavailable")
	}
	if version == "" || a.Version != version {
		return "", errors.New("reaction_conflict")
	}
	v, err := e.Store.Interaction(conv, target)
	if err != nil {
		return "", err
	}
	mine := ""
	for _, r := range v.Reactions {
		for _, actor := range r.Participants {
			if actor == v.OwnerID {
				mine = transport.CanonicalEmoji(r.Emoji)
			}
		}
	}
	if operation == "add" && mine != "" || operation == "switch" && (mine == "" || mine == emoji) || operation == "remove" && mine != emoji {
		return "", errors.New("reaction_conflict")
	}
	cfg, err := e.Configuration()
	if err != nil {
		return "", err
	}
	if quiet(cfg.Settings, e.now()) {
		return "", errors.New("quiet_hours")
	}
	permitted, err := e.Store.Limits(e.now().UTC(), cfg.Settings.HourlyLimit, cfg.Settings.DailyLimit, 0, 0)
	if err != nil {
		return "", err
	}
	if !permitted {
		return "", errors.New("request_limit_reached")
	}
	intent := &store.MutationIntent{Target: target, WireTarget: v.UpstreamID, Protocol: "RCS", Emoji: emoji, Operation: operation, Version: version}
	_, err = e.Store.Enqueue(id, conv, "reaction", store.Plan{Participant: c.ParticipantID, Manual: true, InboxOwnerOnly: true, Mutation: intent})
	return id, err
}

// Dispatch owns both engine locks. A reaction is not a text send and must never
// enter the normal message/receipt reconciliation path.
func (e *Engine) dispatchReaction(ctx context.Context, j store.Job, p store.Plan) error {
	defer e.mu.Unlock()
	m := p.Mutation
	v, err := e.Store.Interaction(j.ConversationID, m.Target)
	if err != nil {
		return err
	}
	c, err := e.lookupConversation(ctx, j.ConversationID)
	if err != nil {
		return err
	}
	if m.Protocol != "RCS" || v == nil || v.Protocol != "RCS" || v.OwnerID == "" || v.UpstreamID != m.WireTarget || store.InteractionVersion(v) != m.Version || !isRCS(c.Protocol) {
		return e.Store.Complete(j.ID, "failed", "reaction_conflict")
	}
	// The separate durable intent must exist before any submission.
	persisted, err := e.Store.Mutation(j.ID)
	if err != nil {
		return err
	}
	if persisted.WireTarget != m.WireTarget {
		return e.Store.Complete(j.ID, "failed", "reaction_conflict")
	}
	reactor, ok := e.Transport.(transport.Reactor)
	if !ok {
		return e.Store.Complete(j.ID, "failed", "reaction_unavailable")
	}
	if err = e.Store.StartMutation(j.ID, *m); err != nil {
		return err
	}
	sendctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	err = reactor.React(sendctx, j.ConversationID, p.Participant, m.WireTarget, m.Emoji, m.Operation)
	if err != nil {
		state, code := "unknown_submission", "transport_outcome_unknown"
		if errors.Is(err, transport.ErrNotSubmitted) {
			state, code = "failed", "not_submitted"
		}
		return e.Store.Complete(j.ID, state, code)
	}
	return e.Store.Complete(j.ID, "submitted", "awaiting_reaction_observation")
}

// Mutation status contains no raw provider errors, message text, or identities.
func (e *Engine) ReactionStatus(conv, id string) (map[string]string, error) {
	var state, code string
	err := e.Store.DB.QueryRow("SELECT j.state,j.code FROM jobs j JOIN interaction_intents a ON a.job=j.id WHERE j.id=? AND j.conversation=?", id, conv).Scan(&state, &code)
	return map[string]string{"id": id, "state": state, "code": code}, err
}
