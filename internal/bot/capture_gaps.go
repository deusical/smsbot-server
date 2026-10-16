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

type pendingCaptureGap struct {
	gap   store.CaptureGap
	epoch uint64
}

// Callback-safe: only bounded metadata aggregation; no SQL, RPC, or message text.
func (e *Engine) noteCaptureGap(m transport.Incoming, epoch uint64, reason string) {
	now := e.now()
	at := m.At
	if at.IsZero() || at.After(now) {
		at = now
	}
	g := store.CaptureGap{Conversation: m.ConversationID, Since: at.Add(-2 * time.Minute).UTC().Format(time.RFC3339Nano), Until: now.UTC().Format(time.RFC3339Nano), Reason: reason, State: "queued"}
	e.gapMu.Lock()
	defer e.gapMu.Unlock()
	if e.pendingGaps == nil {
		e.pendingGaps = map[string]pendingCaptureGap{}
	}
	if len(e.pendingGaps) >= 512 {
		if _, ok := e.pendingGaps[g.Conversation]; !ok {
			g.Conversation = ""
			g.State = "scope_unknown"
			g.Reason = "capture_scope_unknown"
		}
	}
	if previous, ok := e.pendingGaps[g.Conversation]; ok && previous.epoch == epoch {
		g = mergeCaptureGap(previous.gap, g)
	}
	e.pendingGaps[g.Conversation] = pendingCaptureGap{g, epoch}
}
func mergeCaptureGap(old, next store.CaptureGap) store.CaptureGap {
	if old.State == "checked_available" {
		next.Generation = old.Generation + 1
		return next
	}
	if old.Since < next.Since {
		next.Since = old.Since
	}
	if old.Until > next.Until {
		next.Until = old.Until
	}
	next.Generation = old.Generation + 1
	next.Cursor = ""
	next.CheckedAt = ""
	next.RetryAt = ""
	next.Failures = 0
	return next
}
func (e *Engine) flushCaptureGaps() {
	e.gapFlushMu.Lock()
	defer e.gapFlushMu.Unlock()
	e.gapMu.Lock()
	pending := e.pendingGaps
	e.flushingGaps = pending
	defer func() { e.gapMu.Lock(); e.flushingGaps = nil; e.gapMu.Unlock() }()
	e.pendingGaps = map[string]pendingCaptureGap{}
	e.gapMu.Unlock()
	if len(pending) == 0 {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	saved, err := e.Store.CaptureGaps()
	for key, item := range pending {
		consent := e.captureConsent.Load()
		if consent == nil || !consent.settings.Enabled || consent.epoch != item.epoch {
			continue
		}
		g := item.gap
		if err == nil {
			for _, old := range saved {
				if old.Conversation == key {
					g = mergeCaptureGap(old, g)
					break
				}
			}
		}
		if err != nil || e.Store.SaveCaptureGap(g) != nil {
			e.gapMu.Lock()
			if newer, ok := e.pendingGaps[key]; ok {
				newer.gap = mergeCaptureGap(g, newer.gap)
				e.pendingGaps[key] = newer
			} else {
				e.pendingGaps[key] = item
			}
			e.gapMu.Unlock()
		}
	}
}
func (e *Engine) CaptureGapStatus() ([]store.CaptureGap, bool) {
	gaps, err := e.Store.CaptureGaps()
	e.gapMu.Lock()
	combined := make(map[string]pendingCaptureGap, len(e.pendingGaps)+len(e.flushingGaps))
	for key, item := range e.flushingGaps {
		combined[key] = item
	}
	for key, item := range e.pendingGaps {
		if old, ok := combined[key]; ok {
			item.gap = mergeCaptureGap(old.gap, item.gap)
		}
		combined[key] = item
	}
	for key, item := range combined {
		found := false
		for i := range gaps {
			if gaps[i].Conversation == key {
				gaps[i] = mergeCaptureGap(gaps[i], item.gap)
				found = true
				break
			}
		}
		if !found {
			gaps = append(gaps, item.gap)
		}
	}
	e.gapMu.Unlock()
	unknown := err != nil
	for _, g := range gaps {
		if g.Conversation == "" && g.State != "checked_available" {
			unknown = true
		}
	}
	return gaps, unknown
}

func (h archiveHost) RepairCaptureGap(ctx context.Context) error {
	e := h.e
	e.flushCaptureGaps()
	if !e.Transport.Ready() {
		return nil
	}
	e.mu.Lock()
	gaps, err := e.Store.CaptureGaps()
	settings, settingsErr := e.InboxSettings()
	e.mu.Unlock()
	if settingsErr != nil {
		return settingsErr
	}
	if !settings.Enabled {
		return nil
	}
	if err != nil {
		return err
	}
	// Independent durable cursor; one page per tick, oldest unchecked first.
	var selected *store.CaptureGap
	for i := range gaps {
		g := &gaps[i]
		allowed := settings.Scope == "all"
		for _, binding := range settings.Conversations {
			if binding.Conversation == g.Conversation {
				allowed = true
				break
			}
		}
		if !allowed {
			continue
		}
		if g.Conversation == "" || g.State == "checked_available" || !store.GapRetryReady(*g) {
			continue
		}
		if selected == nil || g.RetryAt < selected.RetryAt || (g.RetryAt == selected.RetryAt && g.Since < selected.Since) {
			selected = g
		}
	}
	if selected == nil {
		return nil
	}
	g := *selected
	call, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	c, err := e.lookupConversation(call, g.Conversation)
	var policy history.Policy
	if err == nil {
		policy, err = h.Policy(c)
	}
	var a store.Archive
	if err == nil {
		e.mu.Lock()
		existing, lookupErr := e.Store.Archive(c.ID)
		a = existing
		if errors.Is(lookupErr, sql.ErrNoRows) {
			a = store.Archive{Conversation: c, Generation: 1, State: "queued"}
		}
		e.mu.Unlock()
		if lookupErr == nil && existing.Conversation.ParticipantID != c.ParticipantID {
			err = errors.New("archive_identity_changed")
		}
		if lookupErr != nil && !errors.Is(lookupErr, sql.ErrNoRows) {
			err = lookupErr
		}
	}
	if err != nil {
		return e.finishGapAttempt(g, err)
	}
	since, err := time.Parse(time.RFC3339Nano, g.Since)
	if err != nil {
		return err
	}
	until, err := time.Parse(time.RFC3339Nano, g.Until)
	if err != nil {
		return err
	}
	policy.Since = &since
	snapshot, err := e.Store.ChangeWatermark(c.ID)
	if err != nil {
		return err
	}
	policy.Snapshot = &snapshot
	cursor := g.Cursor
	if cursor == "" {
		if source, ok := e.Transport.(transport.HistoryRangeSource); ok {
			cursor = source.HistoryCursorBefore(until.Add(time.Millisecond))
		}
	}
	g.State = "checking"
	if err = e.finishGapAttempt(g, nil); err != nil {
		return err
	}
	page, fetchErr := h.Fetch(call, c, cursor)
	if fetchErr != nil {
		return e.finishGapAttempt(g, fetchErr)
	}
	done := page.NextCursor == "" || page.NextCursor == cursor
	for _, m := range page.Messages {
		if m.At.Before(since) {
			done = true
		}
	}
	g.Cursor = page.NextCursor
	g.State = "checking"
	if done {
		g.State = "checked_available"
		g.CheckedAt = e.now().UTC().Format(time.RFC3339Nano)
		g.Cursor = ""
	}
	persist, stop := context.WithTimeout(ctx, 5*time.Second)
	defer stop()
	// Commit performs consent, revision, participant and generation checks again.
	if err = h.Commit(persist, a, policy, page, false); err != nil {
		return e.finishGapAttempt(*selected, err)
	}
	return e.finishGapAttempt(g, nil)
}
func (e *Engine) finishGapAttempt(g store.CaptureGap, attemptErr error) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	current, err := e.Store.CaptureGaps()
	if err != nil {
		return err
	}
	for _, saved := range current {
		if saved.Conversation == g.Conversation && saved.Generation != g.Generation {
			return nil
		}
	}
	if attemptErr != nil {
		g.State = "retrying"
		g.Failures++
		g.RetryAt = e.now().Add(time.Duration(1<<min(g.Failures, 6)) * 5 * time.Second).UTC().Format(time.RFC3339Nano)
	} else {
		g.Failures = 0
		g.RetryAt = e.now().Add(3 * time.Second).UTC().Format(time.RFC3339Nano)
	}
	return e.Store.SaveCaptureGap(g)
}

func captureFailureReason(err error) string {
	if errors.Is(err, transport.ErrUnavailable) {
		return "phone_unavailable"
	}
	return "capture_check_failed"
}
