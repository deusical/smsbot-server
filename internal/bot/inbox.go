package bot

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"smsbot-server/internal/media"
	"smsbot-server/internal/store"
	"smsbot-server/internal/transport"
	"strings"
	"time"
	"unicode/utf8"
)

type InboxBinding struct {
	Conversation string `json:"conversation"`
	Participant  string `json:"participant"`
}
type InboxSettings struct {
	ArchiveRetentionDays int            `json:"archive_retention_days"`
	AttachmentLimits     *media.Limits  `json:"attachment_limits,omitempty"`
	Enabled              bool           `json:"enabled"`
	Scope                string         `json:"scope"`
	Conversations        []InboxBinding `json:"conversations"`
	LoadOlder            bool           `json:"load_older"`
	AutomaticReplies     bool           `json:"automatic_replies"`
	// MarkRead is a pointer so settings saved before this option existed keep the
	// default (on) instead of decoding to false.
	MarkRead *bool `json:"mark_read_when_viewed,omitempty"`
	Revision int64 `json:"revision"`
}

func (s InboxSettings) FileLimits() media.Limits {
	if s.AttachmentLimits == nil {
		return media.DefaultLimits()
	}
	return *s.AttachmentLimits
}
func (e *Engine) InboxSettings() (InboxSettings, error) {
	defaults := media.DefaultLimits()
	s := InboxSettings{AttachmentLimits: &defaults, Scope: "all", AutomaticReplies: true, Conversations: []InboxBinding{}}
	b, err := e.Store.GetSecretSetting("inbox_settings", "inbox-settings")
	if err != nil {
		return s, err
	}
	if len(b) > 0 {
		err = json.Unmarshal(b, &s)
	}
	return s, err
}
func (s InboxSettings) Allows(c transport.Conversation) bool {
	if !s.Enabled || c.Group || !c.Writable || c.ParticipantID == "" {
		return false
	}
	if s.Scope == "all" {
		return true
	}
	for _, b := range s.Conversations {
		if b.Conversation == c.ID && b.Participant == c.ParticipantID {
			return true
		}
	}
	return false
}
func (e *Engine) lookupConversation(ctx context.Context, id string) (transport.Conversation, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if lookup, ok := e.Transport.(transport.ConversationLookup); ok {
		return lookup.LookupConversation(ctx, id)
	}
	list, err := e.Transport.Conversations(ctx)
	if err != nil {
		return transport.Conversation{}, err
	}
	for _, c := range list {
		if c.ID == id {
			return c, nil
		}
	}
	return transport.Conversation{}, errors.New("conversation_unavailable")
}
func (e *Engine) SetInboxSettings(ctx context.Context, s InboxSettings) error {
	if s.ArchiveRetentionDays < 0 || s.ArchiveRetentionDays > 3650 {
		return errors.New("invalid_archive_retention")
	}
	limits := s.FileLimits()
	if err := limits.Validate(); err != nil {
		return err
	}
	s.AttachmentLimits = &limits
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	e.mu.Lock()
	old, err := e.InboxSettings()
	e.mu.Unlock()
	if err != nil {
		return err
	}
	if s.Revision != old.Revision {
		return errors.New("settings_changed_reload")
	}
	if (s.Scope != "all" && s.Scope != "selected") || len(s.Conversations) > 500 {
		return errors.New("invalid_inbox_settings")
	}
	seen := map[string]bool{}
	for _, b := range s.Conversations {
		if b.Conversation == "" || b.Participant == "" || len(b.Conversation) > 200 || len(b.Participant) > 200 || seen[b.Conversation] {
			return errors.New("invalid_inbox_binding")
		}
		seen[b.Conversation] = true
		if s.Enabled && s.Scope == "selected" {
			c, err := e.lookupConversation(ctx, b.Conversation)
			if err != nil || c.Group || !c.Writable || c.ParticipantID != b.Participant {
				return errors.New("conversation_identity_changed")
			}
		}
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if err = ctx.Err(); err != nil {
		return err
	}
	current, err := e.InboxSettings()
	if err != nil {
		return err
	}
	if current.Revision != s.Revision {
		return errors.New("settings_changed_reload")
	}
	s.Revision++
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	if err = e.Store.SetInboxPreferences(b, !s.AutomaticReplies, s.ArchiveRetentionDays); err != nil {
		return err
	}
	if !s.AutomaticReplies {
		for _, cancel := range e.pending {
			cancel()
		}
	}
	e.publishCaptureConsent(s)
	if s.Enabled && e.History != nil {
		e.History.Wake()
	}
	return nil
}

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

	if err := e.Observe(ctx, m); err != nil {
		if consent := e.captureConsent.Load(); consent != nil {
			e.noteCaptureGap(m, consent.epoch, captureFailureReason(err))
		}
	}
	accepted, err := e.Accept(ctx, m)
	if accepted && err == nil {
		err = e.Store.PublishCommandMessage(store.Hash(m.ConversationID + "\x00" + m.ID))
	}
	return accepted, err
}
func (e *Engine) Observe(ctx context.Context, m transport.Incoming) error {
	return e.observe(ctx, m, 0)
}
func (e *Engine) observe(ctx context.Context, m transport.Incoming, epoch uint64) error {
	ctx = transport.WithPhonePurpose(ctx, "capture")
	if m.Outgoing && m.Interaction != nil {
		m.Historical = true
	}
	if m.ID == "" || m.ConversationID == "" || (m.Text == "" && len(m.Attachments) == 0) || (!m.Historical && e.now().Sub(m.At) > 2*time.Minute) || m.At.After(e.now().Add(5*time.Second)) {
		return nil
	}
	e.mu.Lock()
	s, err := e.InboxSettings()
	consent := e.captureConsent.Load()
	e.mu.Unlock()
	if err != nil {
		return err
	}
	if !s.Enabled {
		return nil
	}
	if consent == nil {
		return nil
	}
	if epoch == 0 {
		epoch = consent.epoch
	}
	if epoch != consent.epoch {
		return nil
	}
	if s.Scope == "selected" {
		found := false
		for _, b := range s.Conversations {
			if b.Conversation == m.ConversationID && (m.Outgoing || m.ParticipantID == b.Participant) {
				found = true
				break
			}
		}
		if !found {
			return nil
		}
	}
	c, err := e.lookupConversation(ctx, m.ConversationID)
	if err != nil {
		return err
	}
	if !s.Allows(c) || (!m.Outgoing && m.ParticipantID != c.ParticipantID) {
		return nil
	}
	if m.Interaction != nil {
		if source, ok := e.Transport.(transport.InteractionOwnerSource); ok {
			owner, ownerErr := source.InteractionOwner(c.ID, c.ParticipantID)
			if ownerErr != nil {
				return ownerErr
			}
			snapshot := *m.Interaction
			snapshot.OwnerID = owner
			m.Interaction = &snapshot
		}
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	current, err := e.InboxSettings()
	if err != nil {
		return err
	}
	consent = e.captureConsent.Load()
	if consent == nil || consent.epoch != epoch || !current.Allows(c) {
		return nil
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	dir := "incoming"
	if m.Outgoing {
		dir = "outgoing"
	}
	a, archiveErr := e.Store.Archive(c.ID)
	if archiveErr != nil {
		if !errors.Is(archiveErr, sql.ErrNoRows) {
			return archiveErr
		}
		a = store.Archive{Conversation: c, State: "queued", Generation: 1}
	}
	if a.Conversation.ParticipantID != c.ParticipantID {
		return errors.New("archive_identity_changed")
	}
	if err = e.Store.SaveArchive(a, current.ArchiveRetentionDays); err != nil {
		return err
	}
	return e.Store.SaveObservedFiles(c.ID, m.ID, m.TransactionID, dir, m.Text, "phone", m.Status, m.At, m.Attachments, m.Interaction)
}
func (e *Engine) InboxPage(ctx context.Context, conv, cursor, search string, limit int) (store.InboxPage, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	e.mu.Lock()
	s, err := e.InboxSettings()
	e.mu.Unlock()
	if err != nil {
		return store.InboxPage{}, err
	}
	if !s.Enabled {
		return store.InboxPage{}, errors.New("inbox_access_disabled")
	}
	var c transport.Conversation
	if a, archiveErr := e.Store.Archive(conv); archiveErr == nil {
		c = a.Conversation
	} else {
		c, err = e.lookupConversation(ctx, conv)
		if err != nil {
			return store.InboxPage{}, err
		}
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	current, err := e.InboxSettings()
	if err != nil {
		return store.InboxPage{}, err
	}
	if !current.Allows(c) || current.Revision != s.Revision {
		return store.InboxPage{}, errors.New("inbox_access_disabled")
	}
	cfg, err := e.Configuration()
	if err != nil {
		return store.InboxPage{}, err
	}
	if _, err = e.Store.Retention(cfg.Settings.RetentionDays); err != nil {
		return store.InboxPage{}, err
	}
	return e.Store.OwnerMessagePage(conv, cursor, search, limit)
}
func (e *Engine) ImportInboxHistory(ctx context.Context, conv, cursor string) (map[string]any, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	e.mu.Lock()
	s, err := e.InboxSettings()
	e.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if !s.Enabled || !s.LoadOlder {
		return nil, errors.New("older_history_disabled")
	}
	c, err := e.lookupConversation(ctx, conv)
	if err != nil || !s.Allows(c) {
		return nil, errors.New("inbox_access_disabled")
	}
	source, ok := e.Transport.(transport.HistorySource)
	if !ok {
		return nil, errors.New("history_unavailable")
	}
	snapshot, err := e.Store.ChangeWatermark(conv)
	if err != nil {
		return nil, err
	}
	page, err := source.FetchHistory(ctx, conv, c.ParticipantID, cursor)
	if err != nil {
		return nil, err
	}
	verified, err := e.lookupConversation(ctx, conv)
	if err != nil || verified.ParticipantID != c.ParticipantID || !s.Allows(verified) {
		return nil, errors.New("conversation_identity_changed")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	current, err := e.InboxSettings()
	if err != nil {
		return nil, err
	}
	if current.Revision != s.Revision || !current.LoadOlder || !current.Allows(verified) {
		return nil, errors.New("inbox_access_disabled")
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	a, archiveErr := e.Store.Archive(conv)
	if archiveErr != nil {
		if !errors.Is(archiveErr, sql.ErrNoRows) {
			return nil, archiveErr
		}
		a = store.Archive{Conversation: verified, State: "queued", Generation: 1}
	}
	if err = e.Store.SaveArchive(a, current.ArchiveRetentionDays); err != nil {
		return nil, err
	}
	count, duplicates, skipped := 0, 0, 0
	for _, m := range page.Messages {
		changed, checkErr := e.Store.MessageChangedSince(conv, m.ID, m.TransactionID, snapshot)
		if checkErr != nil {
			return nil, checkErr
		}
		if changed {
			skipped++
			continue
		}
		if current.ArchiveRetentionDays > 0 && m.At.Before(e.now().AddDate(0, 0, -current.ArchiveRetentionDays)) {
			skipped++
			continue
		}
		var known int
		if err = e.Store.DB.QueryRow("SELECT count(*) FROM message_alias WHERE alias=?", store.Hash(conv+"\x00"+m.ID)).Scan(&known); err != nil {
			return nil, err
		}
		if err = e.Store.SaveObservedFiles(conv, m.ID, m.TransactionID, m.Direction, m.Text, "history", m.Status, m.At, m.Attachments, m.Interaction); err != nil {
			return nil, err
		}
		if known == 0 {
			count++
		} else {
			duplicates++
		}
	}
	return map[string]any{"imported": count, "duplicates": duplicates, "skipped": skipped, "next_cursor": page.NextCursor, "available_pages_exhausted": page.NextCursor == "", "text_only": false}, nil
}

// A repeat acknowledges the original intent without creating another job,
// even if limits or global pause changed after the lost acknowledgement.
func (e *Engine) existingCompose(id, conv, text string, files []media.File, replyTarget ...string) (bool, error) {
	var n int
	if err := e.Store.DB.QueryRow("SELECT count(*) FROM jobs WHERE id=? AND conversation=? AND command='manual'", id, conv).Scan(&n); err != nil {
		return false, err
	}
	if n == 0 {
		return false, nil
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
	settings, err := e.InboxSettings()
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
	s, settingsErr := e.InboxSettings()
	consent := e.captureConsent.Load()
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
	if !s.Enabled || consent == nil {
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
	current, err := e.InboxSettings()
	if err != nil {
		return "", err
	}
	liveConsent := e.captureConsent.Load()
	if liveConsent == nil || liveConsent.epoch != consent.epoch || !current.Allows(c) {
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
