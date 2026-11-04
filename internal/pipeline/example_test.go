package pipeline_test

import (
	"fmt"
	"strings"

	"github.com/deusical/smsbot-server/internal/pipeline"
)

// A host for a tiny greeting bot: one trigger, a pure module, and a "say"
// effect that only happens through the host.
type greetCtx struct{}

type greetHost struct{ said []string }

func (h *greetHost) Effect(s *pipeline.Step) error {
	h.said = append(h.said, s.Text("text"))
	return nil
}
func (h *greetHost) Halt(*pipeline.Step, pipeline.Value) error { return nil }
func (h *greetHost) Save() error                               { return nil }
func (h *greetHost) Fail(string) error                         { return nil }

func Example() {
	e := pipeline.New[greetCtx](pipeline.Policy{DefaultTrigger: "hello", RequireEffect: true, EffectLast: true})
	e.RegisterType(pipeline.Type{ID: "text", Title: "Text"})
	e.RegisterTrigger(pipeline.Trigger{ID: "hello", Title: "Someone says hello",
		Outputs: []pipeline.Port{{Name: "name", Title: "Their name", Types: []string{"text"}}}})
	e.Register(pipeline.Module[greetCtx]{
		Definition: pipeline.Definition{ID: "greet", Version: 1, Title: "Greet", Effect: pipeline.Pure, Immediate: true,
			Inputs:  []pipeline.Port{{Name: "name", Title: "Name", Types: []string{"text"}, Required: true}},
			Outputs: []pipeline.Port{{Name: "text", Title: "Greeting", Types: []string{"text"}}}},
		Accept: func(_ greetCtx, s *pipeline.Step) (pipeline.Outcome, error) {
			s.Out("text", pipeline.Value{Type: "text", Text: "Hi, " + strings.TrimSpace(s.Text("name")) + "!"})
			return pipeline.Done(), nil
		},
	})
	e.Register(pipeline.Module[greetCtx]{
		Definition: pipeline.Definition{ID: "say", Version: 1, Title: "Say", Effect: pipeline.Sink,
			Inputs: []pipeline.Port{{Name: "text", Title: "Text", Types: []string{"text"}, Required: true}}},
		Accept: func(greetCtx, *pipeline.Step) (pipeline.Outcome, error) { return pipeline.Done(), nil },
	})

	// The recipe is configuration, checked once when it's saved.
	recipe := pipeline.Pipeline{Steps: []pipeline.StepConfig{
		{ID: "greeting", Module: "greet", Inputs: map[string]string{"name": "trigger.name"}},
		{ID: "out", Module: "say", Inputs: map[string]string{"text": "greeting.text"}},
	}}
	if err := e.Validate(&recipe, nil); err != nil {
		panic(err)
	}

	// Each event snapshots the recipe; the effect reaches only the host.
	run := pipeline.Snapshot(recipe, map[string]pipeline.Value{"name": {Type: "text", Text: "Ada"}})
	host := &greetHost{}
	accepted, _ := e.Accept(greetCtx{}, run, host)
	fmt.Println(accepted == pipeline.Ready, host.said)
	// Output: true [Hi, Ada!]
}
