package bot

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/deusical/smsbot-server/internal/store"
)

// pricedProvider charges a fixed cost per request and records what each saw.
type pricedProvider struct {
	mu        sync.Mutex
	inputs    []string
	histories [][]store.Turn
}

func (p *pricedProvider) Models(context.Context) ([]Model, error) {
	return SimulatedProvider{}.Models(context.Background())
}
func (p *pricedProvider) Generate(_ context.Context, _ string, plan store.Plan, h []store.Turn, _ Model, _ *Attachment) (Result, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.inputs = append(p.inputs, plan.Input)
	p.histories = append(p.histories, append([]store.Turn{}, h...))
	cost := 0.01
	return Result{Text: "AI(" + plan.Input + ")", Cost: &cost}, nil
}

// summarize, then translate the summary: two AI requests in one command.
func chainCommand(memory bool) Command {
	steps := []PipelineStep{
		step("summary", "ai.chat", map[string]string{"prompt": "trigger.args"}, `{"instructions":"Summarize."}`),
		step("ask", "text.template", map[string]string{"input": "summary.text"}, `{"template":"Translate: {input}"}`),
	}
	if memory {
		steps = append(steps,
			step("conversation", "context.rolling", nil, ""),
			step("translation", "ai.chat", map[string]string{"prompt": "ask.text", "context": "conversation.context"}, `{"instructions":"Translate."}`),
			reply(map[string]string{"text": "translation.text", "continues": "translation.context"}))
	} else {
		steps = append(steps,
			step("translation", "ai.chat", map[string]string{"prompt": "ask.text"}, `{"instructions":"Translate."}`),
			reply(map[string]string{"text": "translation.text"}))
	}
	return customCommand("chain", Pipeline{Steps: steps})
}

func runChain(t *testing.T, e *Engine, id, args string) string {
	t.Helper()
	m := incoming()
	m.ID, m.Text = id, "!chain "+args
	if ok, err := e.Accept(context.Background(), m); !ok || err != nil {
		t.Fatal(ok, err)
	}
	job := store.Hash(m.ConversationID + "\x00" + m.ID)
	for i := 0; i < 4 && stateOf(t, e, job) == "queued"; i++ {
		if err := e.Dispatch(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	return job
}

func TestSeveralAIStepsRunInOrderAndEachIsAccounted(t *testing.T) {
	e, fixture := setup(t)
	arm(t, e)
	c := configureAI(t, e, false, false)
	c.Commands = append(c.Commands, chainCommand(false))
	if err := e.SetConfiguration(c); err != nil {
		t.Fatal(err)
	}
	provider := &pricedProvider{}
	e.Provider = provider
	m := incoming()
	m.ID, m.Text = "chain-1", "!chain long text"
	if ok, err := e.Accept(context.Background(), m); !ok || err != nil {
		t.Fatal(ok, err)
	}
	job := store.Hash(m.ConversationID + "\x00" + m.ID)
	_, p, _ := e.Store.JobPlan(job)
	deadline, err := time.Parse(time.RFC3339Nano, p.ExecutionDeadline)
	if err != nil || time.Until(deadline) < 14*time.Minute {
		t.Fatal("several AI requests need the execution deadline, not two minutes")
	}
	if err := e.Dispatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := stateOf(t, e, job); got != "submitted" {
		t.Fatal("state", got)
	}
	if len(provider.inputs) != 2 || provider.inputs[0] != "long text" || provider.inputs[1] != "Translate: AI(long text)" {
		t.Fatalf("requests %q", provider.inputs)
	}
	for _, text := range fixture.Sent {
		if text != "AI(Translate: AI(long text))" {
			t.Fatalf("sent %q", text)
		}
	}
	var steps int
	if err := e.Store.DB.QueryRow("SELECT count(*) FROM usage_steps WHERE job=? AND cost=0.01", job).Scan(&steps); err != nil || steps != 2 {
		t.Fatal("each request not kept", steps, err)
	}
	jobs, _ := e.Store.Jobs()
	for _, j := range jobs {
		if j.ID == job && (j.Cost == nil || *j.Cost != 0.02 || j.Model != "fixture/model") {
			t.Fatalf("job total %+v", j)
		}
	}
}

// The daily budget is checked before every request, including the requests
// earlier steps of the same job already made.
func TestTheBudgetStopsALaterAIStep(t *testing.T) {
	e, fixture := setup(t)
	arm(t, e)
	c := configureAI(t, e, false, false)
	c.Settings.DailyBudget = 0.005
	c.Commands = append(c.Commands, chainCommand(false))
	if err := e.SetConfiguration(c); err != nil {
		t.Fatal(err)
	}
	provider := &pricedProvider{}
	e.Provider = provider
	job := runChain(t, e, "chain-budget", "text")
	var code string
	e.Store.DB.QueryRow("SELECT code FROM jobs WHERE id=?", job).Scan(&code)
	if stateOf(t, e, job) != "failed" || code != "budget_exceeded_or_unknown" || len(provider.inputs) != 1 || len(fixture.Sent) != 0 {
		t.Fatal("second request ran over budget", code, len(provider.inputs))
	}
}

// With a conversation on the second AI step, only that step reads and
// records the conversation; the first stays a plain request.
func TestOnlyTheConversationStepIsTheTurn(t *testing.T) {
	e, _ := setup(t)
	arm(t, e)
	c := configureAI(t, e, false, false)
	c.Commands = append(c.Commands, chainCommand(true))
	if err := e.SetConfiguration(c); err != nil {
		t.Fatal(err)
	}
	provider := &pricedProvider{}
	e.Provider = provider
	m := incoming()
	m.ID, m.Text = "turn-1", "!chain first"
	if ok, err := e.Accept(context.Background(), m); !ok || err != nil {
		t.Fatal(ok, err)
	}
	_, p, _ := e.Store.JobPlan(store.Hash(m.ConversationID + "\x00turn-1"))
	if !p.Memory || p.Input != "" {
		// The turn's prompt comes from the first AI answer, so it is known only
		// when the second step runs.
		t.Fatalf("plan turn fields %+v", p)
	}
	if err := e.Dispatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	runChain(t, e, "turn-2", "second")
	if len(provider.histories) != 4 || len(provider.histories[0]) != 0 || len(provider.histories[2]) != 0 {
		t.Fatal("the plain step read the conversation")
	}
	h := provider.histories[3]
	if len(h) != 2 || h[0].Content != "Translate: AI(first)" || !strings.HasPrefix(h[1].Content, "AI(Translate") {
		t.Fatalf("conversation turn %+v", h)
	}
}
