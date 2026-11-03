package bot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/deusical/smsbot-server/internal/store"
	"github.com/deusical/smsbot-server/internal/transport"
)

// A command is a pipeline: the trigger (the command message) followed by a
// linear list of steps. Each step runs one registered module. A module declares
// typed input ports, typed output ports and a settings schema; each input is
// bound to the trigger or to an earlier step's output. The last step is always
// the reply module, the only one whose result can be sent, and only through the
// dispatcher's checks. Commands are configuration; capabilities are modules.

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

// Module effect classes. Only a sink can lead to a send, and a pipeline has one.
const (
	effectPure  = "pure"
	effectLocal = "local"
	effectPaid  = "paid"
	effectSink  = "sink"
)

const (
	maxPipelineSteps = 8
	triggerID        = "trigger"
)

// Port is one named, typed input or output of a module.
type Port struct {
	Name     string   `json:"name"`
	Title    string   `json:"title"`
	Types    []string `json:"types"`
	Required bool     `json:"required,omitempty"`
}

// ModuleDefinition is what clients (and, later, an assistant composing
// commands) see of a module.
type ModuleDefinition struct {
	ID            string `json:"id"`
	Version       int    `json:"version"`
	Title         string `json:"title"`
	Description   string `json:"description"`
	Inputs        []Port `json:"inputs"`
	Outputs       []Port `json:"outputs"`
	Effect        string `json:"effect"`
	RetryClass    string `json:"retry_class"`
	ResourceClass string `json:"resource_class"`
	// Immediate modules finish when the command is accepted, from values that
	// are known then. Their outputs are fixed in the accepted plan.
	Immediate bool `json:"immediate,omitempty"`
	// LongRunning modules give the job an execution deadline instead of the
	// two-minute freshness window.
	LongRunning bool           `json:"long_running,omitempty"`
	Schema      SettingsSchema `json:"settings"`
}

type acceptOutcome int

const (
	stepDone    acceptOutcome = iota // outputs are set
	stepPending                      // the module runs when the job is dispatched
	stepHalt                         // the pipeline ends with acceptRun.halt as the reply
	stepDecline                      // the command is not accepted at all
)

type module struct {
	ModuleDefinition
	// settings decodes raw settings strictly, applies defaults and validates
	// them, returning the normalized form saved with the command.
	settings func(raw json.RawMessage, env pipelineEnv) (json.RawMessage, error)
	// acceptOnly modules work only from what the command message provides, so
	// every input must be known at acceptance.
	acceptOnly bool
	// admit, when set, runs for every step before any step is accepted: false
	// declines the command, as when its AI model isn't usable.
	admit  func(*Engine, *acceptRun, *stepRun) (bool, error)
	accept func(*Engine, *acceptRun, *stepRun) (acceptOutcome, error)
	// run is called by the dispatcher with the control lock held. It returns
	// yield=true when this dispatch round ends here: the module moved the job
	// to another state, deferred it, or completed it.
	run      func(*Engine, *execRun, *stepRun) (yield bool, err error)
	simulate func(*simRun, *stepRun) error
}

var modules = map[string]module{}

func registerModule(m module) {
	if _, exists := modules[m.ID]; exists {
		panic("duplicate module " + m.ID)
	}
	modules[m.ID] = m
}

