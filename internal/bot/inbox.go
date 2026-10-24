package bot

import (
	"context"
	"errors"
	"github.com/deusical/smsbot-server/internal/media"
	"github.com/deusical/smsbot-server/internal/store"
	"github.com/deusical/smsbot-server/internal/transport"
	"strings"
	"time"
	"unicode/utf8"
)

// Receive separates optional owner inbox storage from bot routing. Pause,
// quiet hours and AI preferences never prevent an opted-in inbox observation.
func (e *Engine) Receive(ctx context.Context, m transport.Incoming) (bool, error) {
	if m.ID != "" && !m.Outgoing {
		first, err := e.Store.FirstObservation(m.ConversationID, m.ID)
		if err != nil {
			return false, err
		}
		if !first {
			m.Historical = true
		}
	}

	e.Collection.ObserveNow(ctx, m)
	accepted, err := e.Accept(ctx, e.commandSighting(m))
	if accepted && err == nil {
		err = e.Store.PublishCommandMessage(store.Hash(m.ConversationID + "\x00" + m.ID))
	}
	return accepted, err
}

// A repeat acknowledges the original intent without creating another job,
// even if limits or global pause changed after the lost acknowledgement.
func (e *Engine) existingCompose(id, conv, text string, files []media.File, replyTarget ...string) (bool, error) {
	recorded, err := e.Store.ManualJobExists(id, conv)
	if err != nil || !recorded {
		return false, err
	}
	digest, err := e.Store.GetSecretSetting("compose_intent:"+id, "compose-intent:"+id)
	if err != nil {
		return false, err
	}
	if len(digest) == 0 {
		return false, errors.New("request_already_recorded_check_activity")
	}
	target := ""
	if len(replyTarget) > 0 {
		target = replyTarget[0]
	}
	if string(digest) != store.ComposeIntent(text, files, target) {
		return false, errors.New("request_id_reused_with_different_message")
	}
	return true, nil
}

func (e *Engine) ComposeInbox(ctx context.Context, conv, text, requestID string) (string, error) {
	return e.ComposeInboxFiles(ctx, conv, text, requestID, nil)
}

func (e *Engine) ComposeInboxFiles(ctx context.Context, conv, text, requestID string, uploads []media.Upload, replyTargets ...string) (string, error) {
	replyTarget := ""
	if len(replyTargets) > 0 {
		replyTarget = replyTargets[0]
	}
	if len(replyTarget) > 128 {
		return "", errors.New("invalid_reply_target")
	}
	settings, err := e.Collection.Settings()
	if err != nil {
		return "", err
	}
	files, err := media.PrepareWithLimits(uploads, settings.FileLimits())
	if err != nil {
		return "", err
	}
	if len(files) > 0 {
		if _, ok := e.Transport.(transport.FileSender); !ok {
			return "", errors.New("attachments_unavailable")
		}
	}
	text = strings.TrimSpace(text)
	if (text == "" && len(files) == 0) || utf8.RuneCountInString(text) > 12000 || len(requestID) < 16 || len(requestID) > 100 {
		return "", errors.New("invalid_message_or_request_id")
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	id := store.Hash("compose\x00" + conv + "\x00" + requestID)
	e.mu.Lock()
	exists, err := e.existingCompose(id, conv, text, files, replyTarget)
	s, settingsErr := e.Collection.Settings()
	epoch := e.Collection.ConsentEpoch()
	e.mu.Unlock()
	if err != nil {
		return "", err
	}
	if exists {
		return id, nil
	}
	if settingsErr != nil {
		return "", settingsErr
	}
	if !s.Enabled || epoch == 0 {
		return "", errors.New("inbox_access_disabled")
	}
	c, err := e.lookupConversation(ctx, conv)
	if err != nil {
		return "", err
	}
	if a, archiveErr := e.Store.Archive(conv); archiveErr == nil && a.Conversation.ParticipantID != c.ParticipantID {
		return "", errors.New("archive_identity_changed")
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	exists, err = e.existingCompose(id, conv, text, files, replyTarget)
	if err != nil {
		return "", err
	}
	if exists {
		return id, nil
	}
	if err = ctx.Err(); err != nil {
		return "", err
	}
	current, err := e.Collection.Settings()
	if err != nil {
		return "", err
	}
	if e.Collection.ConsentEpoch() != epoch || !current.Allows(c) {
		return "", errors.New("inbox_access_disabled")
	}
	if err := current.FileLimits().Check(files); err != nil {
		return "", err
	}
	cfg, err := e.Configuration()
	if err != nil {
		return "", err
	}
	paused, err := e.Store.Paused()
	if err != nil {
		return "", err
	}
	if paused || !e.Transport.Ready() || quiet(cfg.Settings, e.now()) {
		return "", errors.New("bot_unavailable")
	}
	permitted, err := e.Store.Limits(e.now().UTC(), cfg.Settings.HourlyLimit, cfg.Settings.DailyLimit, 0, 0)
	if err != nil {
		return "", err
	}
	if !permitted {
		return "", errors.New("request_limit_reached")
	}
	wireTarget := ""
	if replyTarget != "" {
		v, err := e.replyTarget(ctx, c, replyTarget)
		if err != nil {
			return "", err
		}
		wireTarget = v.UpstreamID
	}
	_, err = e.Store.Enqueue(id, conv, "manual", store.Plan{ReplyTarget: replyTarget, ReplyWireTarget: wireTarget, Participant: c.ParticipantID, Reply: text, MaxChars: cfg.Settings.MaxChars, Manual: true, InboxOwnerOnly: true, Attachments: files})
	return id, err
}

// commandSighting marks a repeat sighting of an outgoing command as
// historical for routing. The phone reports a sent message again at each
// status change, and only the first report may start an owner command.
func (e *Engine) commandSighting(m transport.Incoming) transport.Incoming {
	if m.Outgoing && m.ID != "" && !m.Historical && strings.HasPrefix(strings.TrimSpace(m.Text), "!") {
		first, err := e.Store.FirstObservation(m.ConversationID, m.ID)
		m.Historical = err != nil || !first
	}
	return m
}
