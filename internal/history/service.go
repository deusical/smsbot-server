// Package history owns durable archive read jobs. It has no send or command API.
package history

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/deusical/smsbot-server/internal/store"
	"github.com/deusical/smsbot-server/internal/transport"
	"sort"
	"sync"
	"time"
)

type Policy struct {
	Since     *time.Time
	Snapshot  *int64
	Revision  int64
	Retention int
}

// Work names the workflow a history step belongs to. Each workflow owns one
// archive checkpoint and publishes only that checkpoint when it commits.
type Work int

const (
	// SeedWork records a newly discovered conversation; it never overwrites one.
	SeedWork Work = iota
	// ImportWork pages toward older history (ImportCheckpoint, versioned by Generation).
	ImportWork
	// RecentWork reconciles the newest messages (RecentCheckpoint).
	RecentWork
	// ViewWork rechecks the span the owner is viewing (ViewCheckpoint, versioned by ViewGeneration).
	ViewWork
	// GapWork repairs a capture gap; its progress lives in the gap, not the archive.
	GapWork
)

// ErrJobChanged rejects a step whose checkpoint was restarted, cancelled or
// re-targeted after its fetch began.
var ErrJobChanged = errors.New("archive_job_changed")

// Commit is one bounded step's result. Page is nil for a state-only update.
// Apply changes only the checkpoint owned by Work.
type Commit struct {
	Work         Work
	Conversation transport.Conversation
	Token        int64 // checkpoint version read before the step started
	Policy       Policy
	Page         *transport.HistoryPage
	Apply        func(*store.Archive)
}

// Check reports whether the step still applies to the current record.
func (c Commit) Check(a store.Archive, exists bool) error {
	if !exists {
		if c.Work == SeedWork || c.Work == GapWork {
			return nil
		}
		return ErrJobChanged
	}
	if a.Conversation.ParticipantID != c.Conversation.ParticipantID {
		return ErrJobChanged
	}
	switch c.Work {
	case ImportWork:
		if a.Generation != c.Token {
			return ErrJobChanged
		}
	case ViewWork:
		if a.ViewGeneration != c.Token {
			return ErrJobChanged
		}
	}
	return nil
}

// ApplyTo publishes the step onto the current record; see Store.UpdateArchive.
func (c Commit) ApplyTo(a *store.Archive, exists bool) error {
	if err := c.Check(*a, exists); err != nil {
		return err
	}
	if !exists {
		*a = store.NewArchive(c.Conversation)
	}
	if c.Apply != nil {
		c.Apply(a)
	}
	return nil
}

type Host interface {
	Prune(context.Context) error
	Discover(context.Context) ([]transport.Conversation, error)
	Policy(transport.Conversation) (Policy, error)
	Fetch(context.Context, transport.Conversation, string) (transport.HistoryPage, error)
	Commit(context.Context, Commit) error
}
type Service struct {
	backgroundAfter string
	fallbackAfter   string
	savedSchedule   string
	nextDiscovery   time.Time
	nextFallback    time.Time
	// Catch-up after missed capture: the earliest time messages may have been
	// missed, cleared once a discovery pass that started after it finishes.
	catchUpToken uint64
	catchUpSince time.Time
	catchUpFresh bool
	now          func() time.Time
	fetchTimeout time.Duration
	Store        *store.Store
	Host         Host
	tickMu       sync.Mutex
	wake         chan struct{}
}

const (
	activeInterval = 3 * time.Second
	idleInterval   = 15 * time.Minute
)

func New(s *store.Store, h Host) *Service { return newService(s, h, time.Now) }
func newService(s *store.Store, h Host, now func() time.Time) *Service {
	worker := &Service{
		Store:        s,
		Host:         h,
		fetchTimeout: 20 * time.Second,
		now:          now,
		wake:         make(chan struct{}, 1),
	}
	var saved struct {
		After       string
		RecentAfter string
		Fallback    time.Time
	}
	if b, err := s.GetSecretSetting("history_schedule", "history-schedule"); err == nil && json.Unmarshal(b, &saved) == nil {
		worker.savedSchedule = string(b)
		worker.backgroundAfter = saved.After
		worker.fallbackAfter = saved.RecentAfter
		if !saved.Fallback.After(now().Add(idleInterval)) {
			worker.nextFallback = saved.Fallback
		}
	}
	return worker
}

