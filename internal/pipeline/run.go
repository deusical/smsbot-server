package pipeline

import (
	"encoding/json"
	"errors"
	"strings"
)

// Run is an accepted pipeline: the snapshot it started with and each step's
// progress. It is one JSON document the host stores (encrypted, if it holds
// anything private) and passes back to Resume.
type Run struct {
	Steps []StepRun `json:"steps"`
	// Halted ends the run early with the halting step's value, already handed
	// to the host.
	Halted bool `json:"halted,omitempty"`
}

// StepRun is one step's snapshot and state. Steps[0] is the trigger.
type StepRun struct {
	ID       string            `json:"id"`
	Module   string            `json:"module"`
	Version  int               `json:"version"`
	Inputs   map[string]string `json:"inputs,omitempty"`
	Settings json.RawMessage   `json:"settings,omitempty"`
	Done     bool              `json:"done,omitempty"`
	Outputs  map[string]Value  `json:"outputs,omitempty"`
	// State is module-private progress, kept with the run.
	State json.RawMessage `json:"state,omitempty"`
}

// Value is one typed value passed between steps. Text carries text-like
// values; Handle names something the host stores (never a path); Data is any
// other host-defined content.
type Value struct {
	Type   string          `json:"type"`
	Text   string          `json:"text,omitempty"`
	Handle string          `json:"handle,omitempty"`
	Data   json.RawMessage `json:"data,omitempty"`
}

// Step is a step being accepted, run or simulated, with its inputs resolved
// against the run's earlier steps.
type Step struct {
	*StepRun
	run *Run
}

// In returns the value connected to port, if its step has finished.
func (s *Step) In(port string) (Value, bool) {
	from, name, ok := s.source(port)
	if !ok || !from.Done {
		return Value{}, false
	}
	v, ok := from.Outputs[name]
	return v, ok
}

// Take returns the value connected to port and clears its Data in the
// producing step, so large content travels once.
func (s *Step) Take(port string) (Value, bool) {
	from, name, ok := s.source(port)
	if !ok || !from.Done {
		return Value{}, false
	}
	v, ok := from.Outputs[name]
	if ok {
		kept := v
		kept.Data = nil
		from.Outputs[name] = kept
	}
	return v, ok
}

func (s *Step) source(port string) (*StepRun, string, bool) {
	binding := s.Inputs[port]
	if binding == "" {
		return nil, "", false
	}
	from, name, _ := strings.Cut(binding, ".")
	for i := range s.run.Steps {
		if s.run.Steps[i].ID == from {
			return &s.run.Steps[i], name, true
		}
	}
	return nil, "", false
}

// Bound reports whether port is connected, known yet or not.
func (s *Step) Bound(port string) bool { return s.Inputs[port] != "" }

// Text returns the text of the value connected to port, or "".
func (s *Step) Text(port string) string {
	v, _ := s.In(port)
	return v.Text
}

// Out sets an output.
func (s *Step) Out(port string, v Value) {
	if s.Outputs == nil {
		s.Outputs = map[string]Value{}
	}
	s.Outputs[port] = v
}

// Decode reads the step's (normalized) settings.
func (s *Step) Decode(v any) error { return json.Unmarshal(s.Settings, v) }

// Load reads the step's private state; nothing saved leaves v unchanged.
func (s *Step) Load(v any) error {
	if len(s.State) == 0 {
		return nil
	}
	return json.Unmarshal(s.State, v)
}

// Save stores the step's private state in the run.
func (s *Step) Save(v any) { s.State, _ = json.Marshal(v) }

// Steps is the run's steps, for hosts that read across steps.
func (s *Step) Steps() []StepRun { return s.run.Steps }

type outcomeKind int

const (
	kindDone outcomeKind = iota
	kindPending
	kindHalt
	kindDecline
	kindYield
	kindFail
)

// Outcome is what a module call reports.
type Outcome struct {
	kind  outcomeKind
	value Value
	code  string
}

// Done: the step finished; its outputs are set.
func Done() Outcome { return Outcome{kind: kindDone} }

// Pending: (at acceptance) the step runs later.
func Pending() Outcome { return Outcome{kind: kindPending} }

// Halt: the run ends here with v, which the host receives.
func Halt(v Value) Outcome { return Outcome{kind: kindHalt, value: v} }

// Decline: (at admission or acceptance) the run is not accepted.
func Decline() Outcome { return Outcome{kind: kindDecline} }

// Yield: the run pauses; the module has arranged with the host how and when
// it resumes.
func Yield() Outcome { return Outcome{kind: kindYield} }

// Fail: the run ends with a failure code, which the host records.
func Fail(code string) Outcome { return Outcome{kind: kindFail, code: code} }

// Host is how the engine reaches the host during a run.
type Host interface {
	// Effect carries out (or refuses) a sink step's intent: its connected
	// inputs. It is not called for modules the policy lets act directly.
	Effect(s *Step) error
	// Halt receives the value a step ended the run with.
	Halt(s *Step, v Value) error
	// Save persists the run after a step finishes during Resume.
	Save() error
	// Fail records that the run failed with code.
	Fail(code string) error
}

