package collection

import (
	"context"
	"database/sql"
	"errors"
	"github.com/deusical/smsbot-server/internal/archive"
	"github.com/deusical/smsbot-server/internal/history"
	"github.com/deusical/smsbot-server/internal/store"
	"github.com/deusical/smsbot-server/internal/transport"
	"time"
)

type pendingCaptureGap struct {
	gap   store.CaptureGap
	epoch uint64
}

// Callback-safe: only bounded metadata aggregation; no SQL, RPC, or message text.
func (svc *Service) noteCaptureGap(m transport.Incoming, epoch uint64, reason string) {
	now := svc.now()
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
	svc.gapMu.Lock()
	defer svc.gapMu.Unlock()
	if svc.pendingGaps == nil {
		svc.pendingGaps = map[string]pendingCaptureGap{}
	}
	if len(svc.pendingGaps) >= 512 {
		if _, ok := svc.pendingGaps[g.Conversation]; !ok {
			g.Conversation = ""
			g.State = "scope_unknown"
			g.Reason = "capture_scope_unknown"
		}
	}
	if previous, ok := svc.pendingGaps[g.Conversation]; ok && previous.epoch == epoch {
		g = mergeCaptureGap(previous.gap, g)
	}
	svc.pendingGaps[g.Conversation] = pendingCaptureGap{g, epoch}
	if svc.History != nil {
		defer svc.History.Wake()
	}
}
func mergeCaptureGap(old, next store.CaptureGap) store.CaptureGap {
	if store.GapSettled(old) {
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
func (svc *Service) flushCaptureGaps() {
	svc.gapFlushMu.Lock()
	defer svc.gapFlushMu.Unlock()
	svc.gapMu.Lock()
	pending := svc.pendingGaps
	svc.flushingGaps = pending
	defer func() { svc.gapMu.Lock(); svc.flushingGaps = nil; svc.gapMu.Unlock() }()
	svc.pendingGaps = map[string]pendingCaptureGap{}
	svc.gapMu.Unlock()
	if len(pending) == 0 {
		return
	}
	svc.mu.Lock()
	defer svc.mu.Unlock()
	saved, err := svc.Store.CaptureGaps()
	for key, item := range pending {
		consent := svc.consent.Load()
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
		if err != nil || svc.Store.SaveCaptureGap(g) != nil {
			svc.gapMu.Lock()
			if newer, ok := svc.pendingGaps[key]; ok {
				newer.gap = mergeCaptureGap(g, newer.gap)
				svc.pendingGaps[key] = newer
			} else {
				svc.pendingGaps[key] = item
			}
			svc.gapMu.Unlock()
		}
	}
}
func (svc *Service) CaptureGapStatus() ([]store.CaptureGap, bool) {
	gaps, err := svc.Store.CaptureGaps()
	svc.gapMu.Lock()
	combined := make(map[string]pendingCaptureGap, len(svc.pendingGaps)+len(svc.flushingGaps))
	for key, item := range svc.flushingGaps {
		combined[key] = item
	}
	for key, item := range svc.pendingGaps {
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
	svc.gapMu.Unlock()
	unknown := err != nil
	for _, g := range gaps {
		if g.Conversation == "" && !store.GapSettled(g) {
			unknown = true
		}
	}
	return gaps, unknown
}

// NextGapAttempt exposes retry eligibility, not merely a visible warning.
func (h HistoryHost) NextGapAttempt() (time.Time, bool) {
	svc := h.svc
	svc.gapMu.Lock()
	pending := len(svc.pendingGaps) > 0 || len(svc.flushingGaps) > 0
	svc.gapMu.Unlock()
	if pending {
		return time.Time{}, true
	}
	svc.mu.Lock()
	defer svc.mu.Unlock()
	settings, err := svc.Settings()
	if err != nil || !settings.Enabled {
		return time.Time{}, false
	}
	gaps, err := svc.Store.CaptureGaps()
	if err != nil {
		return time.Time{}, false
	}
	var next time.Time
	found := false
	for _, g := range gaps {
		if !settings.AllowsID(g.Conversation) || g.Conversation == "" || store.GapSettled(g) || g.State == "scope_unknown" {
			continue
		}
		at, _ := time.Parse(time.RFC3339Nano, g.RetryAt)
		if !found || at.Before(next) {
			next = at
			found = true
		}
	}
	return next, found
}

func (h HistoryHost) RepairCaptureGap(ctx context.Context) error {
	svc := h.svc
	svc.flushCaptureGaps()
	if !svc.Transport.Ready() {
		return nil
	}
	svc.mu.Lock()
	gaps, err := svc.Store.CaptureGaps()
	settings, settingsErr := svc.Settings()
	svc.mu.Unlock()
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
		if g.Conversation == "" || store.GapSettled(*g) || g.State == "scope_unknown" || !store.GapRetryReady(*g) {
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
	c, err := svc.lookup(call, g.Conversation)
	var policy history.Policy
	if err == nil {
		// The conversation is live but collection does not apply to it (a
		// group, read-only, or not selected): there is nothing to repair, and
		// retrying would only wake the phone.
		if settings, settingsErr := svc.Settings(); settingsErr == nil && !settings.Allows(c) {
			return svc.settleGap(g, "not_collected")
		}
		policy, err = h.Policy(c)
	}
	var a store.Archive
	if err == nil {
		svc.mu.Lock()
		existing, lookupErr := svc.Store.Archive(c.ID)
		a = existing
		if errors.Is(lookupErr, sql.ErrNoRows) {
			a = store.NewArchive(c)
		}
		svc.mu.Unlock()
		if lookupErr == nil && existing.Conversation.ParticipantID != c.ParticipantID {
			err = errors.New("archive_identity_changed")
		}
		if lookupErr != nil && !errors.Is(lookupErr, sql.ErrNoRows) {
			err = lookupErr
		}
	}
	if err != nil {
		return svc.finishGapAttempt(g, err)
	}
	// Also reconcile imports completed before this worker/software restarted.
	// Identity and enabled-access checks above still apply.
	if a.State == "available_pages_exhausted" || a.State == "cutoff_reached" {
		svc.mu.Lock()
		current, loadErr := svc.Store.Archive(c.ID)
		latest, policyErr := svc.Settings()
		resolved := false
		if policyErr == nil && latest.Revision == policy.Revision && latest.Allows(c) && loadErr == nil && current.Generation == a.Generation && current.Conversation.ParticipantID == c.ParticipantID {
			err = svc.finishCaptureGapsFromImport(current, policy.Retention, policy.Revision)
			if err == nil {
				saved, readErr := svc.Store.CaptureGaps()
				err = readErr
				for _, item := range saved {
					if item.Conversation == g.Conversation && item.State == "checked_available" {
						resolved = true
					}
				}
			}
		}
		svc.mu.Unlock()
		if err != nil {
			return svc.finishGapAttempt(g, err)
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
	snapshot, err := svc.Store.ChangeWatermark(c.ID)
	if err != nil {
		return err
	}
	policy.Snapshot = &snapshot
	cursor := g.Cursor
	if cursor == "" {
		if source, ok := svc.Transport.(transport.HistoryRangeSource); ok {
			cursor = source.HistoryCursorBefore(until.Add(time.Millisecond))
		}
	}
	g.State = "checking"
	if err = svc.finishGapAttempt(g, nil); err != nil {
		return err
	}
	page, fetchErr := h.Fetch(call, c, cursor)
	if fetchErr != nil {
		return svc.finishGapAttempt(g, fetchErr)
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
		g.CheckedAt = svc.now().UTC().Format(time.RFC3339Nano)
		g.Cursor = ""
	}
	persist, stop := context.WithTimeout(ctx, 5*time.Second)
	defer stop()
	// Commit performs consent, revision and participant checks again.
	if err = h.Commit(persist, history.Commit{Work: history.GapWork, Conversation: c, Policy: policy, Page: &page}); err != nil {
		return svc.finishGapAttempt(*selected, err)
	}
	g.Pages++
	g.Checked += len(page.Messages)
	g.LastProgressAt = svc.now().UTC().Format(time.RFC3339Nano)
	return svc.finishGapAttempt(g, nil)
}

// Caller holds the control lock. A full import started after a failure also repairs that
// failure; do not leave an independent warning running after the same coverage
// was successfully re-read. Never resolve failures observed after import start.
func (svc *Service) finishCaptureGapsFromImport(a store.Archive, retention int, revision int64) error {
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
		retainedSince := svc.now().AddDate(0, 0, -retention)
		if cutoff.IsZero() || retainedSince.After(cutoff) {
			cutoff = retainedSince
		}
	}
	gaps, err := svc.Store.CaptureGaps()
	if err != nil {
		return err
	}
	for _, g := range gaps {
		if g.Conversation != a.Conversation.ID || store.GapSettled(g) {
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
		g.CheckedAt = svc.now().UTC().Format(time.RFC3339Nano)
		g.LastProgressAt = g.CheckedAt
		g.Generation++ // an in-flight range repair must not overwrite completion
		g.Cursor, g.RetryAt = "", ""
		g.Failures = 0
		if err = svc.Store.SaveCaptureGap(g); err != nil {
			return err
		}
	}
	return nil
}

func (svc *Service) finishGapAttempt(g store.CaptureGap, attemptErr error) error {
	svc.mu.Lock()
	defer svc.mu.Unlock()
	current, err := svc.Store.CaptureGaps()
	if err != nil {
		return err
	}
	for _, saved := range current {
		if saved.Conversation == g.Conversation && saved.Generation != g.Generation {
			return nil
		}
	}
	if attemptErr != nil {
		// Transient failures retry quickly; sustained ones back off to daily,
		// like original files, instead of waking the phone every few minutes.
		g.State = "retrying"
		g.Failures++
		g.RetryAt = svc.now().Add(archive.RetryDelay(g.Failures)).UTC().Format(time.RFC3339Nano)
	} else {
		g.Failures = 0
		g.RetryAt = svc.now().Add(3 * time.Second).UTC().Format(time.RFC3339Nano)
	}
	return svc.Store.SaveCaptureGap(g)
}

func captureFailureReason(err error) string {
	if errors.Is(err, transport.ErrUnavailable) {
		return "phone_unavailable"
	}
	return "capture_check_failed"
}

// settleGap closes a gap that needs no repair, unless a newer failure
// replaced it meanwhile.
func (svc *Service) settleGap(g store.CaptureGap, state string) error {
	svc.mu.Lock()
	defer svc.mu.Unlock()
	current, err := svc.Store.CaptureGaps()
	if err != nil {
		return err
	}
	for _, saved := range current {
		if saved.Conversation == g.Conversation && saved.Generation != g.Generation {
			return nil
		}
	}
	g.State = state
	g.CheckedAt = svc.now().UTC().Format(time.RFC3339Nano)
	g.Cursor, g.RetryAt = "", ""
	g.Failures = 0
	return svc.Store.SaveCaptureGap(g)
}
