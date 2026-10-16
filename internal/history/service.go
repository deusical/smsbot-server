// Package history owns durable archive read jobs. It has no send or command API.
package history

import (
	"context"
	"database/sql"
	"errors"
	"smsbot-server/internal/store"
	"smsbot-server/internal/transport"
	"sync"
	"time"
)

type Policy struct {
	Deep bool

	Since     *time.Time
	Snapshot  *int64
	Revision  int64
	Retention int
}
type Host interface {
	Prune(context.Context) error
	Discover(context.Context) ([]transport.Conversation, error)
	Policy(transport.Conversation) (Policy, error)
	Fetch(context.Context, transport.Conversation, string) (transport.HistoryPage, error)
	Commit(context.Context, store.Archive, Policy, transport.HistoryPage, bool) error
}
type Service struct {
	backgroundIndex int
	fetchTimeout    time.Duration
	Store           *store.Store
	Host            Host
	tickMu          sync.Mutex
	recent          map[string]time.Time
}

func New(s *store.Store, h Host) *Service {
	return &Service{Store: s, Host: h, recent: map[string]time.Time{}, fetchTimeout: 20 * time.Second}
}
func (s *Service) Run(ctx context.Context) {
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			s.Tick(ctx)
			timer.Reset(3 * time.Second)
		}
	}
}
func (s *Service) Tick(ctx context.Context) {
	s.tickMu.Lock()
	defer s.tickMu.Unlock()
	if ctx.Err() != nil {
		return
	}
	_ = s.Host.Prune(ctx)
	// Active views do not wait for a discovery pass or a large inbox sweep.
	archives, _ := s.Store.Archives()
	var focus *store.Archive
	var focusedAt time.Time
	for i := range archives {
		a := &archives[i]
		active := a.RefreshState == "queued" || a.RefreshState == "running" || a.RefreshState == "retrying"
		at, _ := time.Parse(time.RFC3339Nano, a.FocusAt)
		if active && retryReady(a.RefreshRetryAt) && (focus == nil || at.After(focusedAt)) {
			focus = a
			focusedAt = at
		}
	}
	if focus != nil && !focus.Conversation.Group && focus.Conversation.Writable && focus.Conversation.ParticipantID != "" {
		if p, err := s.Host.Policy(focus.Conversation); err == nil {
			if s.refreshStep(ctx, *focus, p) != nil {
				return
			}
		}
	}
	// Explicit imports get one bounded page before routine discovery. Rotate by
	// last attempt so one large request cannot monopolize other owner requests.
	archives, _ = s.Store.Archives()
	var manual *store.Archive
	var manualPolicy Policy
	for i := range archives {
		a := &archives[i]
		explicit := a.RequestedAt != "" || a.RequestSource == "owner" || (a.RequestSource == "" && a.Since != "")
		if !explicit || !retryReady(a.RetryAt) || (a.State != "queued" && a.State != "running" && a.State != "retrying") {
			continue
		}
		if a.Conversation.Group || !a.Conversation.Writable || a.Conversation.ParticipantID == "" {
			continue
		}
		candidatePolicy, err := s.Host.Policy(a.Conversation)
		if err != nil {
			continue
		}
		if manual == nil || a.LastAttemptAt < manual.LastAttemptAt || (a.LastAttemptAt == manual.LastAttemptAt && a.RequestedAt < manual.RequestedAt) {
			manual = a
			manual.RequestSource = "owner"
			manualPolicy = candidatePolicy
		}
	}
	if manual != nil {
		_ = s.step(ctx, *manual, manualPolicy, manual.Cursor, false)
	}
	if repair, ok := s.Host.(interface{ RepairCaptureGap(context.Context) error }); ok {
		_ = repair.RepairCaptureGap(ctx)
	}
	call, cancel := context.WithTimeout(ctx, 20*time.Second)
	list, err := s.Host.Discover(call)
	cancel()
	if err != nil {
		return
	}
	// Visit one background conversation per turn; recent and deep lanes both
	// advance without making an active view wait behind the whole inbox.
	for turn := 0; turn < len(list); turn++ {
		c := list[s.backgroundIndex%len(list)]
		s.backgroundIndex++
		if ctx.Err() != nil {
			return
		}
		if c.Group || !c.Writable || c.ParticipantID == "" {
			continue
		}
		p, err := s.Host.Policy(c)
		if err != nil {
			continue
		}
		a, err := s.Store.Archive(c.ID)
		if err != nil {
			if !errors.Is(err, sql.ErrNoRows) {
				continue
			}
			a = store.Archive{Conversation: c, State: "queued", Generation: 1}
			if err = s.Host.Commit(ctx, a, p, transport.HistoryPage{}, true); err != nil {
				continue
			}
		}
		if a.Conversation.ParticipantID != c.ParticipantID {
			continue
		}
		// Recent reconciliation always gets a turn, even during a deep backfill.
		if retryReady(a.RecentRetryAt) && (a.RecentStarted != "" || time.Since(s.recent[c.ID]) > 30*time.Second) {
			s.recent[c.ID] = time.Now()
			if err = s.step(ctx, a, p, a.RecentCursor, true); err != nil {
				// Give new focus work a turn before another transport attempt.
				return
			}
		}
		a, err = s.Store.Archive(c.ID)
		if err != nil {
			continue
		}
		if a.State == "available_pages_exhausted" || a.State == "cutoff_reached" {
			checked, _ := time.Parse(time.RFC3339Nano, a.DeepCheckedAt)
			if a.DeepCheckedAt == "" {
				// Existing completed archives have no scan timestamp. Start the
				// new daily schedule without an immediate duplicate phone read.
				a.DeepCheckedAt = time.Now().UTC().Format(time.RFC3339Nano)
				if s.Host.Commit(ctx, a, p, transport.HistoryPage{}, true) != nil {
					return
				}
			} else if time.Since(checked) >= 24*time.Hour {
				a.State = "queued"
				a.RequestedAt = "" // Scheduled maintenance is not a new explicit request.
				a.RequestSource = "automatic"
				a.Checked, a.Pages, a.Updated, a.Imported, a.Duplicates, a.Skipped = 0, 0, 0, 0, 0, 0
				a.ScannedThrough, a.LastProgressAt = "", ""
				a.Cursor = ""
			}
		}
		if (manual == nil || manual.Conversation.ID != c.ID) && retryReady(a.RetryAt) && (a.State == "queued" || a.State == "running" || a.State == "retrying") {
			_ = s.step(ctx, a, p, a.Cursor, false)
		}
		return
	}
}
func (s *Service) step(ctx context.Context, a store.Archive, p Policy, cursor string, recent bool) error {
	call, cancel := context.WithTimeout(ctx, s.fetchTimeout)
	defer cancel()
	snapshot, err := s.Store.ChangeWatermark(a.Conversation.ID)
	if err != nil {
		return err
	}
	p.Snapshot = &snapshot
	if !recent {
		p.Deep = true
		a.State = "running"
		a.LastAttemptAt = time.Now().UTC().Format(time.RFC3339Nano)
		if err := s.Host.Commit(ctx, a, p, transport.HistoryPage{}, true); err != nil {
			return err
		}
	}
	page, err := s.Host.Fetch(call, a.Conversation, cursor)
	if err != nil {
		if recent {
			a.RecentError = "history_fetch_failed"
			a.RecentFailures++
			a.RecentRetryAt = retryAfter(a.RecentFailures)
		} else {
			a.Error = "history_fetch_failed"
			a.Failures++
			a.RetryAt = retryAfter(a.Failures)
			a.State = "retrying"
		}
		_ = s.Host.Commit(ctx, a, p, transport.HistoryPage{}, true)
		return errors.New("history_fetch_failed")
	}
	if recent {
		if a.RecentStarted == "" {
			a.RecentStarted = time.Now().UTC().Format(time.RFC3339Nano)
			since, err := time.Parse(time.RFC3339Nano, a.LastSync)
			if err != nil {
				since = time.Now()
			}
			a.RecentSince = since.Add(-30 * time.Minute).UTC().Format(time.RFC3339Nano)
		}
		a.RecentCursor = page.NextCursor
		since, _ := time.Parse(time.RFC3339Nano, a.RecentSince)
		done := page.NextCursor == "" || page.NextCursor == cursor
		for _, m := range page.Messages {
			if m.At.Before(since) {
				done = true
			}
		}
		if done {
			a.LastSync = a.RecentStarted
			a.RecentStarted = ""
			a.RecentCursor = ""
		}
	}
	if !recent {
		a.Pages++
		a.LastProgressAt = time.Now().UTC().Format(time.RFC3339Nano)
		for _, m := range page.Messages {
			if m.At.IsZero() {
				continue
			}
			previous, _ := time.Parse(time.RFC3339Nano, a.ScannedThrough)
			if previous.IsZero() || m.At.Before(previous) {
				a.ScannedThrough = m.At.UTC().Format(time.RFC3339Nano)
			}
		}
		a.State = "running"
		a.Cursor = page.NextCursor
		if a.Cursor == "" || a.Cursor == cursor {
			a.State = "available_pages_exhausted"
			a.Cursor = ""
		}
		since, _ := store.ParseArchiveSince(a.Since)
		for _, m := range page.Messages {
			if !since.IsZero() && m.At.Before(since) {
				a.State = "cutoff_reached"
				a.Cursor = ""
			}
		}
	}
	if !recent && (a.State == "available_pages_exhausted" || a.State == "cutoff_reached") {
		a.DeepCheckedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	if recent {
		a.RecentError, a.RecentRetryAt, a.RecentFailures = "", "", 0
	} else {
		a.Error, a.RetryAt, a.Failures = "", "", 0
	}
	persist, stop := context.WithTimeout(ctx, 5*time.Second)
	defer stop()
	return s.Host.Commit(persist, a, p, page, false)
}

func retryReady(value string) bool {
	retry, _ := time.Parse(time.RFC3339Nano, value)
	return !time.Now().Before(retry)
}
func retryAfter(failures int) string {
	delay := time.Duration(1<<min(failures, 6)) * 5 * time.Second
	return time.Now().Add(delay).UTC().Format(time.RFC3339Nano)
}

func (s *Service) refreshStep(ctx context.Context, a store.Archive, p Policy) error {
	call, cancel := context.WithTimeout(ctx, s.fetchTimeout)
	defer cancel()
	snapshot, err := s.Store.ChangeWatermark(a.Conversation.ID)
	if err != nil {
		return err
	}
	p.Snapshot = &snapshot
	page, err := s.Host.Fetch(call, a.Conversation, a.RefreshCursor)
	if err != nil {
		a.RefreshState = "retrying"
		a.RefreshError = "history_fetch_failed"
		a.RefreshFailures++
		a.RefreshRetryAt = retryAfter(a.RefreshFailures)
		_ = s.Host.Commit(ctx, a, p, transport.HistoryPage{}, true)
		return errors.New("history_fetch_failed")
	}
	since, err := store.ParseArchiveSince(a.RefreshSince)
	if err != nil {
		return err
	}
	p.Since = &since
	done := page.NextCursor == "" || page.NextCursor == a.RefreshCursor
	for _, m := range page.Messages {
		if m.At.Before(since) {
			done = true
		}
	}
	a.RefreshCursor = page.NextCursor
	a.RefreshState = "running"
	a.RefreshError = ""
	a.RefreshFailures = 0
	a.RefreshRetryAt = ""
	if done {
		a.RefreshState = "checked_available"
		a.RefreshCursor = ""
		a.RefreshCheckedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	persist, stop := context.WithTimeout(ctx, 5*time.Second)
	defer stop()
	return s.Host.Commit(persist, a, p, page, false)
}
