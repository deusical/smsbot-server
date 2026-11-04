package pipeline

import (
	"encoding/json"
	"errors"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A toy host: requests carry a word; modules shout it, wait for a slow
// service, and print it.
type toyCtx struct {
	slowReady bool
	log       []string
}

type toyHost struct {
	effects []string
	halted  string
	saves   int
	failed  string
}

func (h *toyHost) Effect(s *Step) error {
	h.effects = append(h.effects, s.Text("text"))
	return nil
}
func (h *toyHost) Halt(_ *Step, v Value) error { h.halted = v.Text; return nil }
func (h *toyHost) Save() error                 { h.saves++; return nil }
func (h *toyHost) Fail(code string) error      { h.failed = code; return nil }

func text(s string) Value { return Value{Type: "text", Text: s} }

func toyEngine(policy Policy) *Engine[*toyCtx] {
	if policy.DefaultTrigger == "" {
		policy.DefaultTrigger = "word"
	}
	e := New[*toyCtx](policy)
	e.RegisterType(Type{ID: "text", Title: "Text"})
	e.RegisterType(Type{ID: "shout", Title: "Shouted text", Is: []string{"text"}})
	e.RegisterType(Type{ID: "number", Title: "Number"})
	e.RegisterTrigger(Trigger{ID: "word", Title: "A word", Outputs: []Port{{Name: "word", Title: "Word", Types: []string{"text"}}}})
	upper := func(_ *toyCtx, s *Step) (Outcome, error) {
		w := s.Text("in")
		if w == "" {
			return Halt(text("say something")), nil
		}
		s.Out("out", Value{Type: "shout", Text: strings.ToUpper(w) + "!"})
		return Done(), nil
	}
	e.Register(Module[*toyCtx]{
		Definition: Definition{ID: "shout", Version: 1, Title: "Shout", Effect: Pure, Immediate: true,
			Inputs: []Port{{Name: "in", Title: "In", Types: []string{"text"}, Required: true}}, Outputs: []Port{{Name: "out", Title: "Out", Types: []string{"shout"}}}},
		Accept: func(c *toyCtx, s *Step) (Outcome, error) {
			if _, ok := s.In("in"); !ok {
				return Pending(), nil
			}
			return upper(c, s)
		},
		Run: upper, Simulate: upper,
	})
	// slow waits for a service, yielding until it's ready, like a download.
	e.Register(Module[*toyCtx]{
		Definition: Definition{ID: "slow", Version: 2, Title: "Slow", Effect: Local, LongRunning: true,
			Inputs: []Port{{Name: "in", Title: "In", Types: []string{"text"}, Required: true}}, Outputs: []Port{{Name: "out", Title: "Out", Types: []string{"text"}}}},
		Settings: func(raw json.RawMessage, env any) (json.RawMessage, error) {
			v := struct {
				Suffix string `json:"suffix"`
			}{"."}
			if err := StrictSettings(raw, &v); err != nil {
				return nil, err
			}
			if env == "strict" && len(v.Suffix) > 1 {
				return nil, errors.New("suffix too long")
			}
			return json.Marshal(v)
		},
		Accept: func(*toyCtx, *Step) (Outcome, error) { return Pending(), nil },
		Run: func(c *toyCtx, s *Step) (Outcome, error) {
			if !c.slowReady {
				c.log = append(c.log, "waiting")
				return Yield(), nil
			}
			var set struct{ Suffix string }
			_ = s.Decode(&set)
			s.Out("out", text(s.Text("in")+set.Suffix))
			return Done(), nil
		},
		Simulate: func(_ *toyCtx, s *Step) (Outcome, error) { s.Out("out", text("(slow)")); return Done(), nil },
	})
	e.Register(Module[*toyCtx]{
		Definition: Definition{ID: "count", Version: 1, Title: "Count", Effect: Pure,
			Inputs: []Port{{Name: "in", Title: "In", Types: []string{"text"}, Required: true}}, Outputs: []Port{{Name: "n", Title: "N", Types: []string{"number"}}}},
		Accept: func(*toyCtx, *Step) (Outcome, error) { return Pending(), nil },
		Run:    func(*toyCtx, *Step) (Outcome, error) { return Fail("count_broken"), nil },
	})
	e.Register(Module[*toyCtx]{
		Definition: Definition{ID: "print", Version: 1, Title: "Print", Effect: Sink,
			Inputs: []Port{{Name: "text", Title: "Text", Types: []string{"text"}, Required: true}}},
		Accept: func(_ *toyCtx, s *Step) (Outcome, error) {
			if _, ok := s.In("text"); !ok {
				return Pending(), nil
			}
			return Done(), nil
		},
		Run:      func(*toyCtx, *Step) (Outcome, error) { return Done(), nil },
		Simulate: func(*toyCtx, *Step) (Outcome, error) { return Done(), nil },
	})
	return e
}

func step(id, module string, inputs map[string]string, settings string) StepConfig {
	s := StepConfig{ID: id, Module: module, Inputs: inputs}
	if settings != "" {
		s.Settings = json.RawMessage(settings)
	}
	return s
}

var shoutThenPrint = Pipeline{Steps: []StepConfig{
	step("loud", "shout", map[string]string{"in": "trigger.word"}, ""),
	step("say", "print", map[string]string{"text": "loud.out"}, ""),
}}

func TestValidateChecksStructureTypesAndPolicy(t *testing.T) {
	e := toyEngine(Policy{MaxSteps: 4, MaxEffects: 1, EffectLast: true, RequireEffect: true})
	ok := shoutThenPrint.Clone()
	if err := e.Validate(&ok, nil); err != nil {
		t.Fatal(err)
	}
	if ok.Steps[0].Version != 1 {
		t.Fatal("version not pinned")
	}
	cases := map[string]Pipeline{
		"empty":           {},
		"unknown trigger": {Trigger: "clock", Steps: shoutThenPrint.Steps},
		"unknown module":  {Steps: []StepConfig{step("x", "rm", nil, ""), step("say", "print", map[string]string{"text": "trigger.word"}, "")}},
		"later binding":   {Steps: []StepConfig{step("a", "shout", map[string]string{"in": "b.out"}, ""), step("b", "shout", map[string]string{"in": "trigger.word"}, ""), step("say", "print", map[string]string{"text": "a.out"}, "")}},
		"type mismatch":   {Steps: []StepConfig{step("n", "count", map[string]string{"in": "trigger.word"}, ""), step("say", "print", map[string]string{"text": "n.n"}, "")}},
		"missing input":   {Steps: []StepConfig{step("loud", "shout", nil, ""), step("say", "print", map[string]string{"text": "trigger.word"}, "")}},
		"effect not last": {Steps: []StepConfig{step("say", "print", map[string]string{"text": "trigger.word"}, ""), step("loud", "shout", map[string]string{"in": "trigger.word"}, "")}},
		"two effects":     {Steps: []StepConfig{step("a", "print", map[string]string{"text": "trigger.word"}, ""), step("b", "print", map[string]string{"text": "trigger.word"}, "")}},
		"bad setting":     {Steps: []StepConfig{step("s", "slow", map[string]string{"in": "trigger.word"}, `{"rm":1}`), step("say", "print", map[string]string{"text": "s.out"}, "")}},
		"old version":     {Steps: []StepConfig{{ID: "s", Module: "slow", Version: 1, Inputs: map[string]string{"in": "trigger.word"}}, step("say", "print", map[string]string{"text": "s.out"}, "")}},
		"too many":        {Steps: []StepConfig{step("a", "shout", map[string]string{"in": "trigger.word"}, ""), step("b", "shout", map[string]string{"in": "a.out"}, ""), step("c", "shout", map[string]string{"in": "b.out"}, ""), step("d", "shout", map[string]string{"in": "c.out"}, ""), step("say", "print", map[string]string{"text": "d.out"}, "")}},
		"reserved id":     {Steps: []StepConfig{step("trigger", "print", map[string]string{"text": "trigger.word"}, "")}},
	}
	for name, p := range cases {
		var perr *Error
		if err := e.Validate(&p, nil); err == nil || !errors.As(err, &perr) {
			t.Errorf("%s: accepted (%v)", name, err)
		}
	}
	// The host's env reaches module settings.
	p := Pipeline{Steps: []StepConfig{step("s", "slow", map[string]string{"in": "trigger.word"}, `{"suffix":"!!"}`), step("say", "print", map[string]string{"text": "s.out"}, "")}}
	if e.Validate(&p, "strict") == nil {
		t.Fatal("env ignored")
	}
}

func TestTypesCountAsTheirParents(t *testing.T) {
	e := toyEngine(Policy{})
	text := Port{Types: []string{"text"}}
	if !e.Accepts(text, "shout") || e.Accepts(Port{Types: []string{"shout"}}, "text") || e.Accepts(text, "number") {
		t.Fatal("type compatibility")
	}
}

func TestPolicyCanRelaxEffects(t *testing.T) {
	e := toyEngine(Policy{})
	p := Pipeline{Steps: []StepConfig{step("a", "print", map[string]string{"text": "trigger.word"}, ""), step("b", "print", map[string]string{"text": "trigger.word"}, "")}}
	if err := e.Validate(&p, nil); err != nil {
		t.Fatal("no policy should allow several effects anywhere", err)
	}
}

func TestPolicyCheckAndUpgrades(t *testing.T) {
	e := toyEngine(Policy{Check: func(p *Pipeline, info Info) error {
		if len(info.Consumers["trigger.word"]) > 1 {
			return Errorf("", "", "read the word once")
		}
		if !info.Immediate["loud"] {
			return Errorf("loud", "", "should be immediate")
		}
		return nil
	}})
	e.RegisterUpgrade(func(p *Pipeline) {
		for i := range p.Steps {
			if p.Steps[i].Module == "yell" {
				p.Steps[i].Module = "shout"
			}
		}
	})
	p := shoutThenPrint.Clone()
	p.Steps[0].Module = "yell"
	if err := e.Validate(&p, nil); err != nil || p.Steps[0].Module != "shout" {
		t.Fatal("upgrade not applied", err)
	}
	p.Steps[1].Inputs["text"] = "trigger.word"
	p.Steps = append([]StepConfig{step("echo", "shout", map[string]string{"in": "trigger.word"}, "")}, p.Steps...)
	p.Steps[1].ID = "loud"
	if err := e.Validate(&p, nil); err == nil {
		t.Fatal("check ignored")
	}
}

func TestRunsAcceptYieldResumeAndGateEffects(t *testing.T) {
	e := toyEngine(Policy{})
	p := Pipeline{Steps: []StepConfig{
		step("loud", "shout", map[string]string{"in": "trigger.word"}, ""),
		step("wait", "slow", map[string]string{"in": "loud.out"}, `{"suffix":"?"}`),
		step("say", "print", map[string]string{"text": "wait.out"}, ""),
	}}
	if err := e.Validate(&p, nil); err != nil {
		t.Fatal(err)
	}
	run := Snapshot(p, map[string]Value{"word": text("hi")})
	c, h := &toyCtx{}, &toyHost{}
	if got, err := e.Accept(c, run, h); got != Ready || err != nil {
		t.Fatal(got, err)
	}
	if !run.Steps[1].Done || run.Steps[2].Done {
		t.Fatal("immediate step not done at acceptance")
	}
	// The run survives a round trip through storage.
	raw, _ := json.Marshal(run)
	var stored Run
	if err := json.Unmarshal(raw, &stored); err != nil {
		t.Fatal(err)
	}
	if stopped, err := e.Resume(c, &stored, h); !stopped || err != nil || len(h.effects) != 0 {
		t.Fatal("did not yield", stopped, err)
	}
	c.slowReady = true
	if stopped, err := e.Resume(c, &stored, h); stopped || err != nil {
		t.Fatal(stopped, err)
	}
	if len(h.effects) != 1 || h.effects[0] != "HI!?" || h.saves != 2 {
		t.Fatalf("effects %v saves %d", h.effects, h.saves)
	}
	// An edit to the command doesn't touch the run.
	p.Steps[1].Settings = json.RawMessage(`{"suffix":"."}`)
	if string(stored.Steps[2].Settings) != `{"suffix":"?"}` {
		t.Fatal("snapshot changed")
	}
}

func TestDirectEffectsSkipTheHost(t *testing.T) {
	e := toyEngine(Policy{Direct: func(m string) bool { return m == "print" }})
	p := shoutThenPrint.Clone()
	_ = e.Validate(&p, nil)
	h := &toyHost{}
	run := Snapshot(p, map[string]Value{"word": text("hi")})
	if got, _ := e.Accept(&toyCtx{}, run, h); got != Ready || len(h.effects) != 0 {
		t.Fatal("direct effect went through the host")
	}
}

func TestHaltFailAndUnavailable(t *testing.T) {
	e := toyEngine(Policy{})
	p := shoutThenPrint.Clone()
	_ = e.Validate(&p, nil)
	h := &toyHost{}
	run := Snapshot(p, map[string]Value{"word": text("")})
	if got, _ := e.Accept(&toyCtx{}, run, h); got != Halted || h.halted != "say something" || !run.Halted || !run.Steps[1].Done {
		t.Fatal("halt at acceptance", got, h.halted)
	}
	if stopped, _ := e.Resume(&toyCtx{}, run, h); stopped || len(h.effects) != 0 {
		t.Fatal("halted run resumed")
	}
	q := Pipeline{Steps: []StepConfig{step("n", "count", map[string]string{"in": "trigger.word"}, ""), step("say", "print", map[string]string{"text": "trigger.word"}, "")}}
	_ = e.Validate(&q, nil)
	run = Snapshot(q, map[string]Value{"word": text("x")})
	h = &toyHost{}
	e.Accept(&toyCtx{}, run, h)
	if stopped, _ := e.Resume(&toyCtx{}, run, h); !stopped || h.failed != "count_broken" {
		t.Fatal("failure not reported", h.failed)
	}
	run.Steps[1].Version = 9
	run.Steps[1].Done = false
	h = &toyHost{}
	if stopped, _ := e.Resume(&toyCtx{}, run, h); !stopped || h.failed != "pipeline_module_unavailable" {
		t.Fatal("unregistered version ran", h.failed)
	}
}

func TestSimulateAndTake(t *testing.T) {
	e := toyEngine(Policy{})
	p := Pipeline{Steps: []StepConfig{step("wait", "slow", map[string]string{"in": "trigger.word"}, ""), step("say", "print", map[string]string{"text": "wait.out"}, "")}}
	_ = e.Validate(&p, nil)
	trace, halt, err := e.Simulate(&toyCtx{}, Snapshot(p, map[string]Value{"word": text("x")}))
	if err != nil || halt != nil || len(trace) != 2 || trace[0].Outputs["out"].Text != "(slow)" {
		t.Fatal(trace, halt, err)
	}
	run := &Run{Steps: []StepRun{
		{ID: "a", Done: true, Outputs: map[string]Value{"v": {Type: "blob", Data: json.RawMessage(`"big"`)}}},
		{ID: "b", Inputs: map[string]string{"in": "a.v"}},
	}}
	s := &Step{StepRun: &run.Steps[1], run: run}
	if v, ok := s.Take("in"); !ok || string(v.Data) != `"big"` || run.Steps[0].Outputs["v"].Data != nil {
		t.Fatal("take kept the data twice")
	}
}

// The engine stays a library: it imports only the standard library.
func TestImportsOnlyTheStandardLibrary(t *testing.T) {
	files, _ := filepath.Glob("*.go")
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), f, src, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range parsed.Imports {
			if path := strings.Trim(imp.Path.Value, `"`); strings.Contains(strings.SplitN(path, "/", 2)[0], ".") {
				t.Errorf("%s imports %s", f, path)
			}
		}
	}
}
