package bot

import (
	"context"
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
	AttachmentLimits *media.Limits  `json:"attachment_limits,omitempty"`
	Enabled          bool           `json:"enabled"`
	Scope            string         `json:"scope"`
	Conversations    []InboxBinding `json:"conversations"`
	LoadOlder        bool           `json:"load_older"`
	AutomaticReplies bool           `json:"automatic_replies"`
	Revision         int64          `json:"revision"`
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
	if err = e.Store.SetInboxPreferences(b, !s.AutomaticReplies); err != nil {
		return err
	}
	if !s.AutomaticReplies {
		for _, cancel := range e.pending {
			cancel()
		}
	}
	e.publishCaptureConsent(s)
	return nil
}

// Receive separates optional owner inbox storage from bot routing. Pause,
// quiet hours and AI preferences never prevent an opted-in inbox observation.
func (e *Engine) Receive(ctx context.Context, m transport.Incoming) (bool, error) {
	if err := e.Observe(ctx, m); err != nil {
		e.captureGap.Store(true)
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
	if m.Historical || m.ID == "" || m.ConversationID == "" || (m.Text == "" && len(m.Attachments) == 0) || utf8.RuneCountInString(m.Text) > 12000 || e.now().Sub(m.At) > 2*time.Minute || m.At.After(e.now().Add(5*time.Second)) {
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
	return e.Store.SaveObservedFiles(c.ID, m.ID, m.TransactionID, dir, m.Text, "phone", m.Status, m.At, m.Attachments)
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
	c, err := e.lookupConversation(ctx, conv)
	if err != nil {
		return store.InboxPage{}, err
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
	cfg, err := e.Configuration()
	if err != nil {
		return nil, err
	}
	count := 0
	for _, m := range page.Messages {
		if cfg.Settings.RetentionDays > 0 && m.At.Before(e.now().AddDate(0, 0, -cfg.Settings.RetentionDays)) {
			continue
		}
		if err = e.Store.SaveObservedFiles(conv, m.ID, m.TransactionID, m.Direction, m.Text, "history", m.Status, m.At, m.Attachments); err != nil {
			return nil, err
		}
		count++
	}
	return map[string]any{"imported": count, "next_cursor": page.NextCursor, "available_pages_exhausted": page.NextCursor == "", "text_only": false}, nil
}

// A repeat acknowledges the original intent without creating another job,
// even if limits or global pause changed after the lost acknowledgement.
func (e *Engine) existingCompose(id, conv, text string, files []media.File) (bool, error) {
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
	if string(digest) != media.Intent(text, files) {
		return false, errors.New("request_id_reused_with_different_message")
	}
	return true, nil
}
func (e *Engine) ComposeInbox(ctx context.Context, conv, text, requestID string) (string, error) {
	return e.ComposeInboxFiles(ctx, conv, text, requestID, nil)
}
func (e *Engine) ComposeInboxFiles(ctx context.Context, conv, text, requestID string, uploads []media.Upload) (string, error) {
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
	exists, err := e.existingCompose(id, conv, text, files)
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
	e.mu.Lock()
	defer e.mu.Unlock()
	exists, err = e.existingCompose(id, conv, text, files)
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
	_, err = e.Store.Enqueue(id, conv, "manual", store.Plan{Participant: c.ParticipantID, Reply: text, MaxChars: cfg.Settings.MaxChars, Manual: true, InboxOwnerOnly: true, Attachments: files})
	return id, err
}
