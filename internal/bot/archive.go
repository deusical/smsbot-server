package bot

import (
	"context"
	"database/sql"
	"errors"
	"github.com/deusical/smsbot-server/internal/history"
	"github.com/deusical/smsbot-server/internal/store"
	"github.com/deusical/smsbot-server/internal/transport"
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

// Commit publishes one history step. Consent and the step's checkpoint version
// are verified before any message is saved; the archive update then changes
// only the checkpoint owned by the step's workflow.
func (h archiveHost) Commit(ctx context.Context, c history.Commit) error {
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
	if s.Revision != c.Policy.Revision || !s.Allows(c.Conversation) {
		return errors.New("archive_access_changed")
	}
	current, err := e.Store.Archive(c.Conversation.ID)
	exists := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err = c.Check(current, exists); err != nil {
		return err
	}
	importPage := c.Work == history.ImportWork
	var result pageResult
	if c.Page != nil {
		since, _ := store.ParseArchiveSince(current.Since)
		if c.Policy.Since != nil {
			since = *c.Policy.Since
		}
		if result, err = e.persistHistoryPage(ctx, c.Conversation.ID, c.Page.Messages, since, c.Policy, importPage); err != nil {
			return err
		}
	}
	a, err := e.Store.UpdateArchive(c.Conversation.ID, c.Policy.Retention, func(a *store.Archive, exists bool) error {
		if err := c.ApplyTo(a, exists); err != nil {
			return err
		}
		result.addTo(a, importPage)
		return nil
	})
	if err != nil {
		return err
	}
	if c.Page != nil && importPage && (a.State == "available_pages_exhausted" || a.State == "cutoff_reached") {
		return e.finishCaptureGapsFromImport(a, c.Policy.Retention, c.Policy.Revision)
	}
	return nil
}

// pageResult counts how one history page changed local messages.
type pageResult struct {
	checked, skipped, imported, updated, duplicates int
	oldest                                          time.Time
}

// addTo adds the counts to the record. Only import pages count toward the
// import's progress; every page counts toward the shared totals.
func (r pageResult) addTo(a *store.Archive, importPage bool) {
	if importPage {
		a.Checked += r.checked
		a.Imported += r.imported
		a.Updated += r.updated
		a.Duplicates += r.duplicates
	}
	a.Skipped += r.skipped
	if !r.oldest.IsZero() {
		oldest, err := time.Parse(time.RFC3339Nano, a.Oldest)
		if err != nil || r.oldest.Before(oldest) {
			a.Oldest = r.oldest.UTC().Format("2006-01-02T15:04:05.000000000Z")
		}
	}
}

// persistHistoryPage saves a fetched page. Caller holds e.mu and has verified
// consent. A message changed locally after the fetch began (Policy.Snapshot)
// keeps its newer observation; messages outside since or retention are skipped.
func (e *Engine) persistHistoryPage(ctx context.Context, conv string, messages []transport.HistoricalMessage, since time.Time, p history.Policy, counted bool) (pageResult, error) {
	var r pageResult
	for _, m := range messages {
		if counted {
			r.checked++
		}
		if err := ctx.Err(); err != nil {
			return r, err
		}
		if p.Snapshot != nil {
			changed, err := e.Store.MessageChangedSince(conv, m.ID, m.TransactionID, *p.Snapshot)
			if err != nil {
				return r, err
			}
			if changed {
				r.skipped++
				continue
			}
		}
		if (!since.IsZero() && m.At.Before(since)) || (p.Retention > 0 && m.At.Before(e.now().AddDate(0, 0, -p.Retention))) {
			r.skipped++
			continue
		}
		var before int
		if err := e.Store.DB.QueryRow(`SELECT EXISTS(SELECT 1 FROM messages WHERE conversation=? AND (id=? OR id IN (SELECT message FROM message_alias WHERE alias=?) OR (?='outgoing' AND id=? AND EXISTS(SELECT 1 FROM jobs WHERE conversation=? AND txn=?))))`, conv, store.Hash(conv+"\x00"+m.ID), store.Hash(conv+"\x00"+m.ID), m.Direction, m.TransactionID, conv, m.TransactionID).Scan(&before); err != nil {
			return r, err
		}
		watermark, err := e.Store.ChangeWatermark(conv)
		if err != nil {
			return r, err
		}
		if err = e.Store.SaveObservedFiles(conv, m.ID, m.TransactionID, m.Direction, m.Text, "history", m.Status, m.At, m.Attachments, m.Interaction); err != nil {
			return r, err
		}
		if counted {
			changed, err := e.Store.MessageChangedSince(conv, m.ID, m.TransactionID, watermark)
			if err != nil {
				return r, err
			}
			if before == 0 {
				r.imported++
			} else if changed {
				r.updated++
			} else {
				r.duplicates++
			}
		}
		if r.oldest.IsZero() || m.At.Before(r.oldest) {
			r.oldest = m.At
		}
	}
	return r, nil
}

// ensureArchive records the verified identity of a conversation the owner
// allows and refreshes its retention. Caller holds e.mu. A record bound to a
// different participant is never reused.
func (e *Engine) ensureArchive(c transport.Conversation, retention int) error {
	_, err := e.Store.UpdateArchive(c.ID, retention, func(a *store.Archive, exists bool) error {
		if !exists {
			*a = store.NewArchive(c)
			return nil
		}
		if a.Conversation.ParticipantID != c.ParticipantID {
			return errors.New("archive_identity_changed")
		}
		return nil
	})
	return err
}

func activeArchiveState(state string) bool {
	return state == "queued" || state == "running" || state == "retrying"
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
		s, err := e.InboxSettings()
		if err != nil {
			return store.Archive{}, err
		}
		return e.Store.UpdateArchive(conv, s.ArchiveRetentionDays, func(a *store.Archive, exists bool) error {
			if !exists {
				return sql.ErrNoRows
			}
			a.State = "cancelled"
			a.Generation++
			return nil
		})
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
	now := e.now().UTC().Format(time.RFC3339Nano)
	wake := false
	a, err := e.Store.UpdateArchive(conv, s.ArchiveRetentionDays, func(a *store.Archive, exists bool) error {
		if exists && a.Since == since && activeArchiveState(a.State) {
			if a.RequestedAt == "" {
				// Promote the existing automatic job without erasing its checkpoint.
				a.RequestSource = "owner"
				a.RequestedAt = now
				a.LastAttemptAt = ""
				a.Generation++
				wake = true
			}
			return nil
		}
		// Restart only the import; view and recent checkpoints are independent.
		// The new generation rejects an import page fetched for the old request.
		a.Conversation = c
		a.Generation++
		a.ImportCheckpoint = store.ImportCheckpoint{Since: since, State: "queued", ScanRevision: s.Revision, RequestSource: "owner", RequestedAt: now}
		a.Skipped = 0
		wake = true
		return nil
	})
	if err == nil && wake && e.History != nil {
		e.History.Wake()
	}
	return a, err
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
		// Ordering hints are volatile phone state, not part of the saved identity.
		c.LastMessageAt, c.Unread, c.Pinned = "", false, false
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
// progress. The view generation rejects a page fetched for superseded bounds.
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
	cursor := ""
	if source, ok := e.Transport.(transport.HistoryRangeSource); ok {
		cursor = source.HistoryCursorBefore(until.Add(time.Millisecond))
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
	from = since.UTC().Format(time.RFC3339Nano)
	wake := false
	a, err := e.Store.UpdateArchive(conv, settings.ArchiveRetentionDays, func(a *store.Archive, exists bool) error {
		if !exists {
			*a = store.NewArchive(c)
		} else if a.Conversation.ParticipantID != c.ParticipantID {
			return errors.New("archive_identity_changed")
		}
		a.FocusAt = e.now().UTC().Format(time.RFC3339Nano)
		currentSince, _ := store.ParseArchiveSince(a.RefreshSince)
		currentUntil, _ := store.ParseArchiveSince(a.RefreshUntil)
		if activeArchiveState(a.RefreshState) && !since.Before(currentSince) && (to == "" || !until.After(currentUntil)) {
			return nil
		}
		if a.RefreshSince == from && (to == "" || a.RefreshUntil == until.UTC().Format(time.RFC3339Nano)) {
			checked, _ := time.Parse(time.RFC3339Nano, a.RefreshCheckedAt)
			if e.now().Sub(checked) < 30*time.Second {
				return nil
			}
		}
		a.ViewGeneration++
		a.RefreshSince = from
		a.RefreshUntil = until.UTC().Format(time.RFC3339Nano)
		a.RefreshState = "queued"
		a.RefreshError = ""
		a.RefreshRetryAt = ""
		a.RefreshFailures = 0
		a.RefreshCursor = cursor
		wake = true
		return nil
	})
	if err == nil && wake && e.History != nil {
		e.History.Wake()
	}
	return a, err
}

// Discovery continues page-by-page until a complete inbox pass is exhausted.
func (h archiveHost) DiscoveryPending() bool {
	cursor, err := h.e.Store.GetSecretSetting("archive_discovery", "archive-discovery")
	return err == nil && len(cursor) > 0
}
