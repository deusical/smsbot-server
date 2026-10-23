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
	backgroundAfter string
	fallbackAfter   string
	savedSchedule   string
	nextDiscovery   time.Time
	nextFallback    time.Time
	now             func() time.Time
	fetchTimeout    time.Duration
	Store           *store.Store
	Host            Host
	tickMu          sync.Mutex
	wake            chan struct{}
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
	if checker, ok := s.Host.(interface{ NextGapAttempt() (time.Time, bool) }); ok {
		if at, pending := checker.NextGapAttempt(); pending {
			consider(at)
		}
	} else if checker, ok := s.Host.(interface{ HasPendingGap() bool }); ok && checker.HasPendingGap() {
		consider(now)
	}
	archives, err := s.Store.Archives()
	if err != nil {
		return idleInterval
	}
	for _, a := range archives {
		if _, err := s.Host.Policy(a.Conversation); err != nil {
			continue
		}
		if activeState(a.RefreshState) {
			consider(parseTime(a.RefreshRetryAt))
		}
		if activeState(a.State) {
			consider(parseTime(a.RetryAt))
		}
		if a.RecentStarted != "" || a.RecentRetryAt != "" {
			consider(parseTime(a.RecentRetryAt))
		}
	}
	return max(activeInterval, next.Sub(now))
}
func activeState(state string) bool {
	return state == "queued" || state == "running" || state == "retrying"
}
func parseTime(value string) time.Time   { at, _ := time.Parse(time.RFC3339Nano, value); return at }
func (s *Service) due(value string) bool { return !s.now().Before(parseTime(value)) }

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
	var focus, manual *store.Archive
	for i := range archives {
		a := &archives[i]
		if !eligibleArchive(*a) {
			continue
		}
		if _, err := s.Host.Policy(a.Conversation); err != nil {
			continue
		}
		if activeState(a.RefreshState) && s.due(a.RefreshRetryAt) && (focus == nil || a.FocusAt > focus.FocusAt) {
			focus = a
		}
		explicit := isExplicitArchive(*a)
		if explicit && activeState(a.State) && s.due(a.RetryAt) && (manual == nil || a.LastAttemptAt < manual.LastAttemptAt || (a.LastAttemptAt == manual.LastAttemptAt && a.Conversation.ID < manual.Conversation.ID)) {
			manual = a
		}
	}
	if focus != nil {
		if p, err := s.Host.Policy(focus.Conversation); err == nil {
			_ = s.refreshStep(ctx, *focus, p)
		}
	}
	if manual != nil {
		// Reload: a view may have advanced this archive's independent checkpoint.
		if a, err := s.Store.Archive(manual.Conversation.ID); err == nil {
			if p, err := s.Host.Policy(a.Conversation); err == nil {
				a.RequestSource = "owner"
				_ = s.step(ctx, a, p, a.Cursor, false)
			}
		}
	}
	if repair, ok := s.Host.(interface{ RepairCaptureGap(context.Context) error }); ok {
		_ = repair.RepairCaptureGap(transport.WithPhonePurpose(ctx, "gap"))
	}
	// Saved jobs must advance even when discovery is unavailable. Discovery has
	// its own cadence; a new page may seed jobs, never reorder the work rotation.
	if !s.now().Before(s.nextDiscovery) {
		s.nextDiscovery = s.now().Add(idleInterval)
		call, cancel := context.WithTimeout(transport.WithPhonePurpose(ctx, "discovery"), 20*time.Second)
		list, err := s.Host.Discover(call)
		cancel()
		if err != nil {
			s.nextDiscovery = s.now().Add(30 * time.Second)
		} else {
			if paged, ok := s.Host.(interface{ DiscoveryPending() bool }); ok && paged.DiscoveryPending() {
				s.nextDiscovery = s.now().Add(activeInterval)
			}
			for _, c := range list {
				a := store.Archive{Conversation: c, State: "queued", Generation: 1}
				if !eligibleArchive(a) {
					continue
				}
				p, err := s.Host.Policy(c)
				if err != nil {
					continue
				}
				if _, err := s.Store.Archive(c.ID); errors.Is(err, sql.ErrNoRows) {
					_ = s.Host.Commit(ctx, a, p, transport.HistoryPage{}, true)
				}
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
	start := sort.Search(len(archives), func(i int) bool { return archives[i].Conversation.ID > s.fallbackAfter })
	for n := 0; n < len(archives); n++ {
		a := archives[(start+n)%len(archives)]
		if !eligibleArchive(a) || !s.due(a.RecentRetryAt) {
			continue
		}
		if a.RecentStarted == "" && a.RecentRetryAt == "" && !fallback {
			continue
		}
		p, err := s.Host.Policy(a.Conversation)
		if err != nil {
			continue
		}
		s.fallbackAfter = a.Conversation.ID
		if fallback {
			s.nextFallback = s.now().Add(idleInterval)
		}
		if err := s.step(ctx, a, p, a.RecentCursor, true); err != nil {
			return
		}
		break
	}
	if fallback {
		s.nextFallback = s.now().Add(idleInterval)
	}
	// Reload after a recent scan so publishing a deep page cannot erase its state.
	archives, err = s.Store.Archives()
	if err != nil {
		return
	}
	sort.Slice(archives, func(i, j int) bool { return archives[i].Conversation.ID < archives[j].Conversation.ID })
	start = sort.Search(len(archives), func(i int) bool { return archives[i].Conversation.ID > s.backgroundAfter })
	for n := 0; n < len(archives); n++ {
		a := archives[(start+n)%len(archives)]
		explicit := isExplicitArchive(a)
		if explicit || !eligibleArchive(a) || !activeState(a.State) || !s.due(a.RetryAt) {
			continue
		}
		p, err := s.Host.Policy(a.Conversation)
		if err != nil {
			continue
		}
		s.backgroundAfter = a.Conversation.ID
		_ = s.step(ctx, a, p, a.Cursor, false)
		break
	}

}
func isExplicitArchive(a store.Archive) bool {
	return a.RequestedAt != "" || a.RequestSource == "owner" || (a.RequestSource == "" && a.Since != "")
}

func eligibleArchive(a store.Archive) bool {
	return !a.Conversation.Group && a.Conversation.Writable && a.Conversation.ParticipantID != ""
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
		if cursor == "" && a.Pages == 0 {
			a.ScanRevision = p.Revision
		}
		p.Deep = true
		a.State = "running"
		a.LastAttemptAt = s.now().UTC().Format(time.RFC3339Nano)
		if err := s.Host.Commit(ctx, a, p, transport.HistoryPage{}, true); err != nil {
			return err
		}
	}
	purpose := "backfill"
	if recent {
		purpose = "recent"
	}
	page, err := s.Host.Fetch(transport.WithPhonePurpose(call, purpose), a.Conversation, cursor)
	if err != nil {
		if recent {
			a.RecentError = "history_fetch_failed"
			a.RecentFailures++
			a.RecentRetryAt = s.retryAfter(a.RecentFailures)
		} else {
			a.Error = "history_fetch_failed"
			a.Failures++
			a.RetryAt = s.retryAfter(a.Failures)
			a.State = "retrying"
		}
		_ = s.Host.Commit(ctx, a, p, transport.HistoryPage{}, true)
		return errors.New("history_fetch_failed")
	}
	if recent {
		if a.RecentStarted == "" {
			a.RecentStarted = s.now().UTC().Format(time.RFC3339Nano)
			since, err := time.Parse(time.RFC3339Nano, a.LastSync)
			if err != nil {
				since = s.now()
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
		a.LastProgressAt = s.now().UTC().Format(time.RFC3339Nano)
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
		a.DeepCheckedAt = s.now().UTC().Format(time.RFC3339Nano)
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

func (s *Service) retryAfter(failures int) string {
	delay := time.Duration(1<<min(failures, 6)) * 5 * time.Second
	return s.now().Add(delay).UTC().Format(time.RFC3339Nano)
}

func (s *Service) refreshStep(ctx context.Context, a store.Archive, p Policy) error {
	call, cancel := context.WithTimeout(ctx, s.fetchTimeout)
	defer cancel()
	snapshot, err := s.Store.ChangeWatermark(a.Conversation.ID)
	if err != nil {
		return err
	}
	p.Snapshot = &snapshot
	page, err := s.Host.Fetch(transport.WithPhonePurpose(call, "view"), a.Conversation, a.RefreshCursor)
	if err != nil {
		a.RefreshState = "retrying"
		a.RefreshError = "history_fetch_failed"
		a.RefreshFailures++
		a.RefreshRetryAt = s.retryAfter(a.RefreshFailures)
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
		a.RefreshCheckedAt = s.now().UTC().Format(time.RFC3339Nano)
	}
	persist, stop := context.WithTimeout(ctx, 5*time.Second)
	defer stop()
	return s.Host.Commit(persist, a, p, page, false)
}