// Snapshot fixes a pipeline into a new run, with the trigger's outputs.
func Snapshot(p Pipeline, trigger map[string]Value) *Run {
	run := &Run{}
	run.Steps = append(run.Steps, StepRun{ID: TriggerStep, Module: TriggerStep, Version: 1, Done: true, Outputs: trigger})
	for _, s := range p.Steps {
		run.Steps = append(run.Steps, StepRun{ID: s.ID, Module: s.Module, Version: s.Version, Inputs: s.Inputs, Settings: s.Settings})
	}
	return run
}

// ErrUnavailable means a run names a module (or version) not registered.
var ErrUnavailable = errors.New("pipeline: module unavailable")

func (e *Engine[C]) step(run *Run, i int) (*Step, Module[C], error) {
	s := &Step{StepRun: &run.Steps[i], run: run}
	m, ok := e.modules[s.Module]
	if !ok || m.Version != s.Version {
		return s, m, ErrUnavailable
	}
	return s, m, nil
}

func (e *Engine[C]) effect(h Host, s *Step, m Module[C]) error {
	if m.Effect != Sink || (e.Policy.Direct != nil && e.Policy.Direct(m.ID)) {
		return nil
	}
	return h.Effect(s)
}

// Accepted is the result of accepting a run.
type Accepted int

const (
	// Declined: the run is not accepted at all.
	Declined Accepted = iota
	// Ready: the run is accepted; pending steps run on Resume.
	Ready
	// Halted: the run is accepted and already ended with a step's value.
	Halted
)

// Accept admits and accepts a run: every step's Admit first, then each
// step's Accept in order. Steps that finish here are done in the run.
func (e *Engine[C]) Accept(c C, run *Run, h Host) (Accepted, error) {
	for i := 1; i < len(run.Steps); i++ {
		s, m, err := e.step(run, i)
		if err != nil {
			return Declined, err
		}
		if m.Admit != nil {
			if ok, err := m.Admit(c, s); err != nil || !ok {
				return Declined, err
			}
		}
	}
	for i := 1; i < len(run.Steps); i++ {
		s, m, err := e.step(run, i)
		if err != nil {
			return Declined, err
		}
		out := Pending()
		if m.Accept != nil {
			if out, err = m.Accept(c, s); err != nil {
				return Declined, err
			}
		}
		switch out.kind {
		case kindDecline:
			return Declined, nil
		case kindHalt:
			s.Done = true
			run.Halted = true
			return Halted, h.Halt(s, out.value)
		case kindDone:
			s.Done = true
			if err := e.effect(h, s, m); err != nil {
				return Declined, err
			}
		case kindPending:
		default:
			return Declined, errors.New("pipeline: invalid outcome at acceptance")
		}
	}
	return Ready, nil
}

// Resume runs the run's remaining steps in order. stopped is true when a
// step yielded or the run failed; otherwise every step is done (or the run
// halted) and the host's effects have their intents.
func (e *Engine[C]) Resume(c C, run *Run, h Host) (stopped bool, err error) {
	if run.Halted {
		return false, nil
	}
	for i := range run.Steps {
		if run.Steps[i].Done {
			continue
		}
		s, m, err := e.step(run, i)
		if err != nil || m.Run == nil {
			return true, h.Fail("pipeline_module_unavailable")
		}
		out, err := m.Run(c, s)
		if err != nil {
			return true, err
		}
		switch out.kind {
		case kindYield:
			return true, nil
		case kindFail:
			return true, h.Fail(out.code)
		case kindHalt:
			s.Done = true
			run.Halted = true
			if err := h.Halt(s, out.value); err != nil {
				return true, err
			}
			if err := h.Save(); err != nil {
				return true, err
			}
			return false, nil
		case kindDone:
			s.Done = true
			if err := e.effect(h, s, m); err != nil {
				return true, err
			}
			if err := h.Save(); err != nil {
				return true, err
			}
		default:
			return true, h.Fail("pipeline_step_incomplete")
		}
	}
	return false, nil
}

// Traced is one simulated step.
type Traced struct {
	ID      string
	Module  string
	Outputs map[string]Value
}

// Simulate previews a run offline, step by step, until it ends or a step
// halts it. halt is the halting value, if any.
func (e *Engine[C]) Simulate(c C, run *Run) (trace []Traced, halt *Value, err error) {
	for i := 1; i < len(run.Steps); i++ {
		s, m, err := e.step(run, i)
		if err != nil {
			return trace, nil, err
		}
		if m.Simulate == nil {
			return trace, nil, errors.New("pipeline: module can't be previewed")
		}
		out, err := m.Simulate(c, s)
		if err != nil {
			return trace, nil, err
		}
		s.Done = true
		trace = append(trace, Traced{ID: s.ID, Module: s.Module, Outputs: s.Outputs})
		if out.kind == kindHalt {
			v := out.value
			return trace, &v, nil
		}
	}
	return trace, nil, nil
}
