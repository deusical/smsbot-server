package bot

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/deusical/cmdflow"
	"github.com/deusical/smsbot-server/internal/media"
	"github.com/deusical/smsbot-server/internal/store"
	"github.com/deusical/smsbot-server/internal/transport"
)

// SmsBot is a host of the cmdflow engine (github.com/deusical/cmdflow). A command is a
// pipeline started by the "message" trigger (the command message); SmsBot
// registers the modules, keeps runs in its encrypted job plans, and is the
// only gate for the Reply step's effect: the dispatcher's checks, persisted
// intent and uncertain-send handling.

type (
	Pipeline         = cmdflow.Pipeline
	PipelineStep     = cmdflow.StepConfig
	ModuleDefinition = cmdflow.Definition
	Port             = cmdflow.Port
	SettingsSchema   = cmdflow.Schema
	PipelineError    = cmdflow.Error
	module           = cmdflow.Module[*runCtx]
)

// Value types passed between steps.
const (
	typeText  = "text"
	typeURL   = "url"
	typeFile  = "file"
	typeVideo = "video"
	// typeStoredVideo is an original kept by the worker; only a later step of
	// the same job can read it.
	typeStoredVideo = "stored_video"
	typeMessage     = "message"
	typeContext     = "context"
)

const (
	effectPure  = cmdflow.Pure
	effectLocal = cmdflow.Local
	effectPaid  = cmdflow.Paid
	effectSink  = cmdflow.Sink
)

const maxPipelineSteps = 8

// commands is SmsBot's engine. Its policy: at most 8 steps, ending with the
// one Reply, which every effect goes through.
var commands = cmdflow.New[*runCtx](cmdflow.Policy{
	MaxSteps:       maxPipelineSteps,
	MaxEffects:     1,
	EffectLast:     true,
	RequireEffect:  true,
	DefaultTrigger: "message",
	Check:          conversationRules,
})

func init() {
	for _, t := range []cmdflow.Type{
		{ID: typeText, Title: "Text"},
		{ID: typeURL, Title: "Link"},
		{ID: typeFile, Title: "File"},
		{ID: typeVideo, Title: "Video"},
		{ID: typeStoredVideo, Title: "Original video"},
		{ID: typeMessage, Title: "Message"},
		{ID: typeContext, Title: "Conversation"},
	} {
		commands.RegisterType(t)
	}
	commands.RegisterTrigger(cmdflow.Trigger{ID: "message", Title: "Command message", Outputs: triggerOutputs})
	commands.RegisterUpgrade(upgradeVideoSteps)
}

func registerModule(m module) { commands.Register(m) }

// ModuleDefinitions lists the registered modules, in a stable order.
func ModuleDefinitions() []ModuleDefinition { return commands.Definitions() }

// triggerOutputs are what the command message provides to the first steps.
var triggerOutputs = []Port{
	{Name: "args", Title: "Text after the command", Types: []string{typeText}},
	{Name: "message", Title: "The command message", Types: []string{typeMessage}},
}

type pipelineEnv struct {
	profiles map[string]bool
}

func envOf(env any) pipelineEnv {
	e, _ := env.(pipelineEnv)
	return e
}

// validatePipeline checks a pipeline and normalizes its settings and versions
// in place. It runs whenever a command is saved or loaded.
func validatePipeline(p *Pipeline, env pipelineEnv) error {
	if p.ExecutionMinutes < 0 || p.ExecutionMinutes > 60 {
		return cmdflow.Errorf("", "", "execution minutes must be between 1 and 60, or 0 for 15")
	}
	return commands.Validate(p, env)
}

