package bot

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/deusical/smsbot-server/internal/store"
	"sync/atomic"
	"testing"
	"time"
)

type controlledProvider struct {
	started  chan store.Plan
	release  chan struct{}
	calls    atomic.Int32
	result   Result
	err      error
	deadline time.Time
}

func (p *controlledProvider) Models(context.Context) ([]Model, error) {
	return SimulatedProvider{}.Models(context.Background())
}
func (p *controlledProvider) Generate(ctx context.Context, key string, plan store.Plan, history []store.Turn, m Model) (Result, error) {
	p.calls.Add(1)
	if deadline, ok := ctx.Deadline(); ok {
		p.deadline = deadline
	}
	if p.started != nil {
		p.started <- plan
	}
	if p.release != nil {
		select {
		case <-p.release:
		case <-ctx.Done():
			return Result{}, ctx.Err()
		}
	}
	return p.result, p.err
}
func configureAI(t *testing.T, e *Engine, memory, approval bool) Configuration {
	t.Helper()
	c := DefaultConfiguration()
	c.Settings.DefaultModel = "fixture/model"
	c.Commands = []Command{{Name: "ask", Type: "ai", Enabled: true, Instructions: "Synthetic instructions", Memory: memory, Approval: approval}}
	if err := e.SetConfiguration(c); err != nil {
		t.Fatal(err)
	}
	if err := e.SetProviderKey("SYNTHETIC_KEY"); err != nil {
		t.Fatal(err)
	}
	return c
}
func acceptAsk(t *testing.T, e *Engine) string {
	t.Helper()
	m := incoming()
	m.Text = "!ask Synthetic input"
	ok, err := e.Accept(context.Background(), m)
	if err != nil || !ok {
		t.Fatalf("accepted=%t error=%v", ok, err)
	}
	return store.Hash(m.ConversationID + "\x00" + m.ID)
}
func stateOf(t *testing.T, e *Engine, id string) string {
	t.Helper()
	var state string
	if err := e.Store.DB.QueryRow("SELECT state FROM jobs WHERE id=?", id).Scan(&state); err != nil {
		t.Fatal(err)
	}
	return state
}
func TestFixtureAIRequiresNoKeyAndIsOffline(t *testing.T) {
	e, f := setup(t)
	arm(t, e)
	c := DefaultConfiguration()
	c.Settings.DefaultModel = "fixture/model"
	c.Commands = []Command{{Name: "ask", Type: "ai", Enabled: true}}
	if err := e.SetConfiguration(c); err != nil {
		t.Fatal(err)
	}
	id := acceptAsk(t, e)
	if err := e.Dispatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if stateOf(t, e, id) != "submitted" || len(f.Sent) != 1 {
		t.Fatal("fixture AI did not submit synthetic answer")
	}
	if _, ok := e.Provider.(SimulatedProvider); !ok {
		t.Fatal("fixture constructed a network provider")
	}
}
func TestQueueSnapshotsGenerationAndContext(t *testing.T) {
	e, _ := setup(t)
	arm(t, e)
	c := configureAI(t, e, true, false)
	g := DefaultGeneration()
	g.Reasoning = "high"
	g.Tokens = 4096
	c.Commands[0].Generation = &g
	if err := e.SetConfiguration(c); err != nil {
		t.Fatal(err)
	}
	scope := memoryScope("demo-chat", "!ask", "fixture/model")
	if err := e.Store.Remember(scope, []store.Turn{{Role: "user", Content: "Synthetic prior input"}}, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	id := acceptAsk(t, e)
	c.Commands[0].Instructions = "Changed later"
	c.Commands[0].Generation = &Generation{Reasoning: "off", AnswerLength: "detailed", Tokens: 128}
	if err := e.SetConfiguration(c); err != nil {
		t.Fatal(err)
	}
	_, p, err := e.Store.JobPlan(id)
	if err != nil {
		t.Fatal(err)
	}
	var snap Generation
	json.Unmarshal(p.Generation, &snap)
	if snap.Reasoning != "high" || snap.Tokens != 4096 || p.Instructions != "Synthetic instructions" || len(p.Context) != 1 {
		t.Fatal("queued snapshot changed")
	}
}
func TestPauseCancelsGenerationWithoutWaiting(t *testing.T) {
	e, f := setup(t)
	arm(t, e)
	configureAI(t, e, false, false)
	p := &controlledProvider{started: make(chan store.Plan, 1), release: make(chan struct{})}
	e.Provider = p
	id := acceptAsk(t, e)
	done := make(chan error, 1)
	go func() { done <- e.Dispatch(context.Background()) }()
	select {
	case <-p.started:
	case <-time.After(3 * time.Second):
		t.Fatal("generation did not start")
	}
	pauseDone := make(chan error, 1)
	go func() { pauseDone <- e.Pause(true) }()
	select {
	case err := <-pauseDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("pause blocked behind provider")
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("generation did not cancel")
	}
	if stateOf(t, e, id) != "cancelled" || len(f.Sent) != 0 {
		t.Fatal("cancelled generation was sent")
	}
}
func TestMemoryResetDuringGenerationCannotRestoreContext(t *testing.T) {
	e, _ := setup(t)
	arm(t, e)
	configureAI(t, e, true, false)
	zero := 0.0
	p := &controlledProvider{started: make(chan store.Plan, 1), release: make(chan struct{}), result: Result{Text: "Synthetic answer", Cost: &zero}}
	e.Provider = p
	scope := memoryScope("demo-chat", "!ask", "fixture/model")
	e.Store.Remember(scope, []store.Turn{{Role: "user", Content: "Synthetic old context"}}, time.Now().Add(time.Hour))
	acceptAsk(t, e)
	done := make(chan error, 1)
	go func() { done <- e.Dispatch(context.Background()) }()
	<-p.started
	m := incoming()
	m.ID = "synthetic-reset"
	m.Text = "!new"
	if ok, err := e.Accept(context.Background(), m); err != nil || !ok {
		t.Fatalf("reset failed %v", err)
	}
	close(p.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	turns, err := e.Store.Memory(scope, time.Now())
	if err != nil || len(turns) != 0 {
		t.Fatal("reset memory was restored by in-flight generation")
	}
}
func TestNewApprovalAfterGenerationDoesNotRegenerate(t *testing.T) {
	e, f := setup(t)
	arm(t, e)
	c := configureAI(t, e, false, false)
	zero := 0.0
	p := &controlledProvider{started: make(chan store.Plan, 1), release: make(chan struct{}), result: Result{Text: "Synthetic answer", Cost: &zero}}
	e.Provider = p
	id := acceptAsk(t, e)
	done := make(chan error, 1)
	go func() { done <- e.Dispatch(context.Background()) }()
	<-p.started
	c.Commands[0].Approval = true
	if err := e.SetConfiguration(c); err != nil {
		t.Fatal(err)
	}
	close(p.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if stateOf(t, e, id) != "awaiting_approval" || len(f.Sent) != 0 {
		t.Fatal("new approval bypassed")
	}
	if err := e.Approve(id); err != nil {
		t.Fatal(err)
	}
	if err := e.Dispatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if p.calls.Load() != 1 || stateOf(t, e, id) != "submitted" || len(f.Sent) != 1 {
		t.Fatal("approved saved answer regenerated or not sent")
	}
}
func TestUnknownCostBlocksNextAIRequest(t *testing.T) {
	e, f := setup(t)
	arm(t, e)
	configureAI(t, e, false, false)
	p := &controlledProvider{result: Result{Text: "Synthetic answer"}}
	e.Provider = p
	acceptAsk(t, e)
	if err := e.Dispatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	m := incoming()
	m.ID = "synthetic-second"
	m.Text = "!ask Next synthetic input"
	if ok, err := e.Accept(context.Background(), m); err != nil || !ok {
		t.Fatal(err)
	}
	if err := e.Dispatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if p.calls.Load() != 1 || len(f.Sent) != 1 {
		t.Fatal("unknown-cost budget was bypassed")
	}
}
func TestApprovalExpiryAndRecipePreview(t *testing.T) {
	e, f := setup(t)
	arm(t, e)
	configureAI(t, e, false, true)
	id := acceptAsk(t, e)
	if err := e.Dispatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(f.Sent) != 0 {
		t.Fatal("approval bypassed")
	}
	e.now = func() time.Time { return time.Now().Add(3 * time.Minute) }
	if e.Approve(id) == nil || stateOf(t, e, id) != "expired" {
		t.Fatal("expired approval accepted")
	}
	c := DefaultConfiguration()
	c.Commands = []Command{{Name: "sample", Type: "fixed", Instructions: "Hello {input}", Enabled: false}}
	if err := e.ImportRecipe(c); err != nil {
		t.Fatal(err)
	}
	result, err := e.Playground("sample", "synthetic")
	if err != nil || result["text"] != "Hello synthetic" {
		t.Fatal("disabled preview failed")
	}
	saved, _ := e.Configuration()
	if saved.Commands[0].Enabled {
		t.Fatal("preview enabled saved command")
	}
}
func TestConsentRemovalInvalidatesQueuedContext(t *testing.T) {
	e, _ := setup(t)
	arm(t, e)
	c := configureAI(t, e, true, false)
	scope := memoryScope("demo-chat", "!ask", "fixture/model")
	e.Store.Remember(scope, []store.Turn{{Role: "user", Content: "Synthetic context"}}, time.Now().Add(time.Hour))
	id := acceptAsk(t, e)
	c.Commands[0].Memory = false
	e.SetConfiguration(c)
	_, p, _ := e.Store.JobPlan(id)
	epoch, _ := e.Store.MemoryEpoch("demo-chat")
	if epoch == p.MemoryEpoch {
		t.Fatal("memory revocation did not invalidate plan")
	}
	turns, _ := e.Store.Memory(scope, time.Now())
	if len(turns) > 0 {
		t.Fatal("revoked context persisted")
	}
}
func TestDispatchBoundsProviderCallContext(t *testing.T) {
	e, _ := setup(t)
	arm(t, e)
	zero := 0.0
	provider := &controlledProvider{result: Result{Text: "Synthetic answer", Cost: &zero}}
	e.Provider = provider
	configureAI(t, e, false, false)
	acceptAsk(t, e)
	started := time.Now()
	if err := e.Dispatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if provider.deadline.IsZero() || provider.deadline.Before(started.Add(90*time.Second)) || provider.deadline.After(started.Add(96*time.Second)) {
		t.Fatalf("provider call deadline was not bounded near 95 seconds: %v", provider.deadline)
	}
}

func TestProviderFailureNeverSubmits(t *testing.T) {
	e, f := setup(t)
	arm(t, e)
	configureAI(t, e, false, false)
	e.Provider = &controlledProvider{err: errors.New("unexpected synthetic detail")}
	id := acceptAsk(t, e)
	if err := e.Dispatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	jobs, _ := e.Store.Jobs()
	if stateOf(t, e, id) != "failed" || jobs[0].Code != "provider_failed" || len(f.Sent) != 0 {
		t.Fatal("provider error leaked or sent")
	}
}
