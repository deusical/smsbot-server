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
	until := now
	kind := "live_capture"
	if m.Historical && !m.At.IsZero() && !m.At.After(now) {
		// An edit/status update retains the original message timestamp. Recheck
		// its neighborhood, rather than inventing an outage from then to now.
		kind = "historical_update"
		until = at.Add(2 * time.Minute)
		if until.After(now) {
			until = now
		}
	}
	g := store.CaptureGap{Kind: kind, ObservedAt: now.UTC().Format(time.RFC3339Nano), Conversation: m.ConversationID, Since: at.Add(-2 * time.Minute).UTC().Format(time.RFC3339Nano), Until: until.UTC().Format(time.RFC3339Nano), Reason: reason, State: "queued"}
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
	if old.Kind != next.Kind {
		next.Kind = "mixed"
	}
	oldSince, _ := time.Parse(time.RFC3339Nano, old.Since)
	nextSince, _ := time.Parse(time.RFC3339Nano, next.Since)
	oldUntil, _ := time.Parse(time.RFC3339Nano, old.Until)
	nextUntil, _ := time.Parse(time.RFC3339Nano, next.Until)
	if oldSince.Before(nextSince) {
		next.Since = old.Since
	}
	if oldUntil.After(nextUntil) {
		next.Until = old.Until
	}
	next.Generation = old.Generation + 1
	next.Pages, next.Checked = 0, 0
	next.LastProgressAt = ""
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
	// Also reconcile imports completed before this worker/software restarted.
	// Identity and enabled-access checks above still apply.
	if a.State == "available_pages_exhausted" || a.State == "cutoff_reached" {
		e.mu.Lock()
		current, loadErr := e.Store.Archive(c.ID)
		latest, policyErr := e.InboxSettings()
		resolved := false
		if policyErr == nil && latest.Revision == policy.Revision && latest.Allows(c) && loadErr == nil && current.Generation == a.Generation && current.Conversation.ParticipantID == c.ParticipantID {
			err = e.finishCaptureGapsFromImport(current, policy.Retention, policy.Revision)
			if err == nil {
				saved, readErr := e.Store.CaptureGaps()
				err = readErr
				for _, item := range saved {
					if item.Conversation == g.Conversation && item.State == "checked_available" {
						resolved = true
					}
				}
			}
		}
		e.mu.Unlock()
		if err != nil {
			return e.finishGapAttempt(g, err)
		}
		if resolved {
			return nil
		}
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
	g.Pages++
	g.Checked += len(page.Messages)
	g.LastProgressAt = e.now().UTC().Format(time.RFC3339Nano)
	return e.finishGapAttempt(g, nil)
}

// Caller holds e.mu. A full import started after a failure also repairs that
// failure; do not leave an independent warning running after the same coverage
// was successfully re-read. Never resolve failures observed after import start.
func (e *Engine) finishCaptureGapsFromImport(a store.Archive, retention int, revision int64) error {
	if a.ScanRevision == 0 || a.ScanRevision != revision {
		return nil
	}
	started, err := time.Parse(time.RFC3339Nano, a.RequestedAt)
	if err != nil {
		return nil
	}
	cutoff, err := store.ParseArchiveSince(a.Since)
	if err != nil {
		return err
	}
	if retention > 0 {
		retainedSince := e.now().AddDate(0, 0, -retention)
		if cutoff.IsZero() || retainedSince.After(cutoff) {
			cutoff = retainedSince
		}
	}
	gaps, err := e.Store.CaptureGaps()
	if err != nil {
		return err
	}
	for _, g := range gaps {
		if g.Conversation != a.Conversation.ID || g.State == "checked_available" {
			continue
		}
		until, parseErr := time.Parse(time.RFC3339Nano, g.Until)
		if parseErr != nil || until.After(started) {
			continue
		}
		if g.ObservedAt != "" {
			observed, parseErr := time.Parse(time.RFC3339Nano, g.ObservedAt)
			if parseErr != nil || observed.After(started) {
				continue
			}
		}
		since, parseErr := time.Parse(time.RFC3339Nano, g.Since)
		if parseErr != nil || (!cutoff.IsZero() && cutoff.After(since)) {
			continue
		}
		g.State = "checked_available"
		g.CheckedAt = e.now().UTC().Format(time.RFC3339Nano)
		g.LastProgressAt = g.CheckedAt
		g.Generation++ // an in-flight range repair must not overwrite completion
		g.Cursor, g.RetryAt = "", ""
		g.Failures = 0
		if err = e.Store.SaveCaptureGap(g); err != nil {
			return err
		}
	}
	return nil
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
