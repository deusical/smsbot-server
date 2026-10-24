package bot

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/deusical/smsbot-server/internal/collection"
	"github.com/deusical/smsbot-server/internal/store"
	"github.com/deusical/smsbot-server/internal/transport"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Engine routes commands, generates replies and dispatches sends. It reads
// collection consent through Collection but owns none of collection's work.
type Engine struct {
	Collection *collection.Service
	Store      *store.Store
	Transport  transport.Transport
	Provider   Provider
	// mu is the control lock shared with Collection: send decisions serialize
	// with consent changes and protected commits.
	mu         *sync.Mutex
	preflight  preflightRetry
	dispatchMu sync.Mutex
	now        func() time.Time
	pending    map[string]context.CancelFunc
}

// New wires an engine without touching storage or starting work. control is
// the lock shared with the collection service. Run Init before use.
func New(s *store.Store, t transport.Transport, c *collection.Service, control *sync.Mutex) *Engine {
	var provider Provider = NewOpenRouter()
	if _, offline := t.(*transport.Fixture); offline {
		provider = SimulatedProvider{}
	}
	return &Engine{Collection: c, Store: s, Transport: t, Provider: provider, mu: control, now: time.Now, pending: map[string]context.CancelFunc{}}
}

// Init performs the engine's one-time startup work: offline fixture mode
// seeds its simulated model catalog.
func (e *Engine) Init() error {
	if _, offline := e.Transport.(*transport.Fixture); !offline {
		return nil
	}
	var models []Model
	if err := e.Store.GetPublicSetting("model_catalog", &models); err != nil {
		return err
	}
	if len(models) > 0 {
		return nil
	}
	models, _ = e.Provider.Models(context.Background())
	return e.Store.SetPublicSetting("model_catalog", models)
}

// CollectionSettingsSaved reacts to new collection settings. The collection
// service calls it under the control lock: turning automatic replies off
// cancels in-flight generation and upload work (queued jobs are cancelled in
// the same transaction that saved the settings).
func (e *Engine) CollectionSettingsSaved(s collection.Settings) {
	if s.AutomaticReplies {
		return
	}
	for _, cancel := range e.pending {
		cancel()
	}
}

func (e *Engine) lookupConversation(ctx context.Context, id string) (transport.Conversation, error) {
	return transport.Lookup(ctx, e.Transport, id)
}

func (e *Engine) Pause(value bool) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if value {
		for _, cancel := range e.pending {
			cancel()
		}
	}
	return e.Store.Pause(value)
}

// ConnectionInterrupted cancels unsent work without changing the owner's Live/Pause choice.
// Submitted or uncertain intents retain their state and are never retried here.
func (e *Engine) ConnectionInterrupted() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, cancel := range e.pending {
		cancel()
	}
	return e.Store.CancelUnsentJobs("connection_interrupted")
}

