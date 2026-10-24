package transport

// Phone events: the bounded receive queue, overflow recovery and the event loop
// that normalizes upstream updates into Incoming messages and receipts.

import (
	"context"
	"errors"
	"time"

	"go.mau.fi/mautrix-gmessages/pkg/libgm"
	"go.mau.fi/mautrix-gmessages/pkg/libgm/events"
	"go.mau.fi/mautrix-gmessages/pkg/libgm/gmproto"
)

// libgm invokes this synchronously on its receive loop. Never wait for the
// consumer, touch storage, or make a phone request here.
func (g *Google) enqueue(event any) {
	_, reason, hard, _, failed := connectionFailure(event)
	if failed {
		g.noteFailure(reason, hard)
	}
	// Typing and activity hints bypass the bounded queue: they are ephemeral and
	// must never contribute to overflow recovery or pause automation.
	switch hint := event.(type) {
	case *gmproto.TypingData:
		g.noteTyping(hint)
		return
	case *gmproto.Conversation:
		g.noteActivity()
	case *libgm.WrappedMessage:
		if hint != nil && hint.Message != nil {
			g.noteActivity()
		}
	}
	original := event
	switch e := event.(type) {
	case *events.ListenRecovered, *events.PhoneRespondingAgain:
		g.wakeRecovery(true)
		return
	case *events.AccountChange, *events.ListenFatalError, *events.ListenTemporaryError, *events.PingFailed, *events.GaiaLoggedOut, *events.PhoneNotResponding, *events.NoDataReceived, *events.HackySetActiveMayFail:
	case *events.AuthTokenRefreshed, *gmproto.Settings, *gmproto.Conversation:
	case *libgm.WrappedMessage:
		if e == nil || e.Message == nil {
			return
		}

		g.fullMedia.deliver(e.Message)

		// Historical incoming messages cannot run commands or reconcile sends.
		// Retain outgoing status events for transaction reconciliation.
		if e.IsOld {
			switch e.GetMessageStatus().GetStatus() {
			case gmproto.MessageStatusType_OUTGOING_COMPLETE, gmproto.MessageStatusType_OUTGOING_DELIVERED, gmproto.MessageStatusType_OUTGOING_DISPLAYED:
				if e.GetTmpID() == "" {
					return
				}
			default:
				return
			}
		}
	default:
		return
	}
	if failed {
		event = connectionEvent{original}
	}
	g.queueMu.Lock()
	defer g.queueMu.Unlock()
	select {
	case g.updates <- event:
	default:
		g.overflow.Store(true)
		select {
		case g.overflowWake <- struct{}{}:
		default:
		}
	}
}

func (g *Google) beginOverflowRecovery(pause func()) {
	pause()
	// No work from before the gap may be submitted later. Refresh identities
	// through the normal full-detail picker before accepting new commands.
	// Dropped events are an outage for capture too.
	g.outages.begin(time.Now())
	g.invalidateIdentities()
	g.mu.Lock()
	g.settings = nil // A dropped settings event makes the overflow case different from reconnecting.
	g.mu.Unlock()
	g.diagnose("event_queue_overflow", errors.New("unavailable"))
}

func (g *Google) finishOverflowRecovery() bool {
	g.queueMu.Lock()
	defer g.queueMu.Unlock()
	if len(g.updates) != 0 {
		return false
	}
	g.started = time.Now()
	g.overflow.Store(false)
	return true
}

func normalize(w *libgm.WrappedMessage, started time.Time) (Incoming, bool) {
	if w == nil || w.Message == nil || w.IsOld || w.GetMessageID() == "" || len(w.GetMessageInfo()) == 0 {
		return Incoming{}, false
	}
	outgoing := false
	status := ""
	switch w.GetMessageStatus().GetStatus() {
	case gmproto.MessageStatusType_INCOMING_COMPLETE, gmproto.MessageStatusType_INCOMING_DELIVERED, gmproto.MessageStatusType_INCOMING_DISPLAYED:
	case gmproto.MessageStatusType_OUTGOING_COMPLETE:
		outgoing = true
		status = "submitted"
	case gmproto.MessageStatusType_OUTGOING_DELIVERED:
		outgoing = true
		status = "delivered"
	case gmproto.MessageStatusType_OUTGOING_DISPLAYED:
		outgoing = true
		status = "read"
	default:
		return Incoming{}, false
	}
	at := time.UnixMicro(w.GetTimestamp())
	if at.Before(started) {
		return Incoming{}, false
	}
	text, files, ok := messageParts(w.Message)
	if !ok {
		return Incoming{}, false
	}
	return Incoming{ID: w.GetMessageID(), ConversationID: w.GetConversationID(), ParticipantID: w.GetParticipantID(), Text: text, Attachments: files, At: at, Outgoing: outgoing, TransactionID: w.GetTmpID(), Status: status}, true
}

