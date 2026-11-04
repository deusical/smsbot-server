// Package pipeline runs commands written as configuration: a trigger followed
// by a linear list of steps, each running a registered, versioned module with
// typed input and output ports and a strict settings schema. Each input is
// connected to the trigger or to an earlier step's output.
//
// The engine knows nothing about what triggers, modules or effects mean. The
// host application registers them, stores runs, and decides whether an effect
// may happen. The engine guarantees:
//
//   - Configuration is checked when it is saved: connections are earlier-only
//     and type-compatible, settings match their schema, module versions exist.
//   - A run is a snapshot: the recipe, module versions and effective settings
//     it started with. Editing the command never changes a run.
//   - A run is one serializable document. Any step can yield and the run
//     resumes later, from the step it stopped at.
//   - Effects go through the host: an effect module's step produces an
//     intent (its connected inputs), and the host's Effect decides what
//     happens, unless the host's policy lets that module act directly.
//   - Composition only: configuration can connect registered modules, never
//     add code.
package pipeline

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
)

// TriggerStep is the reserved step id whose outputs are the trigger's.
const TriggerStep = "trigger"

// Effect classes describe what running a module does.
const (
	// Pure modules compute outputs from inputs and settings.
	Pure = "pure"
	// Local modules do work on the host, such as downloading a file.
	Local = "local"
	// Paid modules make requests that cost money, such as AI calls.
	Paid = "paid"
	// Sink modules act on the outside world. Unless the policy lets a module
	// act directly, its step's connected inputs are an intent the host's
	// Effect carries out or refuses.
	Sink = "sink"
)

// Type is a value type steps exchange. Is lists other types a value of this
// type also counts as: an input accepting "file" accepts a "video" whose Is
// includes "file".
type Type struct {
	ID    string   `json:"id"`
	Title string   `json:"title"`
	Is    []string `json:"is,omitempty"`
}

// Port is one named, typed input or output of a module or trigger.
type Port struct {
	Name     string   `json:"name"`
	Title    string   `json:"title"`
	Types    []string `json:"types"`
	Required bool     `json:"required,omitempty"`
}

// Trigger is a kind of event that starts a run, with the outputs it provides.
type Trigger struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Outputs []Port `json:"outputs"`
}

// Schema is the small, bounded settings form vocabulary clients render.
type Schema struct {
	Type             string            `json:"type"`
	Properties       map[string]Schema `json:"properties,omitempty"`
	Title            string            `json:"title,omitempty"`
	UI               string            `json:"ui,omitempty"`
	Default          any               `json:"default,omitempty"`
	Minimum          *float64          `json:"minimum,omitempty"`
	EffectiveMaximum *float64          `json:"effective_maximum,omitempty"`
	Maximum          *float64          `json:"maximum,omitempty"`
	Enum             []string          `json:"enum,omitempty"`
	Unit             string            `json:"unit,omitempty"`
}

// Bound returns a pointer to v, for schema bounds.
func Bound(v float64) *float64 { return &v }

// Definition is what clients (and anything composing commands) see of a module.
type Definition struct {
	ID            string `json:"id"`
	Version       int    `json:"version"`
	Title         string `json:"title"`
	Description   string `json:"description"`
	Inputs        []Port `json:"inputs"`
	Outputs       []Port `json:"outputs"`
	Effect        string `json:"effect"`
	RetryClass    string `json:"retry_class"`
	ResourceClass string `json:"resource_class"`
	// Immediate modules finish when the run is accepted, from values known
	// then. Their outputs are fixed in the accepted run.
	Immediate bool `json:"immediate,omitempty"`
	// LongRunning is a hint for the host, such as to give the run a longer
	// deadline.
	LongRunning bool   `json:"long_running,omitempty"`
	Schema      Schema `json:"settings"`
}

