package bot

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/deusical/smsbot-server/internal/media"
	"github.com/deusical/smsbot-server/internal/pipeline"
	"github.com/deusical/smsbot-server/internal/store"
	"github.com/deusical/smsbot-server/internal/transport"
)

// SmsBot is a host of the pipeline engine (internal/pipeline). A command is a
// pipeline started by the "message" trigger (the command message); SmsBot
// registers the modules, keeps runs in its encrypted job plans, and is the
// only gate for the Reply step's effect: the dispatcher's checks, persisted
// intent and uncertain-send handling.

type (
	Pipeline         = pipeline.Pipeline
	PipelineStep     = pipeline.StepConfig
	ModuleDefinition = pipeline.Definition
	Port             = pipeline.Port
	SettingsSchema   = pipeline.Schema
	PipelineError    = pipeline.Error
	module           = pipeline.Module[*runCtx]
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
	effectPure  = pipeline.Pure
	effectLocal = pipeline.Local
	effectPaid  = pipeline.Paid
	effectSink  = pipeline.Sink
)

const maxPipelineSteps = 8

// commands is SmsBot's engine. Its policy: at most 8 steps, ending with the
// one Reply, which every effect goes through.
var commands = pipeline.New[*runCtx](pipeline.Policy{
	MaxSteps:       maxPipelineSteps,
	MaxEffects:     1,
	EffectLast:     true,
	RequireEffect:  true,
	DefaultTrigger: "message",
	Check:          conversationRules,
})

func init() {
	for _, t := range []pipeline.Type{
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
	commands.RegisterTrigger(pipeline.Trigger{ID: "message", Title: "Command message", Outputs: triggerOutputs})
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
		return pipeline.Errorf("", "", "execution minutes must be between 1 and 60, or 0 for 15")
	}
	return commands.Validate(p, env)
}