func (s *Service) Wake() {
	if s == nil || s.wake == nil {
		return
	}
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// Each lane owns its own deadline. An active import is not permission to poll
// every completed conversation on each fast turn.
func (s *Service) nextInterval() time.Duration {
	now := s.now()
	next := now.Add(idleInterval)
	consider := func(at time.Time) {
		if at.IsZero() || at.Before(now) {
			at = now
		}
		if at.Before(next) {
			next = at
		}
	}
	if !s.nextDiscovery.IsZero() {
		consider(s.nextDiscovery)
	}
	if !s.nextFallback.IsZero() {
		consider(s.nextFallback)
	}
	if gaps, ok := s.Host.(GapSchedule); ok {
		if at, pending := gaps.NextGapAttempt(); pending {
			consider(at)
		}
	}
	archives, err := s.Store.Archives()
	if err != nil {
		return idleInterval
	}
	for _, at := range pendingDeadlines(archives, s.allowed) {
		consider(at)
	}
	return max(activeInterval, next.Sub(now))
}

// allowed reports whether a record is an individual conversation the owner
// currently permits collection for. Policy is a local check, never a phone call.
func (s *Service) allowed(a store.Archive) bool {
	if !eligibleArchive(a) {
		return false
	}
	_, err := s.Host.Policy(a.Conversation)
	return err == nil
}
func activeState(state string) bool {
	return state == "queued" || state == "running" || state == "retrying"
}
func parseTime(value string) time.Time { at, _ := time.Parse(time.RFC3339Nano, value); return at }

func (s *Service) Run(ctx context.Context) {
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.wake:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			s.Tick(ctx)
			timer.Reset(s.nextInterval())
		case <-timer.C:
			s.Tick(ctx)
			timer.Reset(s.nextInterval())
		}
	}
}

// Tick runs at most one step per workflow. Steps publish only their own
// checkpoint, so one snapshot serves every lane; it is reloaded only after
// discovery, which may seed new records.
func (s *Service) Tick(ctx context.Context) {
	s.tickMu.Lock()
	defer s.tickMu.Unlock()
	defer func() {
		b, err := json.Marshal(struct {
			After       string
			RecentAfter string
			Fallback    time.Time
		}{s.backgroundAfter, s.fallbackAfter, s.nextFallback})
		if err == nil && string(b) != s.savedSchedule {
			if s.Store.SetSecretSetting("history_schedule", "history-schedule", b) == nil {
				s.savedSchedule = string(b)
			}
		}
	}()
	if ctx.Err() != nil {
		return
	}
	_ = s.Host.Prune(ctx)
	archives, err := s.Store.Archives()
	if err != nil {
		return
	}
	// Owner views and explicit imports keep independent, bounded opportunities.
	focus, manual := selectPriority(s.now(), archives, s.allowed)
	if focus != nil {
		if p, err := s.Host.Policy(focus.Conversation); err == nil {
			_ = s.viewStep(ctx, *focus, p)
		}
	}
	if manual != nil {
		if p, err := s.Host.Policy(manual.Conversation); err == nil {
			_ = s.importStep(ctx, *manual, p, true)
		}
	}
	if repair, ok := s.Host.(GapRepairer); ok {
		_ = repair.RepairCaptureGap(transport.WithPhonePurpose(ctx, "gap"))
	}
	// Saved jobs must advance even when discovery is unavailable. Discovery has
	// its own cadence; a new page may seed jobs, never reorder the work rotation.
	// Missed capture (an outage, or the service being down) starts a discovery
	// pass now; the pass catches up the conversations active since then.
	if src, ok := s.Host.(CatchUpSource); ok {
		if since, next, missed := src.MissedSince(s.catchUpToken); missed || next != s.catchUpToken {
			s.catchUpToken = next
			if missed {
				if s.catchUpSince.IsZero() || since.Before(s.catchUpSince) {
					s.catchUpSince = since
				}
				s.catchUpFresh = false
				s.nextDiscovery = time.Time{}
			}
		}
	}
	if !s.now().Before(s.nextDiscovery) {
		s.nextDiscovery = s.now().Add(idleInterval)
		paged, isPaged := s.Host.(DiscoveryPager)
		if !isPaged || !paged.DiscoveryPending() {
			s.catchUpFresh = true // this page starts a complete inbox pass
		}
		call, cancel := context.WithTimeout(transport.WithPhonePurpose(ctx, "discovery"), 20*time.Second)
		list, err := s.Host.Discover(call)
		cancel()
		if err != nil {
			s.nextDiscovery = s.now().Add(30 * time.Second)
		} else {
			pending := isPaged && paged.DiscoveryPending()
			if pending {
				s.nextDiscovery = s.now().Add(activeInterval)
			}
			for _, c := range list {
				if !eligibleArchive(store.NewArchive(c)) {
					continue
				}
				p, err := s.Host.Policy(c)
				if err != nil {
					continue
				}
				if _, err := s.Store.Archive(c.ID); errors.Is(err, sql.ErrNoRows) {
					_ = s.Host.Commit(ctx, Commit{Work: SeedWork, Conversation: c, Policy: p})
				} else if err == nil && missedActivity(c, s.catchUpSince) {
					_ = s.Host.Commit(ctx, catchUp(c, p, s.catchUpSince, s.now()))
				}
			}
			if s.catchUpFresh && !pending {
				s.catchUpSince = time.Time{}
			}
		}
	}
	archives, err = s.Store.Archives()
	if err != nil {
		return
	}
	sort.Slice(archives, func(i, j int) bool { return archives[i].Conversation.ID < archives[j].Conversation.ID })
	// Routine reconciliation and deep imports have separate stable rotations:
	// a small unfinished backlog must not trap fallback coverage in those IDs.
	if manual != nil && s.nextFallback.IsZero() {
		s.nextFallback = s.now().Add(idleInterval)
	}
	fallback := !s.now().Before(s.nextFallback)
	if fallback {
		s.nextFallback = s.now().Add(idleInterval)
	}
	if a, ok := rotate(archives, s.fallbackAfter, func(a store.Archive) bool { return wantsRecent(s.now(), a, fallback) && s.allowed(a) }); ok {
		s.fallbackAfter = a.Conversation.ID
		if p, err := s.Host.Policy(a.Conversation); err == nil {
			if err := s.recentStep(ctx, a, p); err != nil {
				// A failing phone gets no further reads this turn.
				return
			}
		}
	}
	if a, ok := rotate(archives, s.backgroundAfter, func(a store.Archive) bool { return wantsBackground(s.now(), a) && s.allowed(a) }); ok {
		s.backgroundAfter = a.Conversation.ID
		if p, err := s.Host.Policy(a.Conversation); err == nil {
			_ = s.importStep(ctx, a, p, false)
		}
	}
}
func isExplicitArchive(a store.Archive) bool {
	return a.RequestedAt != "" || a.RequestSource == "owner" || (a.RequestSource == "" && a.Since != "")
}