// Module is a registered capability. C is the host's per-run context, passed
// to every call, so modules reach the host's state without the engine
// knowing it.
type Module[C any] struct {
	Definition
	// Settings decodes raw settings strictly, applies defaults and validates
	// them, returning the normalized form saved with the command. env is
	// whatever the host passes to Validate.
	Settings func(raw json.RawMessage, env any) (json.RawMessage, error)
	// AcceptOnly modules work only at acceptance, so every input must be
	// known then.
	AcceptOnly bool
	// Admit, when set, runs for every step before any step is accepted;
	// false declines the run.
	Admit func(c C, s *Step) (bool, error)
	// Accept runs when the run is accepted: Done, Pending (runs later),
	// Halt or Decline.
	Accept func(c C, s *Step) (Outcome, error)
	// Run runs a pending step: Done, Yield, Halt or Fail.
	Run func(c C, s *Step) (Outcome, error)
	// Simulate previews the step offline: Done or Halt.
	Simulate func(c C, s *Step) (Outcome, error)
}

// Pipeline is a command's recipe as configured.
type Pipeline struct {
	// Trigger names the trigger kind; empty means the policy's default.
	Trigger string `json:"trigger,omitempty"`
	// ExecutionMinutes is for the host, such as a deadline for long runs.
	ExecutionMinutes int          `json:"execution_minutes,omitempty"`
	Steps            []StepConfig `json:"steps"`
}

// StepConfig is one configured step: a module, its settings and its connections
// (port to "trigger.<output>" or "<step>.<output>").
type StepConfig struct {
	ID       string            `json:"id"`
	Module   string            `json:"module"`
	Version  int               `json:"version,omitempty"`
	Inputs   map[string]string `json:"inputs,omitempty"`
	Settings json.RawMessage   `json:"settings,omitempty"`
}

// Clone copies a pipeline so a caller can change it without affecting the
// original.
func (p Pipeline) Clone() Pipeline {
	p.Steps = append([]StepConfig(nil), p.Steps...)
	for i := range p.Steps {
		inputs := map[string]string{}
		for k, v := range p.Steps[i].Inputs {
			inputs[k] = v
		}
		p.Steps[i].Inputs = inputs
	}
	return p
}

// Has reports whether any step runs the module.
func (p Pipeline) Has(module string) bool {
	for _, s := range p.Steps {
		if s.Module == module {
			return true
		}
	}
	return false
}

// StepByID returns the configured step with the id, or nil.
func (p Pipeline) StepByID(id string) *StepConfig {
	for i := range p.Steps {
		if p.Steps[i].ID == id {
			return &p.Steps[i]
		}
	}
	return nil
}

// Policy holds the host's rules for valid pipelines and effects.
type Policy struct {
	// MaxSteps limits the steps after the trigger; 0 means no limit.
	MaxSteps int
	// MaxEffects limits sink steps; 0 means no limit.
	MaxEffects int
	// EffectLast requires every sink step to be the last step.
	EffectLast bool
	// RequireEffect requires the last step to be a sink.
	RequireEffect bool
	// Direct reports sink modules that act themselves when they run, instead
	// of through the host's Effect.
	Direct func(module string) bool
	// DefaultTrigger is used when a pipeline names none.
	DefaultTrigger string
	// Check adds host rules after the structural checks pass.
	Check func(p *Pipeline, info Info) error
}

// Info describes a structurally valid pipeline, for Policy.Check.
type Info struct {
	// Immediate reports steps (and "trigger") whose outputs are known at
	// acceptance.
	Immediate map[string]bool
	// Consumers maps each connected binding to the steps reading it.
	Consumers map[string][]string
}

// Error explains why a pipeline is invalid, naming the step and port.
type Error struct {
	Step, Port, Reason string
}

func (e *Error) Error() string {
	where := e.Step
	if e.Port != "" {
		where += "." + e.Port
	}
	if where == "" {
		return "invalid pipeline: " + e.Reason
	}
	return "invalid pipeline step " + where + ": " + e.Reason
}