// conversationRules are SmsBot's rules for AI conversations and replies. An
// AI conversation (recent memory or a thread) is one turn per job: the AI
// step that reads it must be the one whose conversation the reply continues.
func conversationRules(p *Pipeline, info pipeline.Info) error {
	threads := 0
	reply := p.Steps[len(p.Steps)-1]
	for _, s := range p.Steps {
		if s.Module != "context.rolling" && s.Module != "context.thread" {
			continue
		}
		if s.Module == "context.thread" {
			if threads++; threads > 1 {
				return pipeline.Errorf(s.ID, "", "a command can continue one AI thread")
			}
		}
		users := info.Consumers[s.ID+".context"]
		if len(users) != 1 {
			return pipeline.Errorf(s.ID, "", "connect this conversation to exactly one AI step")
		}
		ai := p.StepByID(users[0])
		if ai == nil || ai.Module != "ai.chat" {
			return pipeline.Errorf(s.ID, "", "connect this conversation to an AI step")
		}
		if reply.Inputs["continues"] != ai.ID+".context" {
			return pipeline.Errorf(reply.ID, "continues", "the reply must continue the AI step's conversation")
		}
		// A thread records the question when the command arrives; recent
		// memory records the turn when the reply is sent.
		for _, port := range []string{"prompt", "file"} {
			if b := ai.Inputs[port]; b != "" && s.Module == "context.thread" {
				from, _, _ := strings.Cut(b, ".")
				if !info.Immediate[from] {
					return pipeline.Errorf(ai.ID, port, "with reply threads, this must come from a step that finishes when the command arrives")
				}
			}
		}
	}
	if reply.Inputs["text"] == "" && reply.Inputs["file"] == "" {
		return pipeline.Errorf(reply.ID, "", "connect text or a file to send")
	}
	// The conversation remembers what the reply sends, which may be the AI
	// answer itself or a later step's version of it (a translation, say).
	if reply.Inputs["continues"] != "" && reply.Inputs["text"] == "" {
		return pipeline.Errorf(reply.ID, "continues", "a reply that continues a conversation sends text")
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
// one reading a conversation, else the first AI step. Only it sets the plan's
// AI request fields (model, prompt, earlier turns) that the dispatcher uses
// to record the turn and that Activity shows.
func turnStep(steps []pipeline.StepRun) string {
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

var strictSettings = pipeline.StrictSettings

var bound = pipeline.Bound

func noSettings(raw json.RawMessage, env any) (json.RawMessage, error) {
	return pipeline.NoSettings(raw, env)
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

// planHost is SmsBot's side of a run: the Reply step's intent fills the
// plan's text and attachment, which the dispatcher then checks and sends; a
// halt replies with a fixed text instead.
type planHost struct {
	e *Engine
	j store.Job
	p *store.Plan
}

func (h planHost) Effect(s *pipeline.Step) error {
	h.p.Reply = s.Text("text")
	// The plan carries a file's bytes once, as the reply's attachment.
	if v, ok := s.Take("file"); ok {
		if f := valueMedia(v); f != nil {
			h.p.Attachments = []media.File{*f}
		}
	}
	return nil
}

func (h planHost) Halt(_ *pipeline.Step, v pipeline.Value) error {
	haltPlan(h.p, v.Text)
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

// haltPlan ends the pipeline with a fixed reply and no AI conversation turn.
func haltPlan(p *store.Plan, text string) {
	p.Pipeline.Halted = true
	p.Reply = text
	p.Model = ""
	p.Memory = false
	p.Thread = nil
	p.Context = nil
	p.Attachments = nil
}

func triggerValues(args string, m transport.Incoming) map[string]pipeline.Value {
	return map[string]pipeline.Value{
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
	p.Pipeline = pipeline.Snapshot(pl, triggerValues(args, m))
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
	case result == pipeline.Declined:
		return false, nil
	case result == pipeline.Halted:
		p.ExecutionDeadline = ""
	}
	return true, nil
}

// runPipeline runs the job's remaining steps in order. It returns done=true
// when this dispatch round ended in a step; otherwise the reply step has put
// the text and files to send into the plan.
func (e *Engine) runPipeline(ctx context.Context, j store.Job, p *store.Plan, cfg *Configuration) (bool, error) {
	if p.Pipeline == nil {
		return false, nil
	}
	c := &runCtx{e: e, ctx: ctx, p: p, cfg: cfg, j: j}
	return commands.Resume(c, p.Pipeline, planHost{e: e, j: j, p: p})
}

// pendingImmediate rejects running an immediate step at dispatch: its inputs
// were known at acceptance, so it is already done.
func pendingImmediate(*runCtx, *pipeline.Step) (pipeline.Outcome, error) {
	return pipeline.Fail("pipeline_step_incomplete"), nil
}

// yielded maps a dispatcher helper's (done, err) to an outcome: done means
// the helper already moved the job (deferred, completed or approval).
func yielded(done bool, err error) (pipeline.Outcome, error) {
	if err != nil {
		return pipeline.Yield(), err
	}
	if done {
		return pipeline.Yield(), nil
	}
	return pipeline.Done(), nil
}

// simulatePipeline previews a command offline and returns the reply text.
func simulatePipeline(cfg Configuration, cmd Command, args string) (string, string, []map[string]any, error) {
	pl := commandPipeline(cmd)
	run := pipeline.Snapshot(pl, triggerValues(args, transport.Incoming{ID: "preview"}))
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

func previewValue(v pipeline.Value) string {
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

func textValue(s string) pipeline.Value { return pipeline.Value{Type: typeText, Text: s} }

func fileValue(f *store.FileRef) pipeline.Value {
	data, _ := json.Marshal(f)
	return pipeline.Value{Type: typeFile, Data: data}
}

func valueFile(v pipeline.Value) *store.FileRef {
	if len(v.Data) == 0 {
		return nil
	}
	var f store.FileRef
	if json.Unmarshal(v.Data, &f) != nil {
		return nil
	}
	return &f
}

func mediaValue(f *media.File) pipeline.Value {
	data, _ := json.Marshal(f)
	return pipeline.Value{Type: typeVideo, Data: data}
}

func valueMedia(v pipeline.Value) *media.File {
	if len(v.Data) == 0 {
		return nil
	}
	var f media.File
	if json.Unmarshal(v.Data, &f) != nil {
		return nil
	}
	return &f
}

func contextValue(kind string) pipeline.Value {
	return pipeline.Value{Type: typeContext, Text: kind}
}

func valueContext(v pipeline.Value) string {
	if v.Type != typeContext || v.Text == "" {
		return "none"
	}
	return v.Text
}
