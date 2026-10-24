// Package collection owns the owner's message collection: its settings and
// consent, observation capture, capture-gap repair, archive sync control, and
// the consent-checked hosts the history and original-file workers read
// through. It never routes commands or submits messages; the bot consumes the
// narrow operations it needs (settings, consent epoch, observation).
package collection

import (
	"context"
	"encoding/json"
	"github.com/deusical/smsbot-server/internal/archive"
	"github.com/deusical/smsbot-server/internal/history"
	"github.com/deusical/smsbot-server/internal/media"
	"github.com/deusical/smsbot-server/internal/store"
	"github.com/deusical/smsbot-server/internal/transport"
	"sync"
	"sync/atomic"
	"time"
)

// Service is the collection service.
type Service struct {
	Store     *store.Store
	Transport transport.Transport
	// History and Archive are the workers this service hosts; it wakes them
	// when consent or requests change. Attached by internal/app.
	History *history.Service
	Archive *archive.Service

	// Hooks wired by internal/app; both may be nil.
	//
	// SettingsSaved runs under the control lock after new settings are saved,
	// so the bot can cancel work they no longer permit.
	SettingsSaved func(Settings)
	// MessageRetentionDays reports the bot's message retention, which still
	// bounds collected messages read through the legacy inbox routes.
	MessageRetentionDays func() (int, error)

	// mu is the control lock shared with the bot engine: settings and consent
	// changes serialize with send decisions and protected commits.
	mu           *sync.Mutex
	now          func() time.Time
	reads        readThrottle
	captureReads captureReads
	captureQueue chan captureItem
	consent      atomic.Pointer[captureConsent]
	gapMu        sync.Mutex
	gapFlushMu   sync.Mutex
	flushingGaps map[string]pendingCaptureGap
	pendingGaps  map[string]pendingCaptureGap
}

// New wires the service without touching storage. control is the lock shared
// with the bot engine. Run Init before use.
func New(s *store.Store, t transport.Transport, control *sync.Mutex) *Service {
	return &Service{Store: s, Transport: t, mu: control, now: time.Now, pendingGaps: map[string]pendingCaptureGap{}, captureQueue: make(chan captureItem, 512)}
}

// SetClock replaces the service clock (tests only).
func (svc *Service) SetClock(now func() time.Time) { svc.now = now }

// HistoryHost is the consent-checked adapter the history worker reads through,
// including its optional capture-gap and paged-discovery abilities.
func (svc *Service) HistoryHost() HistoryHost { return HistoryHost{svc} }

// FileHost is the consent-checked adapter the original-file worker uses.
func (svc *Service) FileHost() FileHost { return FileHost{svc} }

// Init publishes the consent snapshot capture reads and marks existing
// opted-in history for preservation. It must run before the legacy expiry pass
// and before any observation; the markers grant no viewing or collection
// rights before verification.
func (svc *Service) Init() error {
	settings, err := svc.Settings()
	if err != nil {
		return err
	}
	svc.publishConsent(settings)
	if !settings.Enabled {
		return nil
	}
	selected := make([]string, 0, len(settings.Conversations))
	for _, b := range settings.Conversations {
		selected = append(selected, b.Conversation)
	}
	return svc.Store.PreserveHistory(settings.Scope == "all", selected)
}

// ConsentEpoch identifies the current collection consent; it changes whenever
// enablement, scope or selected conversations change. Zero means none yet.
func (svc *Service) ConsentEpoch() uint64 {
	if c := svc.consent.Load(); c != nil {
		return c.epoch
	}
	return 0
}

func (svc *Service) lookup(ctx context.Context, id string) (transport.Conversation, error) {
	return transport.Lookup(ctx, svc.Transport, id)
}

// Binding selects one individual conversation by its exact participant.
type Binding struct {
	Conversation string `json:"conversation"`
	Participant  string `json:"participant"`
}

// Settings are the owner's collection preferences, stored encrypted.
type Settings struct {
	ArchiveRetentionDays int           `json:"archive_retention_days"`
	AttachmentLimits     *media.Limits `json:"attachment_limits,omitempty"`
	Enabled              bool          `json:"enabled"`
	Scope                string        `json:"scope"`
	Conversations        []Binding     `json:"conversations"`
	LoadOlder            bool          `json:"load_older"`
	AutomaticReplies     bool          `json:"automatic_replies"`
	// MarkRead is a pointer so settings saved before this option existed keep the
	// default (on) instead of decoding to false.
	MarkRead *bool `json:"mark_read_when_viewed,omitempty"`
	Revision int64 `json:"revision"`
}

func (s Settings) FileLimits() media.Limits {
	if s.AttachmentLimits == nil {
		return media.DefaultLimits()
	}
	return *s.AttachmentLimits
}

// Settings reads the saved preferences. Callers that combine the result with
// a commit hold the control lock.
func (svc *Service) Settings() (Settings, error) {
	defaults := media.DefaultLimits()
	s := Settings{AttachmentLimits: &defaults, Scope: "all", AutomaticReplies: true, Conversations: []Binding{}}
	b, err := svc.Store.GetSecretSetting("inbox_settings", "inbox-settings")
	if err != nil {
		return s, err
	}
	if len(b) > 0 {
		err = json.Unmarshal(b, &s)
	}
	return s, err
}

// Allows is the exact-identity collection check: enabled, an individual
// writable conversation, and selected (or all) by its exact participant.
func (s Settings) Allows(c transport.Conversation) bool {
	if !s.Enabled || c.Group || !c.Writable || c.ParticipantID == "" {
		return false
	}
	if s.Scope == "all" {
		return true
	}
	for _, b := range s.Conversations {
		if b.Conversation == c.ID && b.Participant == c.ParticipantID {
			return true
		}
	}
	return false
}
