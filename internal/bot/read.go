package bot

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"time"

	"smsbot-server/internal/transport"
)

// readThrottle remembers when each conversation was last marked read so a
// busy browser cannot turn viewing into repeated phone requests.
type readThrottle struct {
	mu   sync.Mutex
	last map[string]time.Time
}

func (t *readThrottle) allow(conv string, now time.Time) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.last == nil || len(t.last) > 512 {
		t.last = map[string]time.Time{}
	}
	if previous, ok := t.last[conv]; ok && now.Sub(previous) < 3*time.Second {
		return false
	}
	t.last[conv] = now
	return true
}

// ReadReceipts reports whether viewed conversations are marked read on the
// phone. It is on unless the owner turned it off; old settings without the
// field keep the default.
func (s InboxSettings) ReadReceipts() bool { return s.MarkRead == nil || *s.MarkRead }

// AllowsID is the identity-free check used for ephemeral hints such as typing:
// collection must be on and the conversation selected (or all individuals).
func (s InboxSettings) AllowsID(conv string) bool {
	if !s.Enabled {
		return false
	}
	if s.Scope == "all" {
		return true
	}
	for _, b := range s.Conversations {
		if b.Conversation == conv {
			return true
		}
	}
	return false
}

// MarkConversationRead marks an individual conversation read on the phone when
// the owner has looked at it. Like history reads it needs collection access and
// an exact participant match, runs outside the engine lock, and never sends a
// message, changes Live/Pause or runs a command.
func (e *Engine) MarkConversationRead(ctx context.Context, conv string, through time.Time) error {
	ctx = transport.WithPhonePurpose(ctx, "read")
	marker, ok := e.Transport.(transport.ReadMarker)
	if !ok {
		return errors.New("read_unavailable")
	}
	if through.IsZero() || through.After(e.now().Add(5*time.Minute)) {
		return errors.New("invalid_time")
	}
	e.mu.Lock()
	s, err := e.InboxSettings()
	e.mu.Unlock()
	if err != nil {
		return err
	}
	if !s.Enabled || !s.ReadReceipts() {
		return errors.New("read_receipts_disabled")
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	c, err := e.lookupConversation(ctx, conv)
	if err != nil {
		return err
	}
	if !s.Allows(c) {
		return errors.New("inbox_access_disabled")
	}
	if a, archiveErr := e.Store.Archive(conv); archiveErr == nil && a.Conversation.ParticipantID != c.ParticipantID {
		return errors.New("archive_identity_changed")
	}
	e.mu.Lock()
	current, err := e.InboxSettings()
	e.mu.Unlock()
	if err != nil {
		return err
	}
	if current.Revision != s.Revision || !current.Enabled || !current.ReadReceipts() || !current.Allows(c) {
		return errors.New("inbox_access_disabled")
	}
	if !e.reads.allow(conv, e.now()) {
		return nil
	}
	return marker.MarkRead(ctx, conv, c.ParticipantID, through)
}

// TypingConversations lists conversations the other participant is typing in,
// limited to those the owner has opted to see.
func (e *Engine) TypingConversations() []string {
	reporter, ok := e.Transport.(transport.TypingReporter)
	if !ok {
		return []string{}
	}
	e.mu.Lock()
	s, err := e.InboxSettings()
	e.mu.Unlock()
	out := []string{}
	if err != nil || !s.Enabled {
		return out
	}
	for _, id := range reporter.TypingConversations() {
		if s.AllowsID(id) {
			out = append(out, id)
		}
	}
	return out
}

// ConversationRevision changes whenever the phone reports activity or a message
// is saved, letting the dashboard refetch its list only when something changed.
func (e *Engine) ConversationRevision() string {
	var phone uint64
	if reporter, ok := e.Transport.(transport.ActivityReporter); ok {
		phone = reporter.ActivityRevision()
	}
	return formatRevision(phone, e.Store.ChangeSequence())
}

func formatRevision(phone, saved uint64) string {
	return strconv.FormatUint(phone, 10) + "." + strconv.FormatUint(saved, 10)
}
