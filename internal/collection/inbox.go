package collection

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/deusical/smsbot-server/internal/history"
	"github.com/deusical/smsbot-server/internal/store"
	"github.com/deusical/smsbot-server/internal/transport"
	"time"
)

// SetSettings validates and saves new preferences. Selected conversations are
// verified by exact participant first. Under the control lock it re-checks the
// revision, saves, publishes the new consent, and runs SettingsSaved.
func (svc *Service) SetSettings(ctx context.Context, s Settings) error {
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
	svc.mu.Lock()
	old, err := svc.Settings()
	svc.mu.Unlock()
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
			c, err := svc.lookup(ctx, b.Conversation)
			if err != nil || c.Group || !c.Writable || c.ParticipantID != b.Participant {
				return errors.New("conversation_identity_changed")
			}
		}
	}
	svc.mu.Lock()
	defer svc.mu.Unlock()
	if err = ctx.Err(); err != nil {
		return err
	}
	current, err := svc.Settings()
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
	if err = svc.Store.SetInboxPreferences(b, !s.AutomaticReplies, s.ArchiveRetentionDays); err != nil {
		return err
	}
	if svc.SettingsSaved != nil {
		svc.SettingsSaved(s)
	}
	svc.publishConsent(s)
	if s.Enabled && svc.History != nil {
		svc.History.Wake()
	}
	return nil
}

func (svc *Service) Observe(ctx context.Context, m transport.Incoming) error {
	return svc.observe(ctx, m, 0)
}
func (svc *Service) observe(ctx context.Context, m transport.Incoming, epoch uint64) error {
	ctx = transport.WithPhonePurpose(ctx, "capture")
	if m.Outgoing && m.Interaction != nil {
		m.Historical = true
	}
	if m.ID == "" || m.ConversationID == "" || (m.Text == "" && len(m.Attachments) == 0) || (!m.Historical && svc.now().Sub(m.At) > 2*time.Minute) || m.At.After(svc.now().Add(5*time.Second)) {
		return nil
	}
	resource := store.Hash(m.ConversationID + "\x00" + m.ID)
	release, err := svc.captureReads.acquire(ctx, resource)
	if err != nil {
		return err
	}
	defer release()
	svc.mu.Lock()
	s, err := svc.Settings()
	consent := svc.consent.Load()
	svc.mu.Unlock()
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
	connection := svc.captureConnectionEpoch()
	digest, err := svc.captureDigest(m, consent)
	if err != nil {
		return err
	}
	mutation, err := svc.Store.ActiveMutation(m.ConversationID, m.ID)
	if err != nil {
		return err
	}
	// Outgoing aliases and active mutation reconciliation may have changed
	// independently of message contents. Always run their normal verification.
	if !m.Outgoing && mutation == "" && svc.captureReads.matches(resource, digest) && connection == svc.captureConnectionEpoch() {
		return nil
	}
	c, err := svc.lookup(ctx, m.ConversationID)
	if err != nil {
		return err
	}
	if !s.Allows(c) || (!m.Outgoing && m.ParticipantID != c.ParticipantID) {
		return nil
	}
	if m.Interaction != nil {
		if source, ok := svc.Transport.(transport.InteractionOwnerSource); ok {
			owner, ownerErr := source.InteractionOwner(c.ID, c.ParticipantID)
			if ownerErr != nil {
				return ownerErr
			}
			snapshot := *m.Interaction
			snapshot.OwnerID = owner
			m.Interaction = &snapshot
		}
	}
	svc.mu.Lock()
	defer svc.mu.Unlock()
	current, err := svc.Settings()
	if err != nil {
		return err
	}
	consent = svc.consent.Load()
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
	if err = svc.ensureArchive(c, current.ArchiveRetentionDays); err != nil {
		return err
	}
	if err := svc.Store.SaveObservedFiles(c.ID, m.ID, m.TransactionID, dir, m.Text, "phone", m.Status, m.At, m.Attachments, m.Interaction); err != nil {
		return err
	}
	// Saving may change local message state. Bind the reusable result to the
	// resulting watermark so history, file or receipt updates invalidate it.
	if digest, err = svc.captureDigest(m, consent); err == nil && connection == svc.captureConnectionEpoch() {
		svc.captureReads.remember(resource, digest)
	}
	return nil
}
func (svc *Service) InboxPage(ctx context.Context, conv, cursor, search string, limit int) (store.InboxPage, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	svc.mu.Lock()
	s, err := svc.Settings()
	svc.mu.Unlock()
	if err != nil {
		return store.InboxPage{}, err
	}
	if !s.Enabled {
		return store.InboxPage{}, errors.New("inbox_access_disabled")
	}
	var c transport.Conversation
	if a, archiveErr := svc.Store.Archive(conv); archiveErr == nil {
		c = a.Conversation
	} else {
		c, err = svc.lookup(ctx, conv)
		if err != nil {
			return store.InboxPage{}, err
		}
	}
	svc.mu.Lock()
	defer svc.mu.Unlock()
	current, err := svc.Settings()
	if err != nil {
		return store.InboxPage{}, err
	}
	if !current.Allows(c) || current.Revision != s.Revision {
		return store.InboxPage{}, errors.New("inbox_access_disabled")
	}
	days, err := svc.messageRetention()
	if err != nil {
		return store.InboxPage{}, err
	}
	if _, err = svc.Store.Retention(days); err != nil {
		return store.InboxPage{}, err
	}
	return svc.Store.OwnerMessagePage(conv, cursor, search, limit)
}
func (svc *Service) ImportInboxHistory(ctx context.Context, conv, cursor string) (map[string]any, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	svc.mu.Lock()
	s, err := svc.Settings()
	svc.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if !s.Enabled || !s.LoadOlder {
		return nil, errors.New("older_history_disabled")
	}
	source, ok := svc.Transport.(transport.HistorySource)
	if !ok {
		return nil, errors.New("history_unavailable")
	}
	// Collection consent is this route's grant; enrollment plays no part.
	collected := history.Access{Allows: s.Allows, Denied: errors.New("inbox_access_disabled")}
	verified, page, snapshot, err := history.ReadPage(ctx, svc.Transport, svc.Store, source, conv, cursor, collected)
	if err != nil {
		return nil, err
	}
	svc.mu.Lock()
	defer svc.mu.Unlock()
	current, err := svc.Settings()
	if err != nil {
		return nil, err
	}
	if current.Revision != s.Revision || !current.LoadOlder || !current.Allows(verified) {
		return nil, errors.New("inbox_access_disabled")
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if err = svc.ensureArchive(verified, current.ArchiveRetentionDays); err != nil {
		return nil, err
	}
	r, err := svc.persistHistoryPage(ctx, conv, page.Messages, time.Time{}, history.Policy{Snapshot: &snapshot, Retention: current.ArchiveRetentionDays}, true)
	if err != nil {
		return nil, err
	}
	return map[string]any{"imported": r.imported, "duplicates": r.duplicates + r.updated, "skipped": r.skipped, "next_cursor": page.NextCursor, "available_pages_exhausted": page.NextCursor == "", "text_only": false}, nil
}

// messageRetention is the bot's message retention in days, or zero.
func (svc *Service) messageRetention() (int, error) {
	if svc.MessageRetentionDays == nil {
		return 0, nil
	}
	return svc.MessageRetentionDays()
}
