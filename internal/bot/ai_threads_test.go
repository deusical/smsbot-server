package bot

import (
	"context"
	"errors"
	"github.com/deusical/smsbot-server/internal/store"
	"github.com/deusical/smsbot-server/internal/transport"
	"reflect"
	"strings"
	"testing"
	"time"
)

type transcriptProvider struct {
	history [][]store.Turn
	images  []*Attachment
}

func (p *transcriptProvider) Models(context.Context) ([]Model, error) {
	return SimulatedProvider{}.Models(context.Background())
}
func (p *transcriptProvider) Generate(_ context.Context, _ string, plan store.Plan, h []store.Turn, _ Model, image *Attachment) (Result, error) {
	p.images = append(p.images, image)
	p.history = append(p.history, append([]store.Turn{}, h...))
	zero := 0.0
	return Result{Text: "AI: " + plan.Input, Cost: &zero}, nil
}
func threadPrompt(id, text, wire string) transport.Incoming {
	m := incoming()
	m.ID = id
	m.Text = text
	m.Interaction = &transport.MessageInteraction{UpstreamID: id, Protocol: "RCS", ReplyTo: wire}
	return m
}
func threadAccept(t *testing.T, e *Engine, m transport.Incoming) store.Plan {
	t.Helper()
	ok, err := e.Accept(context.Background(), m)
	if err != nil || !ok {
		t.Fatalf("accept %s=%t %v", m.ID, ok, err)
	}
	_, p, err := e.Store.JobPlan(store.Hash(m.ConversationID + "\x00" + m.ID))
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func threadEcho(t *testing.T, e *Engine, m transport.Incoming, wire string) {
	t.Helper()
	id := store.Hash(m.ConversationID + "\x00" + m.ID)
	var txn string
	if err := e.Store.DB.QueryRow("SELECT txn FROM jobs WHERE id=?", id).Scan(&txn); err != nil || txn == "" {
		t.Fatal("no outbound transaction", err)
	}
	echo := transport.Incoming{ID: wire, ConversationID: m.ConversationID, ParticipantID: "self", Outgoing: true, Historical: true, TransactionID: txn, Status: "submitted", Text: "AI answer", At: time.Now()}
	if ok, err := e.Ingest(context.Background(), echo); err != nil || ok {
		t.Fatalf("echo routed: %t %v", ok, err)
	}
}
func TestAskFreshRootsNativeBranchesAndArchiveOff(t *testing.T) {
	e, _ := setup(t)
	arm(t, e)
	configureAI(t, e, true, false)
	provider := &transcriptProvider{}
	e.Provider = provider
	s, err := e.Collection.Settings()
	if err != nil || s.Enabled {
		t.Fatal("test must run without collection")
	}
	root := threadPrompt("root", "!ask First question", "")
	rp := threadAccept(t, e, root)
	if rp.Thread == nil || len(rp.Context) != 0 || rp.Thread.ReplyWire != "root" {
		t.Fatal("fresh root not planned")
	}
	if err = e.Dispatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	threadEcho(t, e, root, "answer-root")
	b := threadPrompt("b", "Make it shorter", "answer-root")
	bp := threadAccept(t, e, b)
	want := []store.Turn{{Role: "user", Content: "First question"}, {Role: "assistant", Content: "AI: First question"}}
	if !reflect.DeepEqual(bp.Context, want) || bp.Thread.Parent != store.Hash("demo-chat\x00root") {
		t.Fatalf("wrong parent context %#v", bp.Context)
	}
	if err = e.Dispatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	threadEcho(t, e, b, "answer-b")
	// A second reply to root remains a sibling even after B has completed.
	c := threadPrompt("c", "!ask Take another approach", "answer-root")
	cp := threadAccept(t, e, c)
	if !reflect.DeepEqual(cp.Context, want) || cp.Input != "Take another approach" {
		t.Fatal("sibling inherited later discussion")
	}
	if err = e.Dispatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	threadEcho(t, e, c, "answer-c")
	d := threadPrompt("d", "And then?", "answer-b")
	dp := threadAccept(t, e, d)
	if len(dp.Context) != 4 || dp.Context[2].Content != "Make it shorter" {
		t.Fatal("grandchild lost ancestry")
	}
	if err = e.Dispatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	fresh := threadPrompt("fresh", "!ask Unrelated", "")
	fp := threadAccept(t, e, fresh)
	if len(fp.Context) != 0 || fp.Thread.ID == rp.Thread.ID {
		t.Fatal("unquoted ask reused thread")
	}
	if err = e.Dispatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(provider.history) != 5 || len(provider.history[4]) != 0 {
		t.Fatal("provider got rolling context")
	}
	// First native prompt was saved independently of archive collection.
	var count int
	e.Store.DB.QueryRow("SELECT count(*) FROM ai_threads").Scan(&count)
	if count != 2 {
		t.Fatal("wrong root count")
	}
}
func TestAskThreadRestartAndModelSwitch(t *testing.T) {
	e, f := setup(t)
	arm(t, e)
	cfg := configureAI(t, e, true, false)
	e.Provider = &transcriptProvider{}
	root := threadPrompt("root", "!ask Remember this", "")
	threadAccept(t, e, root)
	if err := e.Dispatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	threadEcho(t, e, root, "answer")
	// Reassemble the application: thread state belongs to SQLite, not Engine memory.
	e = assemble(t, e.Store, f)
	models, _ := SimulatedProvider{}.Models(context.Background())
	m := models[0]
	m.ID = "fixture/second"
	models = append(models, m)
	if err := e.Store.SetPublicSetting("model_catalog", models); err != nil {
		t.Fatal(err)
	}
	cfg.Settings.DefaultModel = "fixture/second"
	if err := e.SetConfiguration(cfg); err != nil {
		t.Fatal(err)
	}
	p := threadAccept(t, e, threadPrompt("child", "Still there?", "answer"))
	if p.Model != "fixture/second" || len(p.Context) != 2 || p.Context[0].Content != "Remember this" {
		t.Fatal("model switch/restart lost ancestry")
	}
}
func TestAskThreadFollowupAccessAndUnsupportedTargets(t *testing.T) {
	for _, mode := range []string{"ordinary", "unknown", "sms", "historical", "stale", "wrong-peer", "wrong-chat", "paused", "memory-off", "owner-disabled"} {
		t.Run(mode, func(t *testing.T) {
			e, _ := setup(t)
			arm(t, e)
			cfg := configureAI(t, e, true, false)
			root := threadPrompt("root", "!ask First", "")
			threadAccept(t, e, root)
			if err := e.Dispatch(context.Background()); err != nil {
				t.Fatal(err)
			}
			threadEcho(t, e, root, "answer")
			m := threadPrompt("child", "Follow up", "answer")
			switch mode {
			case "ordinary":
				m.Interaction = nil
			case "unknown":
				m.Interaction.ReplyTo = "human-or-manual-message"
			case "sms":
				m.Interaction.Protocol = "SMS"
			case "historical":
				m.Historical = true
			case "stale":
				m.At = time.Now().Add(-3 * time.Minute)
			case "wrong-peer":
				m.ParticipantID = "other"
			case "wrong-chat":
				m.ConversationID = "other"
			case "paused":
				e.Pause(true)
			case "memory-off":
				cfg.Commands[0].Memory = false
				if err := e.SetConfiguration(cfg); err != nil {
					t.Fatal(err)
				}
			case "owner-disabled":
				m.Outgoing = true
			}
			if ok, err := e.Accept(context.Background(), m); err != nil || ok {
				t.Fatalf("unexpected routing %t %v", ok, err)
			}
		})
	}
}
func TestAskThreadExplicitCommandsResetAndNoResurrection(t *testing.T) {
	e, f := setup(t)
	arm(t, e)
	configureAI(t, e, true, false)
	root := threadPrompt("root", "!ask First", "")
	threadAccept(t, e, root)
	e.Dispatch(context.Background())
	threadEcho(t, e, root, "answer")
	// Explicit command wins even when quoting an AI answer.
	p := threadAccept(t, e, threadPrompt("ping", "!ping", "answer"))
	if p.Thread != nil || p.Reply != "pong" {
		t.Fatal("explicit command became followup")
	}
	e.Dispatch(context.Background())
	zero := 0.0
	provider := &controlledProvider{started: make(chan store.Plan, 1), release: make(chan struct{}), result: Result{Text: "stale answer", Cost: &zero}}
	e.Provider = provider
	child := threadPrompt("child", "Continue", "answer")
	threadAccept(t, e, child)
	done := make(chan error, 1)
	go func() { done <- e.Dispatch(context.Background()) }()
	<-provider.started
	sentBefore := len(f.Sent)
	threadAccept(t, e, threadPrompt("reset", "!new", "answer"))
	close(provider.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if len(f.Sent) != sentBefore {
		t.Fatal("reset generation sent stale answer")
	}
	if stateOf(t, e, store.Hash("demo-chat\x00child")) != "cancelled" {
		t.Fatal("reset did not cancel child")
	}
	if ok, err := e.Accept(context.Background(), threadPrompt("old", "Revive it", "answer")); err != nil || ok {
		t.Fatal("old answer resurrected context", err)
	}
}
func TestAskThreadUnknownSubmissionLateEchoAndDuplicate(t *testing.T) {
	e, f := setup(t)
	arm(t, e)
	configureAI(t, e, true, false)
	f.Fail = "unknown"
	root := threadPrompt("root", "!ask First", "")
	threadAccept(t, e, root)
	if err := e.Dispatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	id := store.Hash("demo-chat\x00root")
	if stateOf(t, e, id) != "unknown_submission" {
		t.Fatal("unknown outcome not retained")
	}
	if p, err := e.Store.FindAIParent("demo-chat", "answer"); err != nil || p != nil {
		t.Fatal("unproven answer indexed")
	}
	f.Fail = ""
	threadEcho(t, e, root, "answer")
	if stateOf(t, e, id) != "submitted" {
		t.Fatal("late echo not reconciled")
	}
	p := threadAccept(t, e, threadPrompt("child", "Next", "answer"))
	if len(p.Context) != 2 {
		t.Fatal("late proof did not enable context")
	}
	if ok, err := e.Accept(context.Background(), root); err != nil || ok {
		t.Fatal("duplicate root accepted", err)
	}
}
func TestAskThreadExpiryNoticeAndContextLimit(t *testing.T) {
	e, _ := setup(t)
	arm(t, e)
	configureAI(t, e, true, false)
	root := threadPrompt("root", "!ask First", "")
	threadAccept(t, e, root)
	e.Dispatch(context.Background())
	threadEcho(t, e, root, "answer")
	e.now = func() time.Time { return time.Now().Add(25 * time.Hour) }
	m := threadPrompt("expired", "Continue", "answer")
	m.At = e.now()
	p := threadAccept(t, e, m)
	if p.Thread != nil || p.Model != "" || p.Reply == "" {
		t.Fatal("expired context silently continued")
	}
	if _, _, err := e.Store.AIHistory(store.AIParent{Thread: "missing", Exchange: "missing"}, "demo-chat", "demo-peer", "!ask", 0, e.now(), 24, 7, 1); !errors.Is(err, store.ErrAIThreadUnavailable) {
		t.Fatal("missing thread accepted")
	}
}

// Unrelated chat must retain its cheap local ignore path even with threads on.
type countingThreadTransport struct {
	*transport.Fixture
	lookups int
}

func (t *countingThreadTransport) LookupConversation(ctx context.Context, id string) (transport.Conversation, error) {
	t.lookups++
	return transport.Lookup(ctx, t.Fixture, id)
}
func TestAskReplyClassificationDoesNotReadPhone(t *testing.T) {
	e, f := setup(t)
	arm(t, e)
	configureAI(t, e, true, false)
	root := threadPrompt("root", "!ask First", "")
	threadAccept(t, e, root)
	e.Dispatch(context.Background())
	threadEcho(t, e, root, "answer")
	counted := &countingThreadTransport{Fixture: f}
	e.Transport = counted
	for _, m := range []transport.Incoming{threadPrompt("ordinary", "hello", ""), threadPrompt("human-reply", "hello", "human-answer")} {
		if ok, err := e.Accept(context.Background(), m); err != nil || ok {
			t.Fatal("unrelated chat accepted", err)
		}
	}
	if counted.lookups != 0 {
		t.Fatal("classification read the phone")
	}
	threadAccept(t, e, threadPrompt("followup", "Next", "answer"))
	if counted.lookups != 1 {
		t.Fatal("valid followup skipped its live identity check")
	}
}
func TestAskOwnerFollowupsRequireCurrentPermissionAndAreDeduplicated(t *testing.T) {
	e, _ := setup(t)
	arm(t, e)
	cfg := configureAI(t, e, true, false)
	cfg.Settings.OwnerCommands = true
	if err := e.SetConfiguration(cfg); err != nil {
		t.Fatal(err)
	}
	root := threadPrompt("root", "!ask First", "")
	threadAccept(t, e, root)
	e.Dispatch(context.Background())
	threadEcho(t, e, root, "answer")
	owner := threadPrompt("owner-child", "Continue", "answer")
	owner.Outgoing = true
	owner.ParticipantID = "self"
	owner.Status = "submitted"
	owner.TransactionID = "owner-txn"
	if ok, err := e.Ingest(context.Background(), owner); err != nil || !ok {
		t.Fatalf("owner followup %t %v", ok, err)
	}
	if ok, err := e.Ingest(context.Background(), owner); err != nil || ok {
		t.Fatal("owner echo repeated prompt", err)
	}
	id := store.Hash("demo-chat\x00owner-child")
	_, p, err := e.Store.JobPlan(id)
	if err != nil || !p.Owner || len(p.Context) != 2 {
		t.Fatal("owner context invalid", err)
	}
	cfg.Settings.OwnerCommands = false
	if err = e.SetConfiguration(cfg); err != nil {
		t.Fatal(err)
	}
	if err = e.Dispatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if stateOf(t, e, id) != "cancelled" {
		t.Fatal("revoked owner permission submitted queued followup")
	}
}
func TestOtherAIMemoryAndStatelessAskRemainSeparate(t *testing.T) {
	e, _ := setup(t)
	arm(t, e)
	cfg := configureAI(t, e, true, false)
	cfg.Commands = append(cfg.Commands, Command{Name: "translate", Type: "ai", Enabled: true, Memory: true})
	if err := e.SetConfiguration(cfg); err != nil {
		t.Fatal(err)
	}
	scope := memoryScope("demo-chat", "!translate", "fixture/model", "")
	if err := e.Store.Remember(scope, []store.Turn{{Role: "user", Content: "existing translator context"}}, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	p := threadAccept(t, e, threadPrompt("translate", "!translate hello", ""))
	if p.Thread != nil || len(p.Context) != 1 {
		t.Fatal("other command memory changed")
	}
	cfg.Commands[0].Memory = false
	if err := e.SetConfiguration(cfg); err != nil {
		t.Fatal(err)
	}
	p = threadAccept(t, e, threadPrompt("ask", "!ask new", ""))
	if p.Thread != nil || p.Memory || len(p.Context) != 0 {
		t.Fatal("stateless ask got context")
	}
}
func TestAskConcurrentSiblingPromptsSnapshotSameParent(t *testing.T) {
	e, _ := setup(t)
	arm(t, e)
	configureAI(t, e, true, false)
	root := threadPrompt("root", "!ask First", "")
	threadAccept(t, e, root)
	e.Dispatch(context.Background())
	threadEcho(t, e, root, "answer")
	done := make(chan error, 2)
	for _, id := range []string{"b", "c"} {
		go func(id string) {
			ok, err := e.Accept(context.Background(), threadPrompt(id, "Followup "+id, "answer"))
			if err == nil && !ok {
				err = errors.New("not accepted")
			}
			done <- err
		}(id)
	}
	for range 2 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	_, b, err := e.Store.JobPlan(store.Hash("demo-chat\x00b"))
	if err != nil {
		t.Fatal(err)
	}
	_, c, err := e.Store.JobPlan(store.Hash("demo-chat\x00c"))
	if err != nil {
		t.Fatal(err)
	}
	if b.Thread.Parent != c.Thread.Parent || !reflect.DeepEqual(b.Context, c.Context) || len(b.Context) != 2 {
		t.Fatal("concurrent branches mixed context")
	}
}

func TestOwnerThreadFollowupMatchingAIAnswerUsesExactProvenance(t *testing.T) {
	for _, mode := range []string{"new-transaction", "missing-transaction", "bot-transaction", "bot-message-id"} {
		t.Run(mode, func(t *testing.T) {
			e, _ := setup(t)
			arm(t, e)
			cfg := configureAI(t, e, true, false)
			cfg.Settings.OwnerCommands = true
			if err := e.SetConfiguration(cfg); err != nil {
				t.Fatal(err)
			}
			e.Provider = &transcriptProvider{}
			root := threadPrompt("root", "!ask hello", "")
			threadAccept(t, e, root)
			if err := e.Dispatch(context.Background()); err != nil {
				t.Fatal(err)
			}
			threadEcho(t, e, root, "answer-root")
			m := threadPrompt("owner-followup", "AI: hello", "answer-root")
			m.Outgoing = true
			m.ParticipantID = "self"
			m.Status = "submitted"
			m.TransactionID = "new-owner-transaction"
			want := true
			switch mode {
			case "missing-transaction":
				m.TransactionID = ""
			case "bot-transaction":
				m.Text = "!ask changed bot echo"
				if err := e.Store.DB.QueryRow("SELECT txn FROM jobs WHERE id=?", store.Hash("demo-chat\x00root")).Scan(&m.TransactionID); err != nil {
					t.Fatal(err)
				}
				want = false
			case "bot-message-id":
				m.Text = "!ask changed bot echo"
				m.ID = "answer-root"
				m.TransactionID = ""
				want = false
			}
			m.Interaction.UpstreamID = m.ID
			accepted, err := e.Ingest(context.Background(), m)
			if err != nil || accepted != want {
				t.Fatalf("accepted=%t want=%t error=%v", accepted, want, err)
			}
			if want {
				_, p, err := e.Store.JobPlan(store.Hash("demo-chat\x00" + m.ID))
				if err != nil || !p.Owner || p.Thread == nil || len(p.Context) != 2 || p.Input != "AI: hello" {
					t.Fatal("followup lost its context", err)
				}
				if err = e.Dispatch(context.Background()); err != nil {
					t.Fatal(err)
				}
				if stateOf(t, e, store.Hash("demo-chat\x00"+m.ID)) != "submitted" {
					t.Fatal("genuine followup was not sent")
				}
			}
		})
	}
}
func TestAskReplyingToAnOrdinaryMessageIncludesIt(t *testing.T) {
	for _, memory := range []bool{true, false} {
		e, _ := setup(t)
		arm(t, e)
		configureAI(t, e, memory, false)
		at := time.Now()
		if err := e.Store.SaveObservedFiles("demo-chat", "human-wire", "", "incoming", "Synthetic plans for Saturday", "phone", "", at, nil); err != nil {
			t.Fatal(err)
		}
		if err := e.Store.SaveObservedFiles("other-chat", "elsewhere-wire", "", "incoming", "Synthetic other chat", "phone", "", at, nil); err != nil {
			t.Fatal(err)
		}
		p := threadAccept(t, e, threadPrompt("ask-quoted", "!ask what does this mean", "human-wire"))
		want := "This question replies to an earlier message in the chat, sent by the person asking:\n\"\"\"\nSynthetic plans for Saturday\n\"\"\"\n\nQuestion: what does this mean"
		if p.Input != want || p.Context != nil {
			t.Fatalf("memory=%t input=%q context=%v", memory, p.Input, p.Context)
		}
		if err := e.Store.SaveObservedFiles("demo-chat", "owner-wire", "", "outgoing", "Synthetic owner note", "phone", "", at, nil); err != nil {
			t.Fatal(err)
		}
		p = threadAccept(t, e, threadPrompt("ask-owner-quote", "!ask summarize", "owner-wire"))
		if !strings.Contains(p.Input, "sent by the other person in this chat:\n\"\"\"\nSynthetic owner note") {
			t.Fatalf("memory=%t input=%q", memory, p.Input)
		}
		// A bare !ask on a quoted message responds to it.
		p = threadAccept(t, e, threadPrompt("ask-bare-quote", "!ask", "human-wire"))
		if !strings.HasSuffix(p.Input, "Synthetic plans for Saturday\n\"\"\"\n\nNo question was given. Respond to that message.") || p.Model == "" || p.Reply != "" {
			t.Fatalf("memory=%t bare input=%q reply=%q", memory, p.Input, p.Reply)
		}
		p = threadAccept(t, e, threadPrompt("ask-bare-unknown", "!ask", "unknown-wire"))
		if p.Model != "" || !strings.HasPrefix(p.Reply, "Send !ask followed by a question") {
			t.Fatalf("memory=%t bare unknown input=%q reply=%q", memory, p.Input, p.Reply)
		}
		// Unknown targets and other conversations' messages add nothing.
		for i, wire := range []string{"unknown-wire", "elsewhere-wire"} {
			p = threadAccept(t, e, threadPrompt("ask-plain-"+string(rune('a'+i)), "!ask what does this mean", wire))
			if p.Input != "what does this mean" {
				t.Fatalf("memory=%t %s input=%q", memory, wire, p.Input)
			}
		}
	}
}