func eligibleArchive(a store.Archive) bool {
	return !a.Conversation.Group && a.Conversation.Writable && a.Conversation.ParticipantID != ""
}

// fetch reads one page with a bounded deadline, after recording the change
// watermark that protects newer live observations from this snapshot.
func (s *Service) fetch(ctx context.Context, w Work, a store.Archive, p *Policy, cursor string) (transport.HistoryPage, error) {
	snapshot, err := s.Store.ChangeWatermark(a.Conversation.ID)
	if err != nil {
		return transport.HistoryPage{}, err
	}
	p.Snapshot = &snapshot
	call, cancel := context.WithTimeout(ctx, s.fetchTimeout)
	defer cancel()
	purpose := map[Work]string{ImportWork: "backfill", RecentWork: "recent", ViewWork: "view"}[w]
	return s.Host.Fetch(transport.WithPhonePurpose(call, purpose), a.Conversation, cursor)
}

// publish commits a fetched page with its own persistence deadline: a page
// that arrived at the fetch deadline must not inherit an exhausted budget.
func (s *Service) publish(ctx context.Context, c Commit) error {
	persist, stop := context.WithTimeout(ctx, 5*time.Second)
	defer stop()
	return s.Host.Commit(persist, c)
}

// importStep fetches one deep import page. owner marks an explicit request.
func (s *Service) importStep(ctx context.Context, a store.Archive, p Policy, owner bool) error {
	cp := a.ImportCheckpoint
	commit := func(page *transport.HistoryPage) Commit {
		checkpoint := cp
		return Commit{Work: ImportWork, Conversation: a.Conversation, Token: a.Generation, Policy: p, Page: page,
			Apply: func(r *store.Archive) { r.ImportCheckpoint = checkpoint }}
	}
	if owner {
		cp.RequestSource = "owner"
	}
	cursor := cp.Cursor
	if cursor == "" && cp.Pages == 0 {
		cp.ScanRevision = p.Revision
	}
	cp.State = "running"
	cp.LastAttemptAt = s.now().UTC().Format(time.RFC3339Nano)
	if err := s.Host.Commit(ctx, commit(nil)); err != nil {
		return err
	}
	page, err := s.fetch(ctx, ImportWork, a, &p, cursor)
	if err != nil {
		cp.Error = "history_fetch_failed"
		cp.Failures++
		cp.RetryAt = s.retryAfter(cp.Failures)
		cp.State = "retrying"
		_ = s.Host.Commit(ctx, commit(nil))
		return errors.New("history_fetch_failed")
	}
	cp.Pages++
	cp.LastProgressAt = s.now().UTC().Format(time.RFC3339Nano)
	for _, m := range page.Messages {
		if m.At.IsZero() {
			continue
		}
		previous, _ := time.Parse(time.RFC3339Nano, cp.ScannedThrough)
		if previous.IsZero() || m.At.Before(previous) {
			cp.ScannedThrough = m.At.UTC().Format(time.RFC3339Nano)
		}
	}
	cp.State = "running"
	cp.Cursor = page.NextCursor
	if cp.Cursor == "" || cp.Cursor == cursor {
		cp.State = "available_pages_exhausted"
		cp.Cursor = ""
	}
	since, _ := store.ParseArchiveSince(cp.Since)
	for _, m := range page.Messages {
		if !since.IsZero() && m.At.Before(since) {
			cp.State = "cutoff_reached"
			cp.Cursor = ""
		}
	}
	if cp.State == "available_pages_exhausted" || cp.State == "cutoff_reached" {
		cp.DeepCheckedAt = s.now().UTC().Format(time.RFC3339Nano)
	}
	cp.Error, cp.RetryAt, cp.Failures = "", "", 0
	return s.publish(ctx, commit(&page))
}

