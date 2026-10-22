package transport

import (
	"context"
	"errors"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"go.mau.fi/mautrix-gmessages/pkg/libgm/gmproto"
)

// ActivityReporter exposes a counter that changes whenever the phone reports
// conversation or message activity, so the dashboard can refresh its list
// promptly without polling the phone.
type ActivityReporter interface{ ActivityRevision() uint64 }

// TypingReporter lists individual conversations whose other participant is
// currently typing. State is in memory only and expires on its own.
type TypingReporter interface{ TypingConversations() []string }

// ReadMarker marks a conversation read on the phone through its newest message.
// It verifies the exact participant first and refuses when a newer message than
// the one the owner saw has arrived.
type ReadMarker interface {
	MarkRead(ctx context.Context, conv, expectedPeer string, through time.Time) error
}

var ErrNewerMessage = errors.New("newer_message")

const (
	typingTTL              = 8 * time.Second
	listInvalidateGap      = 10 * time.Second
	partialRetryGap        = 30 * time.Second
	conversationRefreshGap = time.Minute
)

type activityState struct {
	revision       atomic.Uint64
	listStale      atomic.Bool
	lastInvalidate atomic.Int64
	lastRefresh    atomic.Int64
	typingMu       sync.Mutex
	typing         map[string]time.Time
}

// noteActivity runs on libgm's receive loop: it must stay cheap and never touch
// storage or the phone. Every report marks discovery pages out of date; the
// cache spaces the resulting phone reads so a chatty period cannot cause a burst.
func (g *Google) noteActivity() {
	g.activity.revision.Add(1)
	g.discovery.markChanged(time.Now())
	now := time.Now().UnixNano()
	last := g.activity.lastInvalidate.Load()
	if elapsed := now - last; elapsed >= 0 && elapsed < int64(listInvalidateGap) || !g.activity.lastInvalidate.CompareAndSwap(last, now) {
		return
	}
	g.activity.listStale.Store(true)
}

// ConversationRefresher lets the owner's dashboard ask for one fresh phone read
// of the conversation list, for changes the phone may not push. Requests are
// honoured at most once per conversationRefreshGap across all clients.
type ConversationRefresher interface{ RequestConversationRefresh() bool }

func (g *Google) RequestConversationRefresh() bool {
	return g.Ready() && g.forceDiscoveryRefresh(time.Now())
}

func (g *Google) forceDiscoveryRefresh(now time.Time) bool {
	// A clock stepped backwards counts as elapsed rather than locking refreshes out.
	last := g.activity.lastRefresh.Load()
	if elapsed := now.UnixNano() - last; last != 0 && elapsed >= 0 && elapsed < int64(conversationRefreshGap) || !g.activity.lastRefresh.CompareAndSwap(last, now.UnixNano()) {
		return false
	}
	g.discovery.markChanged(now)
	return true
}

func (g *Google) ActivityRevision() uint64 { return g.activity.revision.Load() }

func (g *Google) noteTyping(e *gmproto.TypingData) {
	if e == nil || e.GetConversationID() == "" {
		return
	}
	g.activity.typingMu.Lock()
	defer g.activity.typingMu.Unlock()
	if e.GetType() != gmproto.TypingTypes_STARTED_TYPING {
		delete(g.activity.typing, e.GetConversationID())
		return
	}
	if g.activity.typing == nil {
		g.activity.typing = map[string]time.Time{}
	}
	if len(g.activity.typing) >= 256 {
		return
	}
	g.activity.typing[e.GetConversationID()] = time.Now().Add(typingTTL)
}

// TypingConversations returns only conversations already identified as
// individual chats; unknown or group conversations are never reported.
func (g *Google) TypingConversations() []string {
	now := time.Now()
	g.activity.typingMu.Lock()
	ids := make([]string, 0, len(g.activity.typing))
	for id, until := range g.activity.typing {
		if now.After(until) {
			delete(g.activity.typing, id)
			continue
		}
		ids = append(ids, id)
	}
	g.activity.typingMu.Unlock()
	out := ids[:0]
	g.mu.Lock()
	for _, id := range ids {
		if c := g.conversations[id]; c != nil && !c.GetIsGroupChat() {
			out = append(out, id)
		}
	}
	g.mu.Unlock()
	sort.Strings(out)
	return out
}

func (g *Google) MarkRead(ctx context.Context, conv, expectedPeer string, through time.Time) error {
	ctx = WithPhonePurpose(ctx, "read")
	if !g.Ready() {
		return ErrUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	g.countPhone(ctx, phoneLookup)
	c, err := g.client.GetConversation(ctx, conv)
	if err != nil || !eligible(c) || peer(c) != expectedPeer || c.GetIsGroupChat() {
		return errors.New("ineligible_conversation")
	}
	if !c.GetUnread() {
		return nil
	}
	g.countPhone(ctx, phoneMessages)
	res, err := g.client.FetchMessages(ctx, conv, 1, nil)
	if err != nil {
		return errors.New("read_failed")
	}
	var latest *gmproto.Message
	for _, m := range res.GetMessages() {
		if m.GetConversationID() == conv && m.GetMessageID() != "" && (latest == nil || m.GetTimestamp() > latest.GetTimestamp()) {
			latest = m
		}
	}
	if latest == nil {
		return nil
	}
	if time.UnixMicro(latest.GetTimestamp()).After(through.Add(2 * time.Second)) {
		return ErrNewerMessage
	}
	// The conversation may have changed identity while the messages were fetched.
	g.countPhone(ctx, phoneLookup)
	if again, err := g.client.GetConversation(ctx, conv); err != nil || !eligible(again) || peer(again) != expectedPeer {
		return errors.New("conversation_identity_changed")
	}
	g.countPhone(ctx, phoneMarkRead)
	if err := g.client.MarkRead(ctx, conv, latest.GetMessageID()); err != nil {
		return errors.New("read_failed")
	}
	g.noteActivity()
	return nil
}

// Fixture implementations let offline tests exercise the same API paths.
func (f *Fixture) ActivityRevision() uint64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.activity
}

func (f *Fixture) NoteActivity() {
	f.mu.Lock()
	f.activity++
	f.mu.Unlock()
}

func (f *Fixture) SetTyping(conv string, typing bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.typing == nil {
		f.typing = map[string]time.Time{}
	}
	if typing {
		f.typing[conv] = time.Now().Add(typingTTL)
	} else {
		delete(f.typing, conv)
	}
	f.activity++
}

func (f *Fixture) TypingConversations() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []string{}
	for id, until := range f.typing {
		if time.Now().Before(until) {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

func (f *Fixture) MarkRead(ctx context.Context, conv, expectedPeer string, through time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.Online {
		return ErrUnavailable
	}
	if expectedPeer != "demo-peer" && expectedPeer != "demo-sms-peer" {
		return errors.New("ineligible_conversation")
	}
	if f.Read == nil {
		f.Read = map[string]int{}
	}
	f.Read[conv]++
	f.activity++
	return nil
}

// IdentityEpochSource invalidates only optimization hints after identity/connection
// changes. A value never grants capture or sending permission.
type IdentityEpochSource interface{ IdentityEpoch() uint64 }

func (g *Google) IdentityEpoch() uint64 { return g.identityEpoch.Load() }
