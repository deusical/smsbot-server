package bot

import (
	"context"
	"database/sql"
	"errors"
	"smsbot-server/internal/history"
	"smsbot-server/internal/store"
	"smsbot-server/internal/transport"
	"time"
)

// archiveHost adapts consent-checked reads to the history module. It exposes no
// command routing or outbound submission methods.
type archiveHost struct{ e *Engine }

func (h archiveHost) Discover(ctx context.Context) ([]transport.Conversation, error) {
	h.e.mu.Lock()
	settings, err := h.e.InboxSettings()
	h.e.mu.Unlock()
	if err != nil || !settings.Enabled {
		return nil, err
	}
	var list []transport.Conversation
	if source, ok := h.e.Transport.(transport.ConversationPageSource); ok {
		cursor, _ := h.e.Store.GetSecretSetting("archive_discovery", "archive-discovery")
		page, fetchErr := source.DiscoverConversations(ctx, string(cursor))
		if fetchErr != nil {
			if len(cursor) > 0 {
				if err = h.e.Store.SetSecretSetting("archive_discovery", "archive-discovery", nil); err != nil {
					return nil, err
				}
			}
			return nil, fetchErr
		}
		list = append(list, page.Conversations...)
		if err = h.e.Store.SetSecretSetting("archive_discovery", "archive-discovery", []byte(page.NextCursor)); err != nil {
			return nil, err
		}
	} else {
		list, err = h.e.Transport.Conversations(ctx)
	}
	existing, archiveErr := h.e.Store.Archives()
	if archiveErr != nil {
		return nil, archiveErr
	}
	seen := map[string]bool{}
	for _, c := range list {
		seen[c.ID] = true
	}
	for _, a := range existing {
		if !seen[a.Conversation.ID] {
			list = append(list, a.Conversation)
		}
	}

	return list, err
}
func (h archiveHost) Policy(c transport.Conversation) (history.Policy, error) {
	h.e.mu.Lock()
	defer h.e.mu.Unlock()
	s, err := h.e.InboxSettings()
	if err != nil {
		return history.Policy{}, err
	}
	if !s.Allows(c) {
		return history.Policy{}, errors.New("archive_access_disabled")
	}
	return history.Policy{Revision: s.Revision, Retention: s.ArchiveRetentionDays}, nil
}
func (h archiveHost) Fetch(ctx context.Context, c transport.Conversation, cursor string) (transport.HistoryPage, error) {
	source, ok := h.e.Transport.(transport.HistorySource)
	if !ok {
		return transport.HistoryPage{}, errors.New("history_unavailable")
	}
	return source.FetchHistory(ctx, c.ID, c.ParticipantID, cursor)
}
func (h archiveHost) Commit(ctx context.Context, a store.Archive, p history.Policy, page transport.HistoryPage, stateOnly bool) error {
	e := h.e
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	s, err := e.InboxSettings()
	if err != nil {
		return err
	}
	if s.Revision != p.Revision || !s.Allows(a.Conversation) {
		return errors.New("archive_access_changed")
	}
	current, lookupErr := e.Store.Archive(a.Conversation.ID)
	if lookupErr == nil && (current.Generation != a.Generation || current.Conversation.ParticipantID != a.Conversation.ParticipantID) {
		return errors.New("archive_job_changed")
	}
	since, _ := store.ParseArchiveSince(a.Since)
	if p.Since != nil {
		since = *p.Since
	}
	if !stateOnly {
		for _, m := range page.Messages {
			if p.Deep {
				a.Checked++
			}
			if err = ctx.Err(); err != nil {
				return err
			}
			if p.Snapshot != nil {
				changed, checkErr := e.Store.MessageChangedSince(a.Conversation.ID, m.ID, m.TransactionID, *p.Snapshot)
				if checkErr != nil {
					return checkErr
				}
				if changed {
					a.Skipped++
					continue
				}
			}
			if (!since.IsZero() && m.At.Before(since)) || (p.Retention > 0 && m.At.Before(e.now().AddDate(0, 0, -p.Retention))) {
				a.Skipped++
				continue
			}
			var before int
			if err = e.Store.DB.QueryRow(`SELECT EXISTS(SELECT 1 FROM messages WHERE conversation=? AND (id=? OR id IN (SELECT message FROM message_alias WHERE alias=?) OR (?='outgoing' AND id=? AND EXISTS(SELECT 1 FROM jobs WHERE conversation=? AND txn=?))))`, a.Conversation.ID, store.Hash(a.Conversation.ID+"\x00"+m.ID), store.Hash(a.Conversation.ID+"\x00"+m.ID), m.Direction, m.TransactionID, a.Conversation.ID, m.TransactionID).Scan(&before); err != nil {
				return err
			}
			watermark, watermarkErr := e.Store.ChangeWatermark(a.Conversation.ID)
			if watermarkErr != nil {
				return watermarkErr
			}
			if err = e.Store.SaveObservedFiles(a.Conversation.ID, m.ID, m.TransactionID, m.Direction, m.Text, "history", m.Status, m.At, m.Attachments); err != nil {
				return err
			}
			if p.Deep {
				changed, err := e.Store.MessageChangedSince(a.Conversation.ID, m.ID, m.TransactionID, watermark)
				if err != nil {
					return err
				}
				if before == 0 {
					a.Imported++
				} else if changed {
					a.Updated++
				} else {
					a.Duplicates++
				}
			}
			oldest, parseErr := time.Parse(time.RFC3339Nano, a.Oldest)
			if parseErr != nil || m.At.Before(oldest) {
				a.Oldest = m.At.UTC().Format("2006-01-02T15:04:05.000000000Z")
			}
		}
	}
	// A focus heartbeat changes no fetch bounds or generation. Preserve it
	// when an in-flight background page publishes its older snapshot.
	currentFocus, _ := time.Parse(time.RFC3339Nano, current.FocusAt)
	previousFocus, _ := time.Parse(time.RFC3339Nano, a.FocusAt)
	if currentFocus.After(previousFocus) {
		a.FocusAt = current.FocusAt
	}
	return e.Store.SaveArchive(a, p.Retention)
}