// recentStep fetches one page of routine reconciliation.
func (s *Service) recentStep(ctx context.Context, a store.Archive, p Policy) error {
	cp := a.RecentCheckpoint
	commit := func(page *transport.HistoryPage) Commit {
		checkpoint := cp
		return Commit{Work: RecentWork, Conversation: a.Conversation, Policy: p, Page: page,
			Apply: func(r *store.Archive) { r.RecentCheckpoint = checkpoint }}
	}
	cursor := cp.RecentCursor
	page, err := s.fetch(ctx, RecentWork, a, &p, cursor)
	if err != nil {
		cp.RecentError = "history_fetch_failed"
		cp.RecentFailures++
		cp.RecentRetryAt = s.retryAfter(cp.RecentFailures)
		_ = s.Host.Commit(ctx, commit(nil))
		return errors.New("history_fetch_failed")
	}
	if cp.RecentStarted == "" {
		cp.RecentStarted = s.now().UTC().Format(time.RFC3339Nano)
		since, err := time.Parse(time.RFC3339Nano, cp.LastSync)
		if err != nil {
			since = s.now()
		}
		cp.RecentSince = since.Add(-30 * time.Minute).UTC().Format(time.RFC3339Nano)
	}
	cp.RecentCursor = page.NextCursor
	since, _ := time.Parse(time.RFC3339Nano, cp.RecentSince)
	done := page.NextCursor == "" || page.NextCursor == cursor
	for _, m := range page.Messages {
		if m.At.Before(since) {
			done = true
		}
	}
	if done {
		cp.LastSync = cp.RecentStarted
		cp.RecentStarted = ""
		cp.RecentCursor = ""
	}
	cp.RecentError, cp.RecentRetryAt, cp.RecentFailures = "", "", 0
	return s.publish(ctx, commit(&page))
}

func (s *Service) retryAfter(failures int) string {
	delay := time.Duration(1<<min(failures, 6)) * 5 * time.Second
	return s.now().Add(delay).UTC().Format(time.RFC3339Nano)
}

// viewStep fetches one page of the owner's viewed span.
func (s *Service) viewStep(ctx context.Context, a store.Archive, p Policy) error {
	cp := a.ViewCheckpoint
	commit := func(page *transport.HistoryPage) Commit {
		checkpoint := cp
		return Commit{Work: ViewWork, Conversation: a.Conversation, Token: cp.ViewGeneration, Policy: p, Page: page,
			Apply: func(r *store.Archive) { r.ViewCheckpoint = checkpoint }}
	}
	page, err := s.fetch(ctx, ViewWork, a, &p, cp.RefreshCursor)
	if err != nil {
		cp.RefreshState = "retrying"
		cp.RefreshError = "history_fetch_failed"
		cp.RefreshFailures++
		cp.RefreshRetryAt = s.retryAfter(cp.RefreshFailures)
		_ = s.Host.Commit(ctx, commit(nil))
		return errors.New("history_fetch_failed")
	}
	since, err := store.ParseArchiveSince(cp.RefreshSince)
	if err != nil {
		return err
	}
	p.Since = &since
	done := page.NextCursor == "" || page.NextCursor == cp.RefreshCursor
	for _, m := range page.Messages {
		if m.At.Before(since) {
			done = true
		}
	}
	cp.RefreshCursor = page.NextCursor
	cp.RefreshState = "running"
	cp.RefreshError = ""
	cp.RefreshFailures = 0
	cp.RefreshRetryAt = ""
	if done {
		cp.RefreshState = "checked_available"
		cp.RefreshCursor = ""
		cp.RefreshCheckedAt = s.now().UTC().Format(time.RFC3339Nano)
	}
	return s.publish(ctx, commit(&page))
}