func (g *Google) Run(ctx context.Context, incoming func(Incoming), pause func(), receipt func(string, string, string, string), interrupted ...func() error) {
	timer := time.NewTicker(30 * time.Second)
	defer timer.Stop()
	recoveryTimer := time.NewTicker(time.Second)
	defer recoveryTimer.Stop()
	results := make(chan recoveryResult, 1)
	interruptionHandled := false
	var pausedEpoch uint64
	handleFailure := func() {
		failed, hard, epoch := g.failureSnapshot()
		if !failed {
			interruptionHandled = false
			return
		}
		if !interruptionHandled {
			interruptionHandled = true
			if len(interrupted) > 0 {
				if interrupted[0]() != nil {
					g.noteFailure("storage_error", true)
					pause()
				}
			} else {
				pause()
			}
		}
		if hard && pausedEpoch != epoch {
			pausedEpoch = epoch
			pause()
		}
	}
	recoveringOverflow := false
	for {
		handleFailure()
		g.startRecovery(ctx, time.Now(), results)
		if g.overflow.Load() {
			if !recoveringOverflow {
				g.beginOverflowRecovery(pause)
				recoveringOverflow = true
			}
			if g.finishOverflowRecovery() {
				recoveringOverflow = false
				g.diagnose("event_queue_drained", nil)
				if !g.healthFailure.Load() {
					g.outageEnded()
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-recoveryTimer.C:
			if g.client != nil && !g.client.IsConnected() && !g.healthFailure.Load() {
				g.noteFailure("reconnecting", false)
			}
		case <-g.recoveryWake:
		case result := <-results:
			recovered, hard := g.finishRecovery(result, time.Now())
			if recovered {
				interruptionHandled = false
				g.diagnose("connection_recovered", nil)
				if !g.overflow.Load() {
					g.outageEnded()
				}
			} else if hard {
				pause()
				g.diagnose("recovery_blocked", result.err)
			} else if result.err != nil {
				g.diagnose("recovery_retry", result.err)
			}
		case <-g.overflowWake:
			// The next iteration pauses and drains; the callback stays nonblocking.
		case <-timer.C:

			if g.Save() != nil {
				g.diagnose("periodic_save_failed", errors.New("save_failed"))
				g.noteFailure("storage_error", true)
				pause()
			}
			// Hints are only a cache: a failed save costs lookups after a restart.
			if g.saveHints() != nil {
				g.diagnose("discovery_hints_save_failed", errors.New("save_failed"))
			}
		case event := <-g.updates:
			if wrapped, ok := event.(connectionEvent); ok {
				event = wrapped.event
			} else {
				if _, reason, hard, _, failed := connectionFailure(event); failed {
					g.noteFailure(reason, hard)
				}
			}
			if name, _, _, err, failed := connectionFailure(event); failed {
				handleFailure()
				g.diagnose(name, err)
				continue
			}
			switch e := event.(type) {
			case *events.AuthTokenRefreshed:
				if g.persistRefresh() != nil {
					g.noteFailure("storage_error", true)
					pause()
				}
			case *gmproto.Settings:
				g.mu.Lock()
				g.settings = e
				g.mu.Unlock()
			case *gmproto.Conversation:
				if g.rememberConversationEvent(e) {
					pause()
				}
			case *libgm.WrappedMessage:
				status := e.GetMessageStatus().GetStatus()
				state := ""
				switch status {
				case gmproto.MessageStatusType_OUTGOING_COMPLETE:
					state = "submitted"
				case gmproto.MessageStatusType_OUTGOING_DELIVERED:
					state = "delivered"
				case gmproto.MessageStatusType_OUTGOING_DISPLAYED:
					state = "read"
				}
				if state != "" && e.GetTmpID() != "" {
					receipt(e.GetConversationID(), e.GetTmpID(), state, status.String())
				}
				if m, ok := normalize(e, g.started); ok && !g.overflow.Load() && !g.healthFailure.Load() {
					g.mu.Lock()
					c := g.conversations[m.ConversationID]
					g.mu.Unlock()
					m.Interaction = interactionSnapshot(e.Message, c.GetDefaultOutgoingID(), time.Now().UTC())
					incoming(m)
				} else if !g.overflow.Load() {
					if item, ok := historicalText(e.Message, e.GetConversationID()); ok {
						g.mu.Lock()
						c := g.conversations[e.GetConversationID()]
						g.mu.Unlock()
						item.Interaction = interactionSnapshot(e.Message, c.GetDefaultOutgoingID(), time.Now().UTC())
						incoming(Incoming{ID: item.ID, ConversationID: e.GetConversationID(), ParticipantID: e.GetParticipantID(), Text: item.Text, Attachments: item.Attachments, At: item.At, Historical: true, Outgoing: item.Direction == "outgoing", TransactionID: item.TransactionID, Status: item.Status, Interaction: item.Interaction})
					}
				}
			case *events.AccountChange:
				// libgm synthesizes an account snapshot during startup. It is not
				// an account switch; actual changes still require owner recovery.
				if e.IsFake {
					g.diagnose("account_snapshot", nil)
					continue
				}

			}
		}
	}
}
