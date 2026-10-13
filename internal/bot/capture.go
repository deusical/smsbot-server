package bot

import (
	"context"
	"encoding/json"
	"smsbot-server/internal/store"
	"smsbot-server/internal/transport"
	"sync"
	"time"
	"unicode/utf8"
)

type captureConsent struct {
	settings InboxSettings
	epoch    uint64
}
type captureItem struct {
	message transport.Incoming
	epoch   uint64
}

func sameCapturePolicy(a, b InboxSettings) bool {
	if a.Enabled != b.Enabled || a.Scope != b.Scope {
		return false
	}
	x, _ := json.Marshal(a.Conversations)
	y, _ := json.Marshal(b.Conversations)
	return string(x) == string(y)
}

// Called under the engine lock when settings change; readers never take that
// lock on the transport event goroutine. Consent snapshots are immutable.
func (e *Engine) publishCaptureConsent(s InboxSettings) {
	s.Conversations = append([]InboxBinding{}, s.Conversations...)
	epoch := uint64(1)
	if previous := e.captureConsent.Load(); previous != nil {
		epoch = previous.epoch
		if !sameCapturePolicy(previous.settings, s) {
			epoch++
		}
	}
	e.captureConsent.Store(&captureConsent{s, epoch})
}
func (e *Engine) InboxCaptureGap() bool { return e.captureGap.Load() }
func (e *Engine) QueueObservation(m transport.Incoming) {
	consent := e.captureConsent.Load()
	if consent == nil || !consent.settings.Enabled || m.Historical || m.ID == "" || m.ConversationID == "" || m.Text == "" || utf8.RuneCountInString(m.Text) > 12000 {
		return
	}
	if consent.settings.Scope == "selected" {
		found := false
		for _, b := range consent.settings.Conversations {
			if b.Conversation == m.ConversationID && (m.Outgoing || b.Participant == m.ParticipantID) {
				found = true
				break
			}
		}
		if !found {
			return
		}
	}
	select {
	case e.captureQueue <- captureItem{m, consent.epoch}:
	default:
		e.captureGap.Store(true)
	}
}
func (e *Engine) RunCapture(ctx context.Context) {
	var workers sync.WaitGroup
	for i := 0; i < 4; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case item := <-e.captureQueue:
					if ctx.Err() != nil {
						return
					}
					current := e.captureConsent.Load()
					if current == nil || current.epoch != item.epoch {
						continue
					}
					if e.now().Sub(item.message.At) > 2*time.Minute {
						e.captureGap.Store(true)
						continue
					}
					if err := e.observe(ctx, item.message, item.epoch); err != nil {
						e.captureGap.Store(true)
					}
				}
			}
		}()
	}
	workers.Wait()
}

// Ingest keeps optional collection off the transport control-event consumer.
// Only failures in the existing command path propagate to its pause handler.
func (e *Engine) Ingest(ctx context.Context, m transport.Incoming) (bool, error) {
	e.QueueObservation(m)
	accepted, err := e.Accept(ctx, m)
	if accepted && err == nil {
		err = e.Store.PublishCommandMessage(store.Hash(m.ConversationID + "\x00" + m.ID))
	}
	return accepted, err
}
