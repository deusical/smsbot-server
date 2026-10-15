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
	fetchTimeout time.Duration
	Store        *store.Store
	Host         Host
	tickMu       sync.Mutex
	recent       map[string]time.Time
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
	call, cancel := context.WithTimeout(ctx, 20*time.Second)
	list, err := s.Host.Discover(call)
	cancel()
	if err != nil {
		return
	}
	for _, c := range list {
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
				// Do not immediately issue another call to a failing transport.
				continue
			}
		}
		a, err = s.Store.Archive(c.ID)
		if err != nil {
			continue
		}
		if retryReady(a.RetryAt) && (a.State == "queued" || a.State == "running" || a.State == "retrying") {
			_ = s.step(ctx, a, p, a.Cursor, false)
		}
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
