package bot

import (
	"errors"
	"github.com/deusical/smsbot-server/internal/store"
	"github.com/deusical/smsbot-server/internal/transport"
	"strings"
)

// planAIThread snapshots only the selected ancestor path. Discovery and history
// collection never supply implicit context for a new request. It returns the
// thread the job adds a turn to, the earlier turns, a note for the instructions
// and, when the request can't continue, the reply that says why.
func (e *Engine) planAIThread(m transport.Incoming, p *store.Plan, parent *store.AIParent, command string, model Model, g Generation, settings Settings, input, instructions string, file *store.FileRef) (*store.AIThreadPlan, []store.Turn, string, string, error) {
	budget := 8192
	if model.ContextLength > 0 {
		available := model.ContextLength - len(input) - len(instructions) - g.Tokens - 1024
		if file != nil {
			available -= fileTokens(file)
		}
		if available < 0 {
			return nil, nil, "", "This question is too large for the selected model's context. Shorten it or choose a larger-context model.", nil
		}
		budget = min(budget, available)
	}
	thread := &store.AIThreadPlan{ID: store.ID(), RetentionDays: settings.RetentionDays}
	var history []store.Turn
	note := ""
	if parent != nil {
		turns, omitted, err := e.Store.AIHistory(*parent, m.ConversationID, p.ThreadOwner(), command, p.MemoryEpoch, e.now(), settings.MemoryHours, settings.RetentionDays, budget)
		if errors.Is(err, store.ErrAIThreadUnavailable) {
			return nil, nil, "", "That AI conversation has expired or was cleared. Send " + command + " to start a new one.", nil
		}
		if err != nil {
			return nil, nil, "", "", err
		}
		thread.ID = parent.Thread
		thread.Parent = parent.Exchange
		history = turns
		if omitted {
			note = "\nEarlier exchanges were omitted to fit the context budget. Do not claim to remember them."
		}
	}
	if m.Interaction != nil && m.Interaction.UpstreamID == m.ID && strings.Contains(strings.ToUpper(m.Interaction.Protocol), "RCS") {
		if _, ok := e.Transport.(transport.ReplySender); ok {
			thread.ReplyWire = m.ID
		}
	}
	return thread, history, note, "", nil
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
