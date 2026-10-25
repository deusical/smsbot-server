package bot

import (
	"errors"
	"github.com/deusical/smsbot-server/internal/store"
	"github.com/deusical/smsbot-server/internal/transport"
	"strings"
)

// planAIThread snapshots only the selected ancestor path. Discovery and history
// collection never supply implicit context for a new ask.
func (e *Engine) planAIThread(m transport.Incoming, p *store.Plan, parent *store.AIParent, model Model, g Generation, settings Settings) error {
	budget := 8192
	if model.ContextLength > 0 {
		available := model.ContextLength - len(p.Input) - len(p.Instructions) - g.Tokens - 1024
		if available < 0 {
			p.Model = ""
			p.Memory = false
			p.Reply = "This question is too large for the selected model's context. Shorten it or choose a larger-context model."
			return nil
		}
		budget = min(budget, available)
	}
	p.Thread = &store.AIThreadPlan{ID: store.ID(), RetentionDays: settings.RetentionDays}
	if parent != nil {
		history, omitted, err := e.Store.AIHistory(*parent, m.ConversationID, p.Participant, "!ask", p.MemoryEpoch, e.now(), settings.MemoryHours, settings.RetentionDays, budget)
		if errors.Is(err, store.ErrAIThreadUnavailable) {
			p.Thread = nil
			p.Memory = false
			p.Model = ""
			p.Reply = "That AI conversation has expired or was cleared. Send !ask to start a new one."
			return nil
		}
		if err != nil {
			return err
		}
		p.Thread.ID = parent.Thread
		p.Thread.Parent = parent.Exchange
		p.Context = history
		if omitted {
			p.Instructions += "\nEarlier exchanges were omitted to fit the context budget. Do not claim to remember them."
		}
	}
	if m.Interaction != nil && m.Interaction.UpstreamID == m.ID && strings.Contains(strings.ToUpper(m.Interaction.Protocol), "RCS") {
		if _, ok := e.Transport.(transport.ReplySender); ok {
			p.Thread.ReplyWire = m.ID
		}
	}
	return nil
}

// recordAIAnswer runs on the serialized event consumer, not libgm's callback.
// Exact transaction evidence can resolve an uncertain send, without resending it.
func (e *Engine) recordAIAnswer(m transport.Incoming) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	cfg, err := e.Configuration()
	if err != nil {
		return err
	}
	return e.Store.RecordAIAnswer(m.ConversationID, m.TransactionID, m.ID, e.now(), cfg.Settings.MemoryHours, cfg.Settings.RetentionDays)
}

// Caller holds the control lock; reset/configuration already cancelled these jobs.
func (e *Engine) cancelClearedThreadRuns() {
	for id, cancel := range e.pending {
		j, _, _ := e.Store.JobPlan(id)
		if j.State == "cancelled" {
			cancel()
		}
	}
}
