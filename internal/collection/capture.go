package collection

import (
	"context"
	"encoding/json"
	"github.com/deusical/smsbot-server/internal/store"
	"github.com/deusical/smsbot-server/internal/transport"
	"sync"
	"time"
)

type captureConsent struct {
	settings Settings
	epoch    uint64
}
type captureItem struct {
	message transport.Incoming
	epoch   uint64
}

func sameCapturePolicy(a, b Settings) bool {
	if a.Enabled != b.Enabled || a.Scope != b.Scope {
		return false
	}
	x, _ := json.Marshal(a.Conversations)
	y, _ := json.Marshal(b.Conversations)
	return string(x) == string(y)
}

// Called under the control lock when settings change; readers never take that
// lock on the transport event goroutine. Consent snapshots are immutable.
func (svc *Service) publishConsent(s Settings) {
	s.Conversations = append([]Binding{}, s.Conversations...)
	epoch := uint64(1)
	if previous := svc.consent.Load(); previous != nil {
		epoch = previous.epoch
		if !sameCapturePolicy(previous.settings, s) {
			epoch++
		}
	}
	svc.consent.Store(&captureConsent{s, epoch})
}
func (svc *Service) InboxCaptureGap() bool {
	gaps, unknown := svc.CaptureGapStatus()
	if unknown {
		return true
	}
	for _, g := range gaps {
		if !store.GapSettled(g) {
			return true
		}
	}
	return false
}
func (svc *Service) QueueObservation(m transport.Incoming) {
	// A full outgoing snapshot is observation-only, including reactions/edits on
	// old messages. Command freshness still applies independently in Accept.
	if m.Outgoing && m.Interaction != nil {
		m.Historical = true
	}
	consent := svc.consent.Load()
	if consent == nil || !consent.settings.Enabled || m.ID == "" || m.ConversationID == "" || (m.Text == "" && len(m.Attachments) == 0) {
		return
	}
	if consent.settings.Scope == "selected" {
		found := false
		for _, b := range consent.settings.Conversations {
			if b.Conversation == m.ConversationID && speaksFor(b.Participant, m) {
				found = true
				break
			}
		}
		if !found {
			return
		}
	}
	select {
	case svc.captureQueue <- captureItem{m, consent.epoch}:
	default:
		svc.noteCaptureGap(m, consent.epoch, "capture_queue_full")
	}
}
func (svc *Service) RunCapture(ctx context.Context) {
	var workers sync.WaitGroup
	workers.Add(1)
	go func() {
		defer workers.Done()
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				svc.flushCaptureGaps()
				return
			case <-ticker.C:
				svc.flushCaptureGaps()
			}
		}
	}()
	for i := 0; i < 4; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case item := <-svc.captureQueue:
					if ctx.Err() != nil {
						return
					}
					current := svc.consent.Load()
					if current == nil || current.epoch != item.epoch {
						continue
					}
					if !item.message.Historical && svc.now().Sub(item.message.At) > 2*time.Minute {
						svc.noteCaptureGap(item.message, item.epoch, "capture_delayed")
						continue
					}
					if err := svc.observe(ctx, item.message, item.epoch); err != nil {
						svc.noteCaptureGap(item.message, item.epoch, captureFailureReason(err))
					}
				}
			}
		}()
	}
	workers.Wait()
}

// ObserveNow captures one message synchronously. A failure is recorded as a
// capture gap for later repair; it never reaches the command path.
func (svc *Service) ObserveNow(ctx context.Context, m transport.Incoming) {
	if err := svc.Observe(ctx, m); err != nil {
		if consent := svc.consent.Load(); consent != nil {
			svc.noteCaptureGap(m, consent.epoch, captureFailureReason(err))
		}
	}
}