// conversationRules are SmsBot's rules for AI conversations and replies. An
// AI conversation (recent memory or a thread) is one turn per job: the AI
// step that reads it must be the one whose conversation the reply continues.
func conversationRules(p *Pipeline, info cmdflow.Info) error {
	threads := 0
	reply := p.Steps[len(p.Steps)-1]
	for _, s := range p.Steps {
		if s.Module != "context.rolling" && s.Module != "context.thread" {
			continue
		}
		if s.Module == "context.thread" {
			if threads++; threads > 1 {
				return cmdflow.Errorf(s.ID, "", "a command can continue one AI thread")
			}
		}
		users := info.Consumers[s.ID+".context"]
		if len(users) != 1 {
			return cmdflow.Errorf(s.ID, "", "connect this conversation to exactly one AI step")
		}
		ai := p.StepByID(users[0])
		if ai == nil || ai.Module != "ai.chat" {
			return cmdflow.Errorf(s.ID, "", "connect this conversation to an AI step")
		}
		if reply.Inputs["continues"] != ai.ID+".context" {
			return cmdflow.Errorf(reply.ID, "continues", "the reply must continue the AI step's conversation")
		}
		// A thread records the question when the command arrives; recent
		// memory records the turn when the reply is sent.
		for _, port := range []string{"prompt", "file"} {
			if b := ai.Inputs[port]; b != "" && s.Module == "context.thread" {
				from, _, _ := strings.Cut(b, ".")
				if !info.Immediate[from] {
					return cmdflow.Errorf(ai.ID, port, "with reply threads, this must come from a step that finishes when the command arrives")
				}
			}
		}
	}
	if reply.Inputs["text"] == "" && reply.Inputs["file"] == "" {
		return cmdflow.Errorf(reply.ID, "", "connect text or a file to send")
	}
	// The conversation remembers what the reply sends, which may be the AI
	// answer itself or a later step's version of it (a translation, say).
	if reply.Inputs["continues"] != "" && reply.Inputs["text"] == "" {
		return cmdflow.Errorf(reply.ID, "continues", "a reply that continues a conversation sends text")
	}
	return nil
}

// longRunning reports whether the pipeline's work can outlast the two-minute
// freshness window: a long-running step, or several AI requests one after
// another. Such a job gets the pipeline's execution deadline instead.
func longRunning(p Pipeline) bool {
	paid := 0
	for _, s := range p.Steps {
		m, _ := commands.Module(s.Module)
		if m.LongRunning {
			return true
		}
		if m.Effect == effectPaid {
			paid++
		}
	}
	return paid > 1
}

// turnStep is the AI step whose request is the job's conversation turn: the
// one reading a conversation, else the first AI step. The dispatcher reads
// that step's saved state when it records the turn.
func turnStep(steps []cmdflow.StepRun) string {
	first := ""
	for _, s := range steps {
		if s.Module != "ai.chat" {
			continue
		}
		if first == "" {
			first = s.ID
		}
		from, _, _ := strings.Cut(s.Inputs["context"], ".")
		for _, c := range steps {
			if c.ID == from && (c.Module == "context.rolling" || c.Module == "context.thread") {
				return s.ID
			}
		}
	}
	return first
}

var strictSettings = cmdflow.StrictSettings

var bound = cmdflow.Bound

func noSettings(raw json.RawMessage, env any) (json.RawMessage, error) {
	return cmdflow.NoSettings(raw, env)
}

// runCtx is what SmsBot's modules receive: the engine and, depending on the
// phase, the command being accepted, the job being dispatched, or a preview.
type runCtx struct {
	e   *Engine
	ctx context.Context
	p   *store.Plan
	cfg *Configuration
	// Acceptance.
	m        transport.Incoming
	cmd      Command
	pipeline Pipeline
	parent   *store.AIParent
	// Dispatch.
	j store.Job
	// Preview.
	sim *simState
}

// simState is what a preview collects.
type simState struct {
	model, text string
}

// planHost is SmsBot's side of a run. The reply text stays on the run. A file
// is taken onto the plan so its bytes can be sealed apart from the run. A
// halt stores its text on the step and drops any AI turn the plan had started.
type planHost struct {
	e *Engine
	j store.Job
	p *store.Plan
}

func (h planHost) Effect(s *cmdflow.Step) error {
	if v, ok := s.Take("file"); ok {
		if f := valueMedia(v); f != nil {
			h.p.Attachments = []media.File{*f}
		}
	}
	return nil
}

func (h planHost) Halt(s *cmdflow.Step, v cmdflow.Value) error {
	if s.Outputs == nil {
		s.Outputs = map[string]cmdflow.Value{}
	}
	s.Outputs["halt"] = v
	haltPlan(h.p)
	return nil
}