// ModuleDefinitions lists the registered modules, in a stable order.
func ModuleDefinitions() []ModuleDefinition {
	out := []ModuleDefinition{}
	for _, m := range modules {
		out = append(out, m.ModuleDefinition)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// triggerOutputs are what the command message provides to the first steps.
var triggerOutputs = []Port{
	{Name: "args", Title: "Text after the command", Types: []string{typeText}},
	{Name: "message", Title: "The command message", Types: []string{typeMessage}},
}

// Pipeline is a command's recipe as configured.
type Pipeline struct {
	// ExecutionMinutes bounds a pipeline with a long-running step, from
	// acceptance to send. Zero means 15.
	ExecutionMinutes int            `json:"execution_minutes,omitempty"`
	Steps            []PipelineStep `json:"steps"`
}

type PipelineStep struct {
	ID       string            `json:"id"`
	Module   string            `json:"module"`
	Version  int               `json:"version,omitempty"`
	Inputs   map[string]string `json:"inputs,omitempty"`
	Settings json.RawMessage   `json:"settings,omitempty"`
}

var stepPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,23}$`)

type pipelineEnv struct {
	profiles map[string]bool
}

// PipelineError explains why a pipeline is invalid, naming the step and port.
type PipelineError struct {
	Step, Port, Reason string
}

func (e *PipelineError) Error() string {
	where := e.Step
	if e.Port != "" {
		where += "." + e.Port
	}
	if where == "" {
		return "invalid pipeline: " + e.Reason
	}
	return "invalid pipeline step " + where + ": " + e.Reason
}

func pipelineErr(step, port, reason string) error {
	return &PipelineError{Step: step, Port: port, Reason: reason}
}

func portOf(ports []Port, name string) (Port, bool) {
	for _, p := range ports {
		if p.Name == name {
			return p, true
		}
	}
	return Port{}, false
}

func accepts(port Port, typ string) bool {
	for _, t := range port.Types {
		if t == typ {
			return true
		}
	}
	return false
}

// validatePipeline checks a pipeline's structure and normalizes its settings
// and versions in place. It runs whenever a command is saved or loaded.
func validatePipeline(p *Pipeline, env pipelineEnv) error {
	if p.ExecutionMinutes < 0 || p.ExecutionMinutes > 60 {
		return pipelineErr("", "", "execution minutes must be between 1 and 60, or 0 for 15")
	}
	upgradeVideoSteps(p)
	if len(p.Steps) == 0 {
		return pipelineErr("", "", "add at least a reply step")
	}
	if len(p.Steps) > maxPipelineSteps {
		return pipelineErr("", "", fmt.Sprintf("at most %d steps", maxPipelineSteps))
	}
	// outputs[step][port] = type; immediate[step] = known at acceptance
	outputs := map[string]map[string]string{triggerID: {}}
	immediate := map[string]bool{triggerID: true}
	for _, o := range triggerOutputs {
		outputs[triggerID][o.Name] = o.Types[0]
	}
	threads := 0
	consumers := map[string][]string{} // binding -> step ids using it
	for i := range p.Steps {
		s := &p.Steps[i]
		if !stepPattern.MatchString(s.ID) || s.ID == triggerID {
			return pipelineErr(s.ID, "", "step ids are 1–24 lowercase letters, digits or _")
		}
		if _, dup := outputs[s.ID]; dup {
			return pipelineErr(s.ID, "", "duplicate step id")
		}
		m, ok := modules[s.Module]
		if !ok {
			return pipelineErr(s.ID, "", "unknown module")
		}
		if s.Version == 0 {
			s.Version = m.Version
		}
		if s.Version != m.Version {
			return pipelineErr(s.ID, "", "unsupported module version")
		}
		if m.Effect == effectSink && i != len(p.Steps)-1 {
			return pipelineErr(s.ID, "", "the reply must be the last step")
		}
		if i == len(p.Steps)-1 && m.Effect != effectSink {
			return pipelineErr(s.ID, "", "the last step must be the reply")
		}
		if s.Module == "context.thread" {
			if threads++; threads > 1 {
				return pipelineErr(s.ID, "", "a command can continue one AI thread")
			}
		}
		for port := range s.Inputs {
			if _, ok := portOf(m.Inputs, port); !ok {
				return pipelineErr(s.ID, port, "unknown input")
			}
		}
		known := m.Immediate
		for _, in := range m.Inputs {
			binding := s.Inputs[in.Name]
			if binding == "" {
				if in.Required {
					return pipelineErr(s.ID, in.Name, "required input is not connected")
				}
				delete(s.Inputs, in.Name)
				continue
			}
			from, port, ok := strings.Cut(binding, ".")
			typ, exists := outputs[from][port]
			if !ok || !exists {
				return pipelineErr(s.ID, in.Name, "connect it to the trigger or an earlier step's output")
			}
			if !accepts(in, typ) {
				return pipelineErr(s.ID, in.Name, "a "+typ+" output can't connect to this input")
			}
			known = known && immediate[from]
			consumers[binding] = append(consumers[binding], s.ID)
		}
		settings, err := m.settings(s.Settings, env)
		if err != nil {
			return pipelineErr(s.ID, "", err.Error())
		}
		s.Settings = settings
		outputs[s.ID] = map[string]string{}
		for _, o := range m.Outputs {
			outputs[s.ID][o.Name] = o.Types[0]
		}
		if m.acceptOnly && !known {
			return pipelineErr(s.ID, "", "this step needs inputs that are known when the command arrives")
		}
		immediate[s.ID] = known
	}
	// An AI conversation (rolling memory or a thread) is one turn per job: the
	// AI step that reads it must be the one whose answer the reply continues,
	// and the prompt must be known at acceptance, when the turn is recorded.
	for i, s := range p.Steps {
		if s.Module != "context.rolling" && s.Module != "context.thread" {
			continue
		}
		users := consumers[s.ID+".context"]
		if len(users) != 1 {
			return pipelineErr(s.ID, "", "connect this conversation to exactly one AI step")
		}
		ai := stepByID(p.Steps, users[0])
		if ai == nil || ai.Module != "ai.chat" {
			return pipelineErr(s.ID, "", "connect this conversation to an AI step")
		}
		reply := p.Steps[len(p.Steps)-1]
		if reply.Inputs["continues"] != ai.ID+".context" {
			return pipelineErr(reply.ID, "continues", "the reply must continue the AI step's conversation")
		}
		// A thread records the question when the command arrives; recent
		// memory records the turn when the reply is sent.
		for _, port := range []string{"prompt", "file"} {
			if b := ai.Inputs[port]; b != "" && s.Module == "context.thread" {
				from, _, _ := strings.Cut(b, ".")
				if !immediate[from] {
					return pipelineErr(ai.ID, port, "with reply threads, this must come from a step that finishes when the command arrives")
				}
			}
		}
		_ = i
	}
	reply := p.Steps[len(p.Steps)-1]
	if reply.Inputs["text"] == "" && reply.Inputs["file"] == "" {
		return pipelineErr(reply.ID, "", "connect text or a file to send")
	}
	// The conversation remembers what the reply sends, which may be the AI
	// answer itself or a later step's version of it (a translation, say).
	if reply.Inputs["continues"] != "" && reply.Inputs["text"] == "" {
		return pipelineErr(reply.ID, "continues", "a reply that continues a conversation sends text")
	}
	return nil
}

func stepByID(steps []PipelineStep, id string) *PipelineStep {
	for i := range steps {
		if steps[i].ID == id {
			return &steps[i]
		}
	}
	return nil
}

func (p Pipeline) has(moduleID string) bool {
	for _, s := range p.Steps {
		if s.Module == moduleID {
			return true
		}
	}
	return false
}

// longRunning reports whether the pipeline's work can outlast the two-minute
// freshness window: a long-running step, or several AI requests one after
// another. Such a job gets the pipeline's execution deadline instead.
func (p Pipeline) longRunning() bool {
	paid := 0
	for _, s := range p.Steps {
		m := modules[s.Module]
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
func turnStep(steps []store.StepRun) string {
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

// strictSettings decodes raw settings into v, rejecting unknown fields and
// trailing data. Empty settings leave v's defaults.
func strictSettings(raw json.RawMessage, v any) error {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil
	}
	if raw[0] != '{' {
		return errors.New("settings must be an object")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(v) != nil || d.Decode(new(any)) != io.EOF {
		return errors.New("invalid settings")
	}
	return nil
}

func noSettings(raw json.RawMessage, _ pipelineEnv) (json.RawMessage, error) {
	var v struct{}
	if err := strictSettings(raw, &v); err != nil {
		return nil, err
	}
	return nil, nil
}

// stepRun is a step being accepted, run or simulated, with its inputs resolved
// against the run's earlier steps.
type stepRun struct {
	*store.StepRun
	steps []store.StepRun
}

func (s *stepRun) in(port string) (store.Value, bool) {
	binding := s.Inputs[port]
	if binding == "" {
		return store.Value{}, false
	}
	from, name, _ := strings.Cut(binding, ".")
	for _, step := range s.steps {
		if step.ID == from {
			if !step.Done {
				return store.Value{}, false
			}
			v, ok := step.Outputs[name]
			return v, ok
		}
	}
	return store.Value{}, false
}

// bound reports whether port is connected, whether or not its value is known yet.
func (s *stepRun) bound(port string) bool { return s.Inputs[port] != "" }

func (s *stepRun) text(port string) string {
	v, _ := s.in(port)
	return v.Text
}

func (s *stepRun) out(port string, v store.Value) {
	if s.Outputs == nil {
		s.Outputs = map[string]store.Value{}
	}
	s.Outputs[port] = v
}

func (s *stepRun) decode(v any) error { return json.Unmarshal(s.Settings, v) }

func (s *stepRun) load(v any) error {
	if len(s.State) == 0 {
		return nil
	}
	return json.Unmarshal(s.State, v)
}

func (s *stepRun) save(v any) {
	s.State, _ = json.Marshal(v)
}

// acceptRun is a pipeline being accepted for one command message.
type acceptRun struct {
	ctx      context.Context
	m        transport.Incoming
	p        *store.Plan
	cfg      Configuration
	cmd      Command
	pipeline Pipeline
	args     string
	parent   *store.AIParent
	halt     string
}

// execRun is an accepted job being dispatched.
type execRun struct {
	ctx context.Context
	j   store.Job
	p   *store.Plan
	cfg *Configuration
}

// simRun previews a pipeline offline: no provider, worker or transport calls.
type simRun struct {
	cfg   Configuration
	cmd   Command
	args  string
	model string
	text  string
}

// snapshotPipeline fixes the command's pipeline into the plan.
func snapshotPipeline(p Pipeline, args string, m transport.Incoming) *store.PipelineRun {
	run := &store.PipelineRun{}
	trigger := store.StepRun{ID: triggerID, Module: "trigger", Version: 1, Done: true, Outputs: map[string]store.Value{
		"args":    {Type: typeText, Text: args},
		"message": {Type: typeMessage, Message: &store.MessageRef{ID: m.ID}},
	}}
	run.Steps = append(run.Steps, trigger)
	for _, s := range p.Steps {
		run.Steps = append(run.Steps, store.StepRun{ID: s.ID, Module: s.Module, Version: s.Version, Inputs: s.Inputs, Settings: s.Settings})
	}
	return run
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

// acceptPipeline snapshots the command's pipeline into the plan and runs every
// step that can finish at acceptance. ok is false when the command declines
// the message, as when its AI model isn't available.
func (e *Engine) acceptPipeline(ctx context.Context, m transport.Incoming, p *store.Plan, cfg Configuration, cmd Command, args string, parent *store.AIParent) (bool, error) {
	pl := commandPipeline(cmd)
	if parent != nil && !pl.has("context.thread") {
		// Only a command that continues AI threads answers a native reply.
		return false, nil
	}
	p.Pipeline = snapshotPipeline(pl, args, m)
	p.Approval = cmd.Approval
	if pl.longRunning() {
		minutes := pl.ExecutionMinutes
		if minutes == 0 {
			minutes = 15
		}
		p.ExecutionDeadline = e.now().Add(time.Duration(minutes) * time.Minute).UTC().Format(time.RFC3339Nano)
	}
	a := &acceptRun{ctx: ctx, m: m, p: p, cfg: cfg, cmd: cmd, pipeline: pl, args: args, parent: parent}
	for i := 1; i < len(p.Pipeline.Steps); i++ {
		s := &stepRun{StepRun: &p.Pipeline.Steps[i], steps: p.Pipeline.Steps}
		mod, ok := modules[s.Module]
		if !ok || mod.Version != s.Version {
			return false, errors.New("invalid command pipeline")
		}
		if mod.admit != nil {
			if ok, err := mod.admit(e, a, s); err != nil || !ok {
				return false, err
			}
		}
	}
	for i := 1; i < len(p.Pipeline.Steps); i++ {
		s := &stepRun{StepRun: &p.Pipeline.Steps[i], steps: p.Pipeline.Steps}
		mod, ok := modules[s.Module]
		if !ok || mod.Version != s.Version {
			return false, errors.New("invalid command pipeline")
		}
		outcome, err := mod.accept(e, a, s)
		if err != nil {
			return false, err
		}
		switch outcome {
		case stepDecline:
			return false, nil
		case stepHalt:
			// The step ran and ended the command with its reply.
			s.Done = true
			haltPlan(p, a.halt)
			p.ExecutionDeadline = ""
			return true, nil
		case stepDone:
			s.Done = true
		}
	}
	return true, nil
}

// runPipeline runs the job's remaining steps in order. It returns done=true
// when this dispatch round ended in a step; otherwise the reply step has put
// the text and files to send into the plan.
func (e *Engine) runPipeline(ctx context.Context, j store.Job, p *store.Plan, cfg *Configuration) (bool, error) {
	if p.Pipeline == nil || p.Pipeline.Halted {
		return false, nil
	}
	r := &execRun{ctx: ctx, j: j, p: p, cfg: cfg}
	for i := range p.Pipeline.Steps {
		s := &stepRun{StepRun: &p.Pipeline.Steps[i], steps: p.Pipeline.Steps}
		if s.Done {
			continue
		}
		mod, ok := modules[s.Module]
		if !ok || mod.Version != s.Version {
			return true, e.Store.Complete(j.ID, "failed", "pipeline_module_unavailable")
		}
		yield, err := mod.run(e, r, s)
		if err != nil || yield {
			return true, err
		}
		if !s.Done {
			return true, e.Store.Complete(j.ID, "failed", "pipeline_step_incomplete")
		}
		if err = e.Store.SavePlan(j.ID, *p); err != nil {
			return true, err
		}
		if p.Pipeline.Halted {
			break
		}
	}
	return false, nil
}

// pendingImmediate rejects running an immediate step at dispatch: its inputs
// were known at acceptance, so it is already done.
func pendingImmediate(e *Engine, r *execRun, s *stepRun) (bool, error) {
	return true, e.Store.Complete(r.j.ID, "failed", "pipeline_step_incomplete")
}

// simulatePipeline previews a command offline and returns the reply text.
func simulatePipeline(cfg Configuration, cmd Command, args string) (string, string, []map[string]any, error) {
	pl := commandPipeline(cmd)
	run := snapshotPipeline(pl, args, transport.Incoming{ID: "preview"})
	sim := &simRun{cfg: cfg, cmd: cmd, args: args, model: "Local"}
	trace := []map[string]any{}
	for i := 1; i < len(run.Steps); i++ {
		s := &stepRun{StepRun: &run.Steps[i], steps: run.Steps}
		mod := modules[s.Module]
		if err := mod.simulate(sim, s); err != nil {
			return "", "", nil, err
		}
		s.Done = true
		outputs := map[string]string{}
		for name, v := range s.Outputs {
			outputs[name] = previewValue(v)
		}
		trace = append(trace, map[string]any{"id": s.ID, "module": s.Module, "outputs": outputs})
		if sim.text != "" && mod.Effect != effectSink {
			// A step ended the pipeline early with a reply.
			break
		}
	}
	return sim.text, sim.model, trace, nil
}

func previewValue(v store.Value) string {
	switch v.Type {
	case typeText, typeURL:
		return v.Text
	case typeVideo, typeFile, typeStoredVideo:
		return "(" + v.Type + ")"
	case typeContext:
		if v.Context != nil {
			return "(" + v.Context.Kind + " conversation)"
		}
	}
	return "(" + v.Type + ")"
}