// RequestArchive starts a durable rescan. An identical in-flight request is
// idempotent; changing/cancelling increments the generation before any old fetch
// can commit. No network call occurs under the engine lock.
func (e *Engine) RequestArchive(ctx context.Context, conv, since string, cancel bool) (store.Archive, error) {
	if _, err := store.ParseArchiveSince(since); err != nil {
		return store.Archive{}, errors.New("invalid_since")
	}
	if cancel {
		e.mu.Lock()
		defer e.mu.Unlock()
		a, err := e.Store.Archive(conv)
		if err != nil {
			return a, err
		}
		a.State = "cancelled"
		a.Generation++
		s, err := e.InboxSettings()
		if err != nil {
			return a, err
		}
		return a, e.Store.SaveArchive(a, s.ArchiveRetentionDays)
	}
	c, err := e.lookupConversation(ctx, conv)
	if err != nil {
		return store.Archive{}, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	s, err := e.InboxSettings()
	if err != nil || !s.Allows(c) {
		return store.Archive{}, errors.New("archive_access_disabled")
	}
	a, err := e.Store.Archive(conv)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return a, err
	}
	if err == nil && a.Since == since && (a.State == "queued" || a.State == "running" || a.State == "retrying") {
		if a.RequestedAt == "" {
			// Promote the existing automatic job without erasing its checkpoint.
			a.RequestSource = "owner"
			a.RequestedAt = e.now().UTC().Format(time.RFC3339Nano)
			a.LastAttemptAt = ""
			a.Generation++
			return a, e.Store.SaveArchive(a, s.ArchiveRetentionDays)
		}
		return a, nil
	}
	generation := a.Generation + 1
	// Restart only the deep lane; an active view and recent checkpoints are
	// independent, and their old in-flight reads are guarded by the generation.
	a.Conversation = c
	a.Since = since
	a.State = "queued"
	a.RequestSource = "owner"
	a.RequestedAt = e.now().UTC().Format(time.RFC3339Nano)
	a.LastAttemptAt = ""
	a.LastProgressAt = ""
	a.Checked = 0
	a.Pages = 0
	a.Updated = 0
	a.ScannedThrough = ""
	a.Generation = generation
	a.Cursor = ""
	a.Imported = 0
	a.Duplicates = 0
	a.Skipped = 0
	a.Error = ""
	a.Failures = 0
	a.RetryAt = ""
	a.DeepCheckedAt = ""
	return a, e.Store.SaveArchive(a, s.ArchiveRetentionDays)
}
func (e *Engine) ArchiveConversations() ([]transport.Conversation, error) {
	list, err := e.Store.Archives()
	if err != nil {
		return nil, err
	}
	out := []transport.Conversation{}
	for _, a := range list {
		c := a.Conversation
		c.Enrolled = e.Store.Enrolled(c.ID, c.ParticipantID)
		c.Archived = true
		out = append(out, c)
	}
	return out, nil
}