func (h planHost) Save() error {
	if h.j.ID == "" {
		return nil // acceptance: the plan is enqueued afterwards
	}
	return h.e.Store.SavePlan(h.j.ID, *h.p)
}

func (h planHost) Fail(code string) error {
	if h.j.ID == "" {
		return errors.New(code)
	}
	return h.e.Store.Complete(h.j.ID, "failed", code)
}

// haltPlan drops an AI turn a halted run must not record. The reply text is
// on the halted step.
func haltPlan(p *store.Plan) {
	p.Model = ""
	p.Input = ""
	p.Instructions = ""
	p.Generation = nil
	p.File = nil
	p.Memory = false
	p.Thread = nil
	p.Context = nil
	p.Attachments = nil
}

// planReply is the text the dispatcher sends: a step that halted, else the
// reply step's text, else a built-in reply stored on the plan.
func planReply(p store.Plan) string {
	if t, ok := haltedText(p.Pipeline); ok {
		return t
	}
	if t := stepText(p.Pipeline, "reply", "text"); t != "" {
		return t
	}
	return p.Reply
}

func haltedText(run *cmdflow.Run) (string, bool) {
	if run == nil {
		return "", false
	}
	for _, s := range run.Steps {
		if s.Status != cmdflow.StatusHalted {
			continue
		}
		if v, ok := s.Outputs["halt"]; ok && v.Text != "" {
			return v.Text, true
		}
	}
	return "", false
}

func stepText(run *cmdflow.Run, module, port string) string {
	if run == nil {
		return ""
	}
	for _, s := range run.Steps {
		if s.Module != module {
			continue
		}
		from, name, ok := strings.Cut(s.Inputs[port], ".")
		if !ok {
			continue
		}
		for _, src := range run.Steps {
			if src.ID == from {
				if v, ok := src.Outputs[name]; ok {
					return v.Text
				}
			}
		}
	}
	return ""
}

// planTurn is the AI step the reply continues, read from the run.
func planTurn(p store.Plan) (aiState, bool) {
	if p.Pipeline == nil {
		return aiState{}, false
	}
	id := turnStep(p.Pipeline.Steps)
	for _, s := range p.Pipeline.Steps {
		if s.ID == id && s.Module == "ai.chat" && len(s.State) > 0 {
			var st aiState
			if json.Unmarshal(s.State, &st) == nil {
				return st, true
			}
		}
	}
	return aiState{}, false
}

func triggerValues(args string, m transport.Incoming) map[string]cmdflow.Value {
	return map[string]cmdflow.Value{
		"args":    {Type: typeText, Text: args},
		"message": {Type: typeMessage, Handle: m.ID},
	}
}

// acceptPipeline snapshots the command's pipeline into the plan and runs every
// step that can finish at acceptance. ok is false when the command declines
// the message, as when its AI model isn't available.
func (e *Engine) acceptPipeline(ctx context.Context, m transport.Incoming, p *store.Plan, cfg Configuration, cmd Command, args string, parent *store.AIParent) (bool, error) {
	pl := commandPipeline(cmd)
	if parent != nil && !pl.Has("context.thread") {
		// Only a command that continues AI threads answers a native reply.
		return false, nil
	}
	p.Pipeline = cmdflow.Snapshot(pl, triggerValues(args, m))
	p.Approval = cmd.Approval
	if longRunning(pl) {
		minutes := pl.ExecutionMinutes
		if minutes == 0 {
			minutes = 15
		}
		p.ExecutionDeadline = e.now().Add(time.Duration(minutes) * time.Minute).UTC().Format(time.RFC3339Nano)
	}
	c := &runCtx{e: e, ctx: ctx, p: p, cfg: &cfg, m: m, cmd: cmd, pipeline: pl, parent: parent}
	switch result, err := commands.Accept(c, p.Pipeline, planHost{e: e, p: p}); {
	case err != nil:
		return false, err
	case result == cmdflow.Declined:
		return false, nil
	case result == cmdflow.Halted:
		p.ExecutionDeadline = ""
	}
	return true, nil
}