func (e *Engine) Unenroll(id string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	for job, cancel := range e.pending {
		j, _, err := e.Store.JobPlan(job)
		if err == nil && j.ConversationID == id {
			cancel()
		}
	}
	if err := e.Store.Unenroll(id); err != nil {
		return err
	}
	return e.Store.ClearMemory(id)
}
func (e *Engine) Enroll(id, peer string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.Store.Enroll(id, peer)
}
func (e *Engine) Cancel(id string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if cancel := e.pending[id]; cancel != nil {
		cancel()
	}
	return e.Store.Cancel(id)
}
func (e *Engine) Approve(id string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	j, p, err := e.Store.JobPlan(id)
	if err != nil {
		return errors.New("approval_unavailable")
	}
	if j.State != "awaiting_approval" {
		return errors.New("approval_unavailable")
	}
	at, err := time.Parse(time.RFC3339Nano, j.CreatedAt)
	if err != nil || e.now().Sub(at) > 2*time.Minute {
		e.Store.Complete(id, "expired", "approval_expired")
		return errors.New("approval_expired")
	}
	paused, err := e.Store.Paused()
	if err != nil {
		return err
	}
	if paused || !e.Store.Enrolled(j.ConversationID, p.Participant) {
		return errors.New("approval_unavailable")
	}
	p.Approved = true
	if err = e.Store.SavePlan(id, p); err != nil {
		return err
	}
	ok, err := e.Store.Transition(id, "awaiting_approval", "queued")
	if err == nil && !ok {
		return errors.New("approval_unavailable")
	}
	return err
}
func (e *Engine) validConversation(ctx context.Context, conv, peer string) (bool, error) {
	if _, ok := e.Transport.(transport.ConversationLookup); ok {
		c, err := e.lookupConversation(ctx, conv)
		return err == nil && c.ParticipantID == peer && !c.Group && c.Writable, err
	}
	list, err := e.Transport.Conversations(ctx)
	if err != nil {
		return false, err
	}
	for _, c := range list {
		if c.ID == conv && c.ParticipantID == peer && !c.Group && c.Writable {
			return true, nil
		}
	}
	return false, nil
}
func (e *Engine) Accept(ctx context.Context, m transport.Incoming) (bool, error) {
	// Ordinary chat text never needs a command identity RPC or engine lock.
	if !strings.HasPrefix(strings.TrimSpace(m.Text), "!") {
		return false, nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	age := e.now().Sub(m.At)
	if len(m.Attachments) > 0 || m.ID == "" || m.Historical || age > 2*time.Minute || age < -5*time.Second || len(m.Text) > 12000 {
		return false, nil
	}
	paused, err := e.Store.Paused()
	if err != nil {
		return false, err
	}
	if paused || !e.Transport.Ready() {
		return false, nil
	}
	cfg, err := e.Configuration()
	if err != nil {
		return false, err
	}
	// The owner's own messages carry the owner's participant, so the
	// enrolled peer stands in for every identity check that follows.
	peer := m.ParticipantID
	if m.Outgoing {
		if !cfg.Settings.OwnerCommands {
			return false, nil
		}
		var ok bool
		if peer, ok = e.Store.EnrolledParticipant(m.ConversationID); !ok {
			return false, nil
		}
		ours, err := e.Store.SentByBot(m.ConversationID, m.TransactionID, m.Text)
		if err != nil || ours {
			return false, err
		}
	}
	if !e.Store.Enrolled(m.ConversationID, peer) {
		return false, nil
	}
	inbox, err := e.Collection.Settings()
	if err != nil {
		return false, err
	}
	if !inbox.AutomaticReplies {
		return false, nil
	}
	valid, err := e.validConversation(ctx, m.ConversationID, peer)
	if err != nil || !valid {
		return false, err
	}
	if quiet(cfg.Settings, e.now()) {
		return false, nil
	}

	text := strings.TrimSpace(m.Text)
	id := store.Hash(m.ConversationID + "\x00" + m.ID)
	exists, err := e.Store.JobExists(id)
	if err != nil {
		return false, err
	}
	if exists {
		return false, nil
	}
	parts := strings.Fields(text)
	if len(parts) == 0 || !strings.HasPrefix(parts[0], "!") {
		return false, nil
	}
	name := strings.TrimPrefix(parts[0], "!")
	input := strings.TrimSpace(strings.TrimPrefix(text, parts[0]))
	limitOK, err := e.Store.Limits(e.now().UTC(), cfg.Settings.HourlyLimit, cfg.Settings.DailyLimit, 0, 0)
	if err != nil || !limitOK {
		return false, err
	}
	p := store.Plan{Participant: peer, MaxChars: cfg.Settings.MaxChars, Owner: m.Outgoing}
	switch name {
	case "ping":
		if input != "" {
			return false, nil
		}
		p.Reply = "pong"
	case "help":
		if input != "" {
			return false, nil
		}
		names := []string{"!ping", "!help", "!status", "!new"}
		for _, cmd := range cfg.Commands {
			if cmd.Enabled && allowed(cmd, m.ConversationID) {
				names = append(names, "!"+cmd.Name)
			}
		}
		p.Reply = "Commands: " + strings.Join(names, ", ")
	case "status":
		if input != "" {
			return false, nil
		}
		p.Reply = "Bot active. Default model: " + cfg.Settings.DefaultModel
	case "new":
		if input != "" {
			return false, nil
		}
		if err = e.Store.ClearMemory(m.ConversationID); err != nil {
			return false, err
		}
		p.Reply = "Conversation memory cleared."
	case "more":
		if input != "" {
			return false, nil
		}
		p.Reply = "Paired replies are sent in full, subject to the configured character limit."
	default:
		cmd, model, instructions, g, ok := resolve(cfg, name)
		if !ok || !allowed(cmd, m.ConversationID) {
			return false, nil
		}
		name = cmd.Name
		p.Memory = cmd.Memory
		if p.Memory {
			p.MemoryEpoch, err = e.Store.MemoryEpoch(m.ConversationID)
			if err != nil {
				return false, err
			}
			p.Context, err = e.Store.Memory(memoryScope(m.ConversationID, "!"+name, model), e.now())
			if err != nil {
				return false, err
			}
		}
		p.MemoryHours = cfg.Settings.MemoryHours
		p.Approval = cmd.Approval
		p.Input = input
		if cmd.Type == "fixed" {
			p.Reply = strings.ReplaceAll(instructions, "{input}", input)
		} else {
			if model == "" {
				return false, nil
			}
			selectedModel, err := e.model(model)
			if err != nil {
				return false, err
			}
			if !e.ProviderConfiguredFor(selectedModel) {
				return false, nil
			}
			if err = validateModel(g, selectedModel); err != nil {
				return false, err
			}
			p.Model = model
			p.Instructions = instructions
			p.Generation, _ = json.Marshal(g)
		}
	}
	accepted, err := e.Store.Enqueue(id, m.ConversationID, "!"+name, p)
	if err != nil || !accepted {
		return accepted, err
	}
	if m.Outgoing {
		// The phone's copy of the owner's message is usually saved already,
		// under the dashboard send when it came from there.
		known, err := e.Store.MessageKnown(m.ConversationID, m.ID, "outgoing", m.TransactionID)
		if err != nil || known {
			return true, err
		}
		return true, e.Store.SaveMessage(id, m.ConversationID, "outgoing", text)
	}
	return true, e.Store.SaveMessage(id, m.ConversationID, "incoming", text)
}
func memoryScope(conv, command, model string) string {
	return store.Hash(conv) + ":" + store.Hash(command+"\x00"+model)
}
func (e *Engine) check(ctx context.Context, j store.Job, p store.Plan) (Configuration, string, error) {
	cfg, err := e.Configuration()
	if err != nil {
		return cfg, "", err
	}
	paused, err := e.Store.Paused()
	if err != nil {
		return cfg, "", err
	}
	if paused {
		return cfg, "paused", nil
	}
	if !e.Transport.Ready() {
		return cfg, "transport_unavailable", nil
	}
	at, err := time.Parse(time.RFC3339Nano, j.CreatedAt)
	if err != nil || e.now().Sub(at) > 2*time.Minute {
		return cfg, "stale", nil
	}
	if quiet(cfg.Settings, e.now()) {
		return cfg, "quiet_hours", nil
	}
	inbox, err := e.Collection.Settings()
	if err != nil {
		return cfg, "", err
	}
	if !p.Manual && !inbox.AutomaticReplies {
		return cfg, "automatic_replies_disabled", nil
	}
	if p.InboxOwnerOnly {
		if err := inbox.FileLimits().Check(p.Attachments); err != nil {
			return cfg, "attachment_limits_changed", nil
		}
		c, err := e.lookupConversation(ctx, j.ConversationID)
		if err != nil {
			return cfg, "", err
		}
		if !inbox.Allows(c) || c.ParticipantID != p.Participant {
			return cfg, "inbox_access_disabled", nil
		}
	} else if !e.Store.Enrolled(j.ConversationID, p.Participant) {
		return cfg, "unenrolled", nil
	}
	if p.Owner && !cfg.Settings.OwnerCommands {
		return cfg, "owner_commands_disabled", nil
	}
	valid, err := e.validConversation(ctx, j.ConversationID, p.Participant)
	if err != nil {
		return cfg, "", err
	}
	if !valid {
		return cfg, "identity_changed", nil
	}
	if !p.Manual && !strings.Contains("|!ping|!help|!status|!new|!more|", "|"+j.Command+"|") {
		cmd, _, _, _, ok := resolve(cfg, strings.TrimPrefix(j.Command, "!"))
		if !ok || !allowed(cmd, j.ConversationID) {
			return cfg, "command_unavailable", nil
		}
		if cmd.Approval && !p.Approved {
			return cfg, "approval_required", nil
		}
	}
	return cfg, "", nil
}

// Dispatch sends at most one queued job. The control lock is held throughout,
// except while a provider generates text or files upload, so pause and consent
// changes stay responsive; every check is repeated after the lock is regained.
// The send itself is serialized with pause and enrollment, and its intent is
// persisted (submitting, with a transaction ID) before the transport call.
func (e *Engine) Dispatch(ctx context.Context) error {
	ctx = transport.WithPhonePurpose(ctx, "send")
	e.dispatchMu.Lock()
	defer e.dispatchMu.Unlock()
	e.mu.Lock()
	j, p, cfg, ready, err := e.nextReady(ctx)
	if !ready || err != nil {
		e.mu.Unlock()
		return err
	}
	if p.Mutation != nil {
		return e.dispatchReaction(ctx, j, p) // releases the lock
	}
	defer e.mu.Unlock()
	history, err := e.memoryContext(j, cfg, &p)
	if err != nil {
		return err
	}
	if done, err := e.generateReply(ctx, j, &p, &cfg, history); done {
		return err
	}
	if done, err := e.prepareFiles(ctx, j, &p); done {
		return err
	}
	return e.submit(ctx, j, p, cfg, history)
}

// nextReady returns the next queued job that passes every pre-send check.
// A job that fails a check is completed or sent for approval instead; a check
// that errors backs off before the same job is tried again. Caller holds the
// control lock.
func (e *Engine) nextReady(ctx context.Context) (j store.Job, p store.Plan, cfg Configuration, ready bool, err error) {
	paused, err := e.Store.Paused()
	if err != nil || paused || !e.Transport.Ready() {
		return j, p, cfg, false, err
	}
	j, p, err = e.Store.Next()
	if errors.Is(err, sql.ErrNoRows) {
		return j, p, cfg, false, nil
	}
	if err != nil {
		return j, p, cfg, false, err
	}
	if e.preflight.job != j.ID {
		e.preflight = preflightRetry{job: j.ID}
	}
	if e.now().Before(e.preflight.until) {
		return j, p, cfg, false, nil
	}
	cfg, code, err := e.check(ctx, j, p)
	if err != nil {
		e.preflight.failures++
		e.preflight.until = e.now().Add(min(30*time.Second, time.Second*time.Duration(1<<min(e.preflight.failures-1, 5))))
		return j, p, cfg, false, err
	}
	e.preflight = preflightRetry{}
	if code != "" {
		if code == "approval_required" {
			_, err = e.Store.Transition(j.ID, "queued", "awaiting_approval")
		} else {
			state := "cancelled"
			if code == "stale" {
				state = "expired"
			}
			err = e.Store.Complete(j.ID, state, code)
		}
		return j, p, cfg, false, err
	}
	return j, p, cfg, true, nil
}

// memoryContext returns the conversation memory a generation may use, or none
// when memory was cleared or disabled after the job was planned.
func (e *Engine) memoryContext(j store.Job, cfg Configuration, p *store.Plan) ([]store.Turn, error) {
	history := p.Context
	if p.Memory {
		epoch, epochErr := e.Store.MemoryEpoch(j.ConversationID)
		cmd, _, _, _, enabled := resolve(cfg, strings.TrimPrefix(j.Command, "!"))
		if epochErr != nil {
			return nil, epochErr
		}
		if epoch != p.MemoryEpoch || !enabled || !cmd.Memory {
			p.Memory = false
			history = nil
		}
	}
	return history, nil
}

// generateReply asks the provider for an AI reply, within the daily budget.
// The lock is released only for the provider call; the job state and every
// pre-send check are verified again afterwards. done reports that dispatch
// ended here (the job completed, moved to approval, or an error).
func (e *Engine) generateReply(ctx context.Context, j store.Job, p *store.Plan, cfg *Configuration, history []store.Turn) (done bool, err error) {
	if p.Model == "" || p.Reply != "" {
		return false, nil
	}

	m, err := e.model(p.Model)
	if err != nil {
		err = e.Store.Complete(j.ID, "failed", "model_unavailable")
		return true, err
	}
	var g Generation
	if json.Unmarshal(p.Generation, &g) != nil || validateModel(g, m) != nil {
		err = e.Store.Complete(j.ID, "failed", "generation_unsupported")
		return true, err
	}
	reserve := m.OutputPrice*float64(g.Tokens) + m.InputPrice*float64(len(p.Input)+len(p.Instructions)+512)
	for _, turn := range history {
		reserve += m.InputPrice * float64(len(turn.Content)+64)
	}
	day := e.now().UTC().Format("2006-01-02") + "T00:00:00Z"
	spent, unknown, err := e.Store.SpendSince(day)
	if err != nil {
		return true, err
	}
	if cfg.Settings.DailyBudget > 0 && (!m.PricingKnown || unknown > 0 || spent+reserve > cfg.Settings.DailyBudget) {
		err = e.Store.Complete(j.ID, "failed", "budget_exceeded_or_unknown")
		return true, err
	}
	provider, key, err := e.providerForModel(m)
	if err != nil || len(key) == 0 {
		err = e.Store.Complete(j.ID, "failed", "provider_key_missing")
		return true, err
	}
	ok, err := e.Store.Transition(j.ID, "queued", "generating")
	if err != nil || !ok {
		return true, err
	}
	if err = e.Store.Usage(j.ID, p.Model, 0, 0, 0, nil); err != nil {
		return true, err
	}
	if err = e.Store.SetPublicSetting("requested_reasoning:"+j.ID, g.Reasoning); err != nil {
		return true, err
	}
	genctx, cancel := context.WithTimeout(ctx, 95*time.Second)
	e.pending[j.ID] = cancel
	e.mu.Unlock()
	result, generr := provider.Generate(genctx, key, *p, history, m)
	cancel()
	e.mu.Lock()
	delete(e.pending, j.ID)
	reportedModel := result.Model
	if reportedModel == "" {
		reportedModel = p.Model
	}
	if err = e.Store.Usage(j.ID, reportedModel, result.PromptTokens, result.CompletionTokens, result.ReasoningTokens, result.Cost); err != nil {
		return true, err
	}
	current, _, loaderr := e.Store.JobPlan(j.ID)
	if loaderr != nil && current.State == "generating" {
		return true, loaderr
	}
	if loaderr != nil || current.State != "generating" {
		return true, nil
	}
	if generr != nil {
		err = e.Store.Complete(j.ID, "failed", safeProviderCode(generr))
		return true, err
	}
	p.Reply = result.Text
	checked, code, err := e.check(ctx, j, *p)
	*cfg = checked
	if err != nil {
		e.Store.Complete(j.ID, "failed", "final_check_failed")
		return true, err
	}
	if code != "" {
		if code == "approval_required" {
			p.Approval = true
			p.Approved = false
			if err = e.Store.SavePlan(j.ID, *p); err != nil {
				return true, err
			}
			_, err = e.Store.Transition(j.ID, "generating", "awaiting_approval")
		} else {
			err = e.Store.Complete(j.ID, "cancelled", code)
		}
		return true, err
	}
	if err = e.Store.SavePlan(j.ID, *p); err != nil {
		return true, err
	}
	if _, err = e.Store.Transition(j.ID, "generating", "queued"); err != nil {
		return true, err
	}

	return false, nil
}

// Uploading does not send a message. Release the control lock so pause and
// consent revocation remain responsive, then repeat every final-send check.
// done reports that dispatch ended here.
func (e *Engine) prepareFiles(ctx context.Context, j store.Job, p *store.Plan) (done bool, err error) {
	if len(p.Attachments) == 0 {
		return false, nil
	}

	preparer, ok := e.Transport.(transport.FilePreparer)
	if !ok {
		err = e.Store.Complete(j.ID, "failed", "attachments_unavailable")
		return true, err
	}
	transitioned, transitionErr := e.Store.Transition(j.ID, "queued", "generating")
	if transitionErr != nil || !transitioned {
		return true, transitionErr
	}
	uploadCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	e.pending[j.ID] = cancel
	e.mu.Unlock()
	files, uploadErr := preparer.PrepareFiles(uploadCtx, p.Attachments)
	cancel()
	e.mu.Lock()
	delete(e.pending, j.ID)
	current, _, loadErr := e.Store.JobPlan(j.ID)
	// Cancellation clears the plan, so a known terminal state may have a
	// plan-decode error. A failed SQL lookup has no state and must propagate.
	if current.State != "" && current.State != "generating" {
		return true, nil
	}
	if loadErr != nil {
		return true, loadErr
	}
	if uploadErr != nil {
		err = e.Store.Complete(j.ID, "failed", "attachment_upload_failed")
		return true, err
	}
	_, code, checkErr := e.check(ctx, j, *p)
	if checkErr != nil || code != "" {
		err = e.Store.Complete(j.ID, "cancelled", "attachment_final_check_failed")
		return true, err
	}
	p.Attachments = files
	if err = e.Store.SavePlan(j.ID, *p); err != nil {
		return true, err
	}
	if _, err = e.Store.Transition(j.ID, "generating", "queued"); err != nil {
		return true, err
	}

	return false, nil
}

// submit persists the send intent (submitting, with a new transaction ID),
// then calls the transport once. A transport error other than a definite
// non-submission is recorded as unknown_submission and never retried.
func (e *Engine) submit(ctx context.Context, j store.Job, p store.Plan, cfg Configuration, history []store.Turn) error {
	current, _, err := e.Store.JobPlan(j.ID)
	if err != nil || current.State != "queued" {
		return err
	}
	if p.Reply == "" && len(p.Attachments) == 0 {
		err = e.Store.Complete(j.ID, "failed", "empty_reply")
		return err
	}
	max := p.MaxChars
	if max == 0 {
		max = 12000
	}
	runes := []rune(p.Reply)
	if len(runes) > max {
		p.Reply = string(runes[:max-25]) + "\n[Reply shortened.]"
	}
	if p.ReplyTarget != "" {
		c, lookupErr := e.lookupConversation(ctx, j.ConversationID)
		if lookupErr != nil {
			return lookupErr
		}
		v, targetErr := e.replyTarget(ctx, c, p.ReplyTarget)
		if targetErr != nil || v.UpstreamID != p.ReplyWireTarget {
			err = e.Store.Complete(j.ID, "failed", "reply_unavailable")
			return err
		}
	}
	txn := store.TransactionID()
	if err = e.Store.Submitting(j.ID, txn); err != nil {
		return err
	}
	// The send is serialized with pause/enrollment; once invoked its outcome may be uncertain.
	sendctx, cancelSend := context.WithTimeout(ctx, 30*time.Second)
	var receipt transport.Receipt
	var senderr error
	if p.ReplyTarget != "" {
		if sender, ok := e.Transport.(transport.ReplySender); ok {
			receipt, senderr = sender.SendReply(sendctx, j.ConversationID, txn, p.Reply, p.ReplyWireTarget, p.Attachments)
		} else {
			senderr = transport.ErrNotSubmitted
		}
	} else if len(p.Attachments) > 0 {
		if sender, ok := e.Transport.(transport.FileSender); ok {
			receipt, senderr = sender.SendFiles(sendctx, j.ConversationID, txn, p.Reply, p.Attachments)
		} else {
			senderr = transport.ErrNotSubmitted
		}
	} else {
		receipt, senderr = e.Transport.Send(sendctx, j.ConversationID, txn, p.Reply)
	}
	cancelSend()
	if senderr != nil {
		state, code := "unknown_submission", "transport_outcome_unknown"
		if errors.Is(senderr, transport.ErrNotSubmitted) {
			state, code = "failed", "not_submitted"
		}
		err = e.Store.Complete(j.ID, state, code)
		return err
	}
	if receipt.State != "submitted" {
		err = e.Store.Complete(j.ID, "unknown_submission", "unrecognized_receipt")
		return err
	}
	if len(p.Attachments) > 0 {
		err = e.Store.SaveSentFiles(txn, j.ConversationID, p.Reply, p.Attachments)
	} else {
		err = e.Store.SaveMessage(txn, j.ConversationID, "outgoing", p.Reply)
	}
	if err != nil {
		return err
	}
	if p.ReplyTarget != "" {
		if err = e.Store.SaveReplyRelationship(txn, j.ConversationID, p.ReplyWireTarget); err != nil {
			return err
		}
	}
	origin := "bot"
	if p.Manual {
		origin = "web"
	}
	if err = e.Store.RecordSubmitted(txn, origin, p.InboxOwnerOnly); err != nil {
		return err
	}
	if p.Model != "" && p.Memory {
		cmd, _, _, _, ok := resolve(cfg, strings.TrimPrefix(j.Command, "!"))
		epoch, epochErr := e.Store.MemoryEpoch(j.ConversationID)
		if epochErr != nil {
			return epochErr
		}
		if ok && cmd.Memory && epoch == p.MemoryEpoch {
			history = append(history, store.Turn{Role: "user", Content: p.Input}, store.Turn{Role: "assistant", Content: p.Reply})
			if err = e.Store.Remember(memoryScope(j.ConversationID, j.Command, p.Model), history, e.now().Add(time.Duration(p.MemoryHours)*time.Hour)); err != nil {
				return err
			}
		}
	}
	err = e.Store.Complete(j.ID, "submitted", "")
	return err
}

func safeProviderCode(err error) string {
	code := err.Error()
	if strings.HasPrefix(code, "provider_") && !strings.ContainsAny(code, " \r\n") {
		return code
	}
	return "provider_failed"
}
func (e *Engine) Compose(ctx context.Context, conv, text string) (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	text = strings.TrimSpace(text)
	if text == "" || utf8.RuneCountInString(text) > 12000 {
		return "", errors.New("invalid_message")
	}
	cfg, err := e.Configuration()
	if err != nil {
		return "", err
	}
	paused, err := e.Store.Paused()
	if err != nil {
		return "", err
	}
	if paused || !e.Transport.Ready() || quiet(cfg.Settings, e.now()) {
		return "", errors.New("bot_unavailable")
	}
	permitted, err := e.Store.Limits(e.now().UTC(), cfg.Settings.HourlyLimit, cfg.Settings.DailyLimit, 0, 0)
	if err != nil {
		return "", err
	}
	if !permitted {
		return "", errors.New("request_limit_reached")
	}
	list, err := e.Transport.Conversations(ctx)
	if err != nil {
		return "", err
	}
	for _, c := range list {
		if c.ID == conv && !c.Group && c.Writable && e.Store.Enrolled(conv, c.ParticipantID) {
			id := store.ID()
			_, err = e.Store.Enqueue(id, conv, "manual", store.Plan{Participant: c.ParticipantID, Reply: text, MaxChars: cfg.Settings.MaxChars, Manual: true})
			return id, err
		}
	}
	return "", errors.New("enrollment_required")
}
func (e *Engine) Playground(name, input string) (map[string]any, error) {
	cfg, err := e.Configuration()
	if err != nil {
		return nil, err
	}
	name = strings.TrimPrefix(name, "!")
	switch name {
	case "ping":
		return map[string]any{"text": "pong", "model": "Local", "simulated": true}, nil
	case "new":
		return map[string]any{"text": "Simulated memory reset. Saved memory is unchanged.", "model": "Local", "simulated": true}, nil
	case "status":
		return map[string]any{"text": "Synthetic status preview. No bot state was changed.", "model": "Local", "simulated": true}, nil
	case "help":
		return map[string]any{"text": "Commands: !ping, !help, !status, !new, !more", "model": "Local", "simulated": true}, nil
	case "more":
		return map[string]any{"text": "Paired replies are sent in full, subject to the character limit.", "model": "Local", "simulated": true}, nil
	}
	// Preview a copy only. Imported/disabled commands stay disabled on disk.
	for i := range cfg.Commands {
		if cfg.Commands[i].Name == name {
			cfg.Commands[i].Enabled = true
		}
	}
	cmd, model, instructions, _, ok := resolve(cfg, name)
	if !ok {
		return nil, errors.New("command_unavailable")
	}
	text := "Simulated AI response. No provider request was made."
	if cmd.Type == "fixed" {
		text = strings.ReplaceAll(instructions, "{input}", input)
	}
	return map[string]any{"text": text, "model": model, "simulated": true}, nil
}
func (e *Engine) Run(ctx context.Context, report func(error)) {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	retention := time.NewTicker(time.Hour)
	defer retention.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-retention.C:
			cfg, err := e.Configuration()
			if err == nil {
				_, err = e.Store.Retention(cfg.Settings.RetentionDays)
			}
			if err != nil {
				report(err)
			}
		case <-ticker.C:
			if err := e.Dispatch(ctx); err != nil {
				report(err)
			}
		}
	}
}
func (e *Engine) Reconcile(conv, txn, state, reported string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	rank := map[string]int{"submitting": 0, "unknown_submission": 0, "submitted": 1, "delivered": 2, "read": 3}
	desired, ok := rank[state]
	if !ok || desired == 0 {
		return nil
	}
	id, current, found, err := e.Store.JobByTransaction(conv, txn)
	if err != nil || !found {
		return err
	}
	previous, ok := rank[current]
	if !ok || desired < previous {
		return nil
	}
	if err = e.Store.Complete(id, state, reported); err != nil {
		return err
	}
	return e.Store.UpdateMessageStatus(conv, txn, state)
}

type preflightRetry struct {
	job      string
	until    time.Time
	failures int
}