func (h archiveHost) Prune(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	h.e.mu.Lock()
	defer h.e.mu.Unlock()
	settings, err := h.e.InboxSettings()
	if err != nil {
		return err
	}
	return h.e.Store.ArchiveRetention(settings.ArchiveRetentionDays)
}

// RequestArchiveRefresh checks a viewed time span without replacing deep-import
// progress. The generation rejects a page fetched for superseded view bounds.
func (e *Engine) RequestArchiveRefresh(ctx context.Context, conv, from, to string) (store.Archive, error) {
	since, err := store.ParseArchiveSince(from)
	if err != nil || since.IsZero() {
		return store.Archive{}, errors.New("invalid_range")
	}
	until := e.now()
	if to != "" {
		until, err = store.ParseArchiveSince(to)
	}
	if err != nil || until.Before(since) || until.After(e.now().Add(5*time.Minute)) {
		return store.Archive{}, errors.New("invalid_range")
	}
	c, err := e.lookupConversation(ctx, conv)
	if err != nil {
		return store.Archive{}, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return store.Archive{}, err
	}
	settings, err := e.InboxSettings()
	if err != nil || !settings.Allows(c) {
		return store.Archive{}, errors.New("archive_access_disabled")
	}
	a, err := e.Store.Archive(conv)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return a, err
	}
	if err == nil && a.Conversation.ParticipantID != c.ParticipantID {
		return a, errors.New("archive_identity_changed")
	}
	if errors.Is(err, sql.ErrNoRows) {
		a = store.Archive{Conversation: c, State: "queued", Generation: 1}
	}
	a.FocusAt = e.now().UTC().Format(time.RFC3339Nano)
	from = since.UTC().Format(time.RFC3339Nano)
	active := a.RefreshState == "queued" || a.RefreshState == "running" || a.RefreshState == "retrying"
	currentSince, _ := store.ParseArchiveSince(a.RefreshSince)
	currentUntil, _ := store.ParseArchiveSince(a.RefreshUntil)
	if active && !since.Before(currentSince) && (to == "" || !until.After(currentUntil)) {
		return a, e.Store.SaveArchive(a, settings.ArchiveRetentionDays)
	}
	if a.RefreshSince == from && (to == "" || a.RefreshUntil == until.UTC().Format(time.RFC3339Nano)) {
		checked, _ := time.Parse(time.RFC3339Nano, a.RefreshCheckedAt)
		if e.now().Sub(checked) < 30*time.Second {
			return a, e.Store.SaveArchive(a, settings.ArchiveRetentionDays)
		}
	}
	a.Generation++
	a.RefreshSince = from
	a.RefreshUntil = until.UTC().Format(time.RFC3339Nano)
	a.RefreshState = "queued"
	a.RefreshError = ""
	a.RefreshRetryAt = ""
	a.RefreshFailures = 0
	a.RefreshCursor = ""
	if source, ok := e.Transport.(transport.HistoryRangeSource); ok {
		a.RefreshCursor = source.HistoryCursorBefore(until.Add(time.Millisecond))
	}
	return a, e.Store.SaveArchive(a, settings.ArchiveRetentionDays)
}
