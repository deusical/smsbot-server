// Package app assembles the service. It constructs the engine and its
// workers, runs startup work in the order safety requires, routes transport
// events, and owns every goroutine it starts. No other package imports it.
package app

import (
	"context"
	"errors"
	"github.com/deusical/smsbot-server/internal/archive"
	"github.com/deusical/smsbot-server/internal/bot"
	"github.com/deusical/smsbot-server/internal/collection"
	"github.com/deusical/smsbot-server/internal/history"
	"github.com/deusical/smsbot-server/internal/store"
	"github.com/deusical/smsbot-server/internal/transport"
	"sync"
	"time"
)

// Service is the assembled application.
type Service struct {
	Store      *store.Store
	Transport  transport.Transport
	Engine     *bot.Engine
	Collection *collection.Service
	History    *history.Service
	Archive    *archive.Service
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
	// One control lock serializes consent changes, send decisions and
	// protected commits across the collection service and the bot.
	control := new(sync.Mutex)
	c := collection.New(s, t, control)
	e := bot.New(s, t, c, control)
	c.History = history.New(s, c.HistoryHost())
	c.Archive = archive.New(s, archiveRoot, c.FileHost())
	// A finished connection outage wakes history at once to catch up.
	if g, ok := t.(*transport.Google); ok {
		g.Reconnected = c.History.Wake
	}
	// Cross-service reactions are explicit here rather than discovered.
	c.SettingsSaved = e.CollectionSettingsSaved
	c.MessageRetentionDays = func() (int, error) {
		cfg, err := e.Configuration()
		return cfg.Settings.RetentionDays, err
	}
	return &Service{Store: s, Transport: t, Engine: e, Collection: c, History: c.History, Archive: c.Archive}
}

// Init runs one-time startup work in order: collection publishes consent and
// marks opted-in history for preservation before the legacy expiry pass can
// delete it. Any failure stops startup.
func (a *Service) Init() error {
	if err := a.Collection.Init(); err != nil {
		return err
	}
	if err := a.Engine.Init(); err != nil {
		return err
	}
	cfg, err := a.Engine.Configuration()
	if err != nil {
		return err
	}
	_, err = a.Store.Retention(cfg.Settings.RetentionDays)
	if err == nil {
		err = a.Store.PruneAIThreads(time.Now(), cfg.Settings.MemoryHours, cfg.Settings.RetentionDays)
	}
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
	w.start(func() { a.Collection.RunCapture(ctx) })
	w.start(func() { a.Archive.Run(ctx) })
	w.start(func() { a.History.Run(ctx) })
	if events != nil {
		w.start(func() {
			events.Run(ctx, a.ingest(ctx), func() { _ = e.Pause(true) }, a.receipt, e.ConnectionInterrupted)
		})
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