// Errorf builds an Error, for modules and Policy.Check.
func Errorf(step, port, format string, args ...any) error {
	return &Error{Step: step, Port: port, Reason: fmt.Sprintf(format, args...)}
}

var stepPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,23}$`)

// Engine holds the registered modules, types, triggers and upgrades.
type Engine[C any] struct {
	Policy   Policy
	modules  map[string]Module[C]
	types    map[string]Type
	triggers map[string]Trigger
	upgrades []func(*Pipeline)
}

// New makes an engine with the host's policy.
func New[C any](policy Policy) *Engine[C] {
	return &Engine[C]{Policy: policy, modules: map[string]Module[C]{}, types: map[string]Type{}, triggers: map[string]Trigger{}}
}

// Register adds a module. Registering an id twice panics.
func (e *Engine[C]) Register(m Module[C]) {
	if _, exists := e.modules[m.ID]; exists {
		panic("pipeline: duplicate module " + m.ID)
	}
	if m.Settings == nil {
		m.Settings = NoSettings
	}
	e.modules[m.ID] = m
}

// RegisterType adds a value type.
func (e *Engine[C]) RegisterType(t Type) { e.types[t.ID] = t }

// RegisterTrigger adds a trigger kind.
func (e *Engine[C]) RegisterTrigger(t Trigger) { e.triggers[t.ID] = t }

// RegisterUpgrade adds a function that rewrites old configuration before it
// is validated, such as a module split into two.
func (e *Engine[C]) RegisterUpgrade(f func(*Pipeline)) { e.upgrades = append(e.upgrades, f) }

// Module returns a registered module.
func (e *Engine[C]) Module(id string) (Module[C], bool) {
	m, ok := e.modules[id]
	return m, ok
}

// Definitions lists the modules in a stable order.
func (e *Engine[C]) Definitions() []Definition {
	out := []Definition{}
	for _, m := range e.modules {
		out = append(out, m.Definition)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Types lists the value types in a stable order.
func (e *Engine[C]) Types() []Type {
	out := []Type{}
	for _, t := range e.types {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Triggers lists the trigger kinds in a stable order.
func (e *Engine[C]) Triggers() []Trigger {
	out := []Trigger{}
	for _, t := range e.triggers {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Accepts reports whether a port takes a value of type typ, directly or
// through the types it counts as.
func (e *Engine[C]) Accepts(port Port, typ string) bool {
	seen := map[string]bool{}
	var walk func(string) bool
	walk = func(t string) bool {
		if seen[t] {
			return false
		}
		seen[t] = true
		for _, want := range port.Types {
			if want == t {
				return true
			}
		}
		for _, parent := range e.types[t].Is {
			if walk(parent) {
				return true
			}
		}
		return false
	}
	return walk(typ)
}

func portOf(ports []Port, name string) (Port, bool) {
	for _, p := range ports {
		if p.Name == name {
			return p, true
		}
	}
	return Port{}, false
}

// Validate applies upgrades, checks a pipeline's structure and the policy,
// and normalizes settings and versions in place. env is passed to modules'
// Settings and is otherwise the host's.
func (e *Engine[C]) Validate(p *Pipeline, env any) error {
	for _, up := range e.upgrades {
		up(p)
	}
	trigger, ok := e.triggers[e.triggerOf(*p)]
	if !ok {
		return Errorf("", "", "unknown trigger")
	}
	if len(p.Steps) == 0 {
		return Errorf("", "", "add at least one step")
	}
	if e.Policy.MaxSteps > 0 && len(p.Steps) > e.Policy.MaxSteps {
		return Errorf("", "", "at most %d steps", e.Policy.MaxSteps)
	}
	outputs := map[string]map[string]string{TriggerStep: {}}
	immediate := map[string]bool{TriggerStep: true}
	for _, o := range trigger.Outputs {
		outputs[TriggerStep][o.Name] = o.Types[0]
	}
	consumers := map[string][]string{}
	effects := 0
	for i := range p.Steps {
		s := &p.Steps[i]
		if !stepPattern.MatchString(s.ID) || s.ID == TriggerStep {
			return Errorf(s.ID, "", "step ids are 1–24 lowercase letters, digits or _")
		}
		if _, dup := outputs[s.ID]; dup {
			return Errorf(s.ID, "", "duplicate step id")
		}
		m, ok := e.modules[s.Module]
		if !ok {
			return Errorf(s.ID, "", "unknown module")
		}
		if s.Version == 0 {
			s.Version = m.Version
		}
		if s.Version != m.Version {
			return Errorf(s.ID, "", "unsupported module version")
		}
		last := i == len(p.Steps)-1
		if m.Effect == Sink {
			if effects++; e.Policy.MaxEffects > 0 && effects > e.Policy.MaxEffects {
				return Errorf(s.ID, "", "at most %d %s", e.Policy.MaxEffects, plural(e.Policy.MaxEffects, "step that acts", "steps that act"))
			}
			if e.Policy.EffectLast && !last {
				return Errorf(s.ID, "", "%s must be the last step", m.Title)
			}
		}
		if last && e.Policy.RequireEffect && m.Effect != Sink {
			return Errorf(s.ID, "", "the last step must be %s", e.sinkTitles())
		}
		for port := range s.Inputs {
			if _, ok := portOf(m.Inputs, port); !ok {
				return Errorf(s.ID, port, "unknown input")
			}
		}
		known := m.Immediate
		for _, in := range m.Inputs {
			binding := s.Inputs[in.Name]
			if binding == "" {
				if in.Required {
					return Errorf(s.ID, in.Name, "required input is not connected")
				}
				delete(s.Inputs, in.Name)
				continue
			}
			from, port, ok := strings.Cut(binding, ".")
			typ, exists := outputs[from][port]
			if !ok || !exists {
				return Errorf(s.ID, in.Name, "connect it to the trigger or an earlier step's output")
			}
			if !e.Accepts(in, typ) {
				return Errorf(s.ID, in.Name, "a %s output can't connect to this input", typ)
			}
			known = known && immediate[from]
			consumers[binding] = append(consumers[binding], s.ID)
		}
		settings, err := m.Settings(s.Settings, env)
		if err != nil {
			return Errorf(s.ID, "", "%s", err.Error())
		}
		s.Settings = settings
		outputs[s.ID] = map[string]string{}
		for _, o := range m.Outputs {
			outputs[s.ID][o.Name] = o.Types[0]
		}
		if m.AcceptOnly && !known {
			return Errorf(s.ID, "", "this step needs inputs that are known when the command arrives")
		}
		immediate[s.ID] = known
	}
	if e.Policy.Check != nil {
		return e.Policy.Check(p, Info{Immediate: immediate, Consumers: consumers})
	}
	return nil
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func (e *Engine[C]) sinkTitles() string {
	titles := []string{}
	for _, d := range e.Definitions() {
		if d.Effect == Sink {
			titles = append(titles, d.Title)
		}
	}
	if len(titles) == 0 {
		return "a step that acts"
	}
	return strings.Join(titles, " or ")
}

func (e *Engine[C]) triggerOf(p Pipeline) string {
	if p.Trigger != "" {
		return p.Trigger
	}
	return e.Policy.DefaultTrigger
}

// StrictSettings decodes raw settings into v, rejecting unknown fields and
// trailing data. Empty settings leave v's defaults.
func StrictSettings(raw json.RawMessage, v any) error {
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

// NoSettings accepts only empty settings.
func NoSettings(raw json.RawMessage, _ any) (json.RawMessage, error) {
	var v struct{}
	if err := StrictSettings(raw, &v); err != nil {
		return nil, err
	}
	return nil, nil
}
