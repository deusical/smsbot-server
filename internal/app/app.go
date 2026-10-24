// Package app assembles the service. It constructs the engine and its
// workers, runs startup work in the order safety requires, routes transport
// events, and owns every goroutine it starts. No other package imports it.
package app

import (
	"context"
	"errors"
	"github.com/deusical/smsbot-server/internal/archive"
	"github.com/deusical/smsbot-server/internal/bot"
	"github.com/deusical/smsbot-server/internal/history"
	"github.com/deusical/smsbot-server/internal/store"
	"github.com/deusical/smsbot-server/internal/transport"
	"sync"
)

// Service is the assembled application.
type Service struct {
	Store     *store.Store
	Transport transport.Transport
	Engine    *bot.Engine
	History   *history.Service
	Archive   *archive.Service
}

// PrepareStore recovers sends interrupted by the last shutdown and pauses
// automation. It runs before any transport connects, so no phone event can
// reach the dispatcher before every start is paused.
func PrepareStore(s *store.Store) error {
	if err := s.Recover(); err != nil {
		return err
	}
	return s.Pause(true)
}

// New wires the service without touching storage or starting work.
// archiveRoot holds saved originals.
func New(s *store.Store, t transport.Transport, archiveRoot string) *Service {
	e := bot.New(s, t)
	e.History = history.New(s, e.HistoryHost())
	e.Archive = archive.New(s, archiveRoot, e.FileHost())
	return &Service{Store: s, Transport: t, Engine: e, History: e.History, Archive: e.Archive}
}

// Init runs one-time startup work in order: the engine publishes consent and
// marks opted-in history for preservation before the legacy expiry pass can
// delete it. Any failure stops startup.
func (a *Service) Init() error {
	if err := a.Engine.Init(); err != nil {
		return err
	}
	cfg, err := a.Engine.Configuration()
	if err != nil {
		return err
	}
	_, err = a.Store.Retention(cfg.Settings.RetentionDays)
	return err
}

// EventSource delivers phone events. *transport.Google implements it.
type EventSource interface {
	Run(ctx context.Context, incoming func(transport.Incoming), pause func(), receipt func(conv, txn, state, reported string), interrupted ...func() error)
}

// Workers are the running background loops. Stop cancels them and waits
// until every one has returned.
type Workers struct {
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

func (w *Workers) Stop() {
	w.cancel()
	w.wg.Wait()
}

func (w *Workers) start(run func()) {
	w.wg.Add(1)
	go func() { defer w.wg.Done(); run() }()
}

// Start launches capture, original-file, history and dispatch workers and,
// when events is non-nil, the transport event loop. dispatchFailed is told
// when a dispatch failure paused automation. The workers stop when ctx ends
// or Stop is called.
func (a *Service) Start(ctx context.Context, events EventSource, dispatchFailed func()) *Workers {
	ctx, cancel := context.WithCancel(ctx)
	w := &Workers{cancel: cancel}
	e := a.Engine
	w.start(func() { e.RunCapture(ctx) })
	w.start(func() { a.Archive.Run(ctx) })
	w.start(func() { a.History.Run(ctx) })
	if events != nil {
		w.start(func() { events.Run(ctx, a.ingest(ctx), func() { _ = e.Pause(true) }, a.receipt, e.ConnectionInterrupted) })
	}
	w.start(func() {
		e.Run(ctx, func(err error) {
			// A lost connection cancels unsent work without changing Live/Pause;
			// any other dispatch failure pauses automation for the owner.
			if errors.Is(err, transport.ErrUnavailable) && e.ConnectionInterrupted() == nil {
				return
			}
			_ = e.Pause(true)
			if dispatchFailed != nil {
				dispatchFailed()
			}
		})
	})
	return w
}

// ingest routes one incoming event. Only failures in the command path pause:
// collection failures are recorded as capture gaps inside the engine.
func (a *Service) ingest(ctx context.Context) func(transport.Incoming) {
	e := a.Engine
	return func(m transport.Incoming) {
		if _, err := e.Ingest(ctx, m); err != nil {
			if !errors.Is(err, transport.ErrUnavailable) || e.ConnectionInterrupted() != nil {
				_ = e.Pause(true)
			}
		}
	}
}

// receipt reconciles a send status; a failure to record it pauses automation.
func (a *Service) receipt(conv, txn, state, reported string) {
	if a.Engine.Reconcile(conv, txn, state, reported) != nil {
		_ = a.Engine.Pause(true)
	}
}