// runPipeline runs the job's remaining steps in order and delivers the
// Reply step's intent into the plan. It returns done=true when this dispatch
// round ended in a step; otherwise the plan holds the text and files to send.
func (e *Engine) runPipeline(ctx context.Context, j store.Job, p *store.Plan, cfg *Configuration) (bool, error) {
	if p.Pipeline == nil {
		return false, nil
	}
	c := &runCtx{e: e, ctx: ctx, p: p, cfg: cfg, j: j}
	done, err := commands.Resume(c, p.Pipeline, planHost{e: e, j: j, p: p})
	if errors.Is(err, cmdflow.ErrFormat) || errors.Is(err, cmdflow.ErrNotAccepted) || errors.Is(err, cmdflow.ErrFailed) {
		// A run this version can't continue, such as one stored by an
		// earlier version before a restart, fails once instead of retrying.
		return true, e.Store.Complete(j.ID, "failed", "pipeline_unavailable")
	}
	return done, err
}

// pendingImmediate rejects running an immediate step at dispatch: its inputs
// were known at acceptance, so it is already done.
func pendingImmediate(*runCtx, *cmdflow.Step) (cmdflow.Outcome, error) {
	return cmdflow.Fail("pipeline_step_incomplete"), nil
}

// yielded maps a dispatcher helper's (done, err) to an outcome: done means
// the helper already moved the job (deferred, completed or approval).
func yielded(done bool, err error) (cmdflow.Outcome, error) {
	if err != nil {
		return cmdflow.Yield(), err
	}
	if done {
		return cmdflow.Yield(), nil
	}
	return cmdflow.Done(), nil
}

// simulatePipeline previews a command offline and returns the reply text.
func simulatePipeline(cfg Configuration, cmd Command, args string) (string, string, []map[string]any, error) {
	pl := commandPipeline(cmd)
	run := cmdflow.Snapshot(pl, triggerValues(args, transport.Incoming{ID: "preview"}))
	c := &runCtx{cfg: &cfg, cmd: cmd, pipeline: pl, sim: &simState{model: "Local"}}
	traced, halt, err := commands.Simulate(c, run)
	if err != nil {
		return "", "", nil, err
	}
	trace := []map[string]any{}
	for _, t := range traced {
		outputs := map[string]string{}
		for name, v := range t.Outputs {
			outputs[name] = previewValue(v)
		}
		trace = append(trace, map[string]any{"id": t.ID, "module": t.Module, "outputs": outputs})
	}
	text := c.sim.text
	if halt != nil {
		text = halt.Text
	}
	return text, c.sim.model, trace, nil
}

func previewValue(v cmdflow.Value) string {
	switch v.Type {
	case typeText, typeURL:
		return v.Text
	case typeVideo, typeFile, typeStoredVideo:
		return "(" + v.Type + ")"
	case typeContext:
		return "(" + v.Text + " conversation)"
	}
	return "(" + v.Type + ")"
}

// Values carry SmsBot's own references: an archived file in Data, produced
// media in Data, a conversation's kind in Text.

func textValue(s string) cmdflow.Value { return cmdflow.Value{Type: typeText, Text: s} }

func fileValue(f *store.FileRef) cmdflow.Value {
	data, _ := json.Marshal(f)
	return cmdflow.Value{Type: typeFile, Data: data}
}

func valueFile(v cmdflow.Value) *store.FileRef {
	if v.Type != typeFile || len(v.Data) == 0 {
		return nil
	}
	var f store.FileRef
	if json.Unmarshal(v.Data, &f) != nil {
		return nil
	}
	return &f
}

func mediaValue(f *media.File) cmdflow.Value {
	data, _ := json.Marshal(f)
	return cmdflow.Value{Type: typeVideo, Data: data}
}

func valueMedia(v cmdflow.Value) *media.File {
	if v.Type != typeVideo || len(v.Data) == 0 {
		return nil
	}
	var f media.File
	if json.Unmarshal(v.Data, &f) != nil {
		return nil
	}
	return &f
}

func contextValue(kind string) cmdflow.Value {
	return cmdflow.Value{Type: typeContext, Text: kind}
}

func valueContext(v cmdflow.Value) string {
	if v.Type != typeContext || v.Text == "" {
		return "none"
	}
	return v.Text
}
