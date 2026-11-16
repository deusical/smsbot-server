package bot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/deusical/smsbot-server/internal/store"
)

func TestAskDefaultToolsAndShortAliasRunPipelineCommands(t *testing.T) {
	t.Parallel()
	var calls int
	var seenToolNames []string
	var toolOutputs []string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []struct {
				Role    string `json:"role"`
				Content any    `json:"content"`
			} `json:"messages"`
			Tools []ToolSpec `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		calls++
		w.Header().Set("Content-Type", "application/json")
		switch calls {
		case 1:
			for _, ts := range body.Tools {
				seenToolNames = append(seenToolNames, ts.Function.Name)
			}
			w.Write([]byte(`{"model":"synthetic/agent","choices":[{"finish_reason":"tool_calls","message":{"content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"hello","arguments":"{\"input\":\"Alice\"}"}},{"id":"call_2","type":"function","function":{"name":"translate","arguments":"{\"input\":\"Hello Alice\"}"}}]}}],"usage":{"prompt_tokens":20,"completion_tokens":10,"cost":0.001}}`))
		case 2:
			// Specialist translate AI step (must run without tools to prevent recursion).
			if len(body.Tools) != 0 {
				t.Errorf("sub-command AI call received %d tools, want 0", len(body.Tools))
			}
			w.Write([]byte(`{"model":"synthetic/agent","choices":[{"finish_reason":"stop","message":{"content":"Bonjour Alice"}}],"usage":{"prompt_tokens":12,"completion_tokens":4,"cost":0.0005}}`))
		case 3:
			for _, m := range body.Messages {
				if m.Role == "tool" {
					if s, ok := m.Content.(string); ok {
						toolOutputs = append(toolOutputs, s)
					}
				}
			}
			w.Write([]byte(`{"model":"synthetic/agent","choices":[{"finish_reason":"stop","message":{"content":"Final: Bonjour Alice!"}}],"usage":{"prompt_tokens":30,"completion_tokens":8,"cost":0.002}}`))
		default:
			t.Fatalf("unexpected call %d", calls)
		}
	}))
	defer server.Close()

	e, fixture := setup(t)
	e.Provider = NewOpenRouter()
	arm(t, e)
	if err := e.SetCompatibleProvider(server.URL+"/v1", "SYNTHETIC_KEY", false); err != nil {
		t.Fatal(err)
	}
	model := Model{
		ID:               "compatible:synthetic/agent",
		Name:             "Synthetic agent",
		Provider:         "compatible",
		RemoteID:         "synthetic/agent",
		PricingKnown:     true,
		InputPrice:       0.000001,
		OutputPrice:      0.000001,
		Parameters:       []string{"tools"},
		ReasoningEfforts: []string{},
	}
	if err := e.Store.SetPublicSetting("model_catalog", []Model{model}); err != nil {
		t.Fatal(err)
	}

	c := DefaultConfiguration()
	c.Settings.DefaultModel = model.ID
	c.Commands = []Command{
		// Pre-existing saved v1 ask pipeline upgrades to v2 with tools: true.
		customCommand("ask", Pipeline{Steps: []PipelineStep{
			step("question", "message.question", map[string]string{"text": "trigger.args", "message": "trigger.message"}, `{"require_question":true}`),
			step("conversation", "context.thread", nil, ""),
			{ID: "answer", Module: "ai.chat", Version: 1, Inputs: map[string]string{"prompt": "question.prompt", "file": "question.file", "context": "conversation.context"}, Settings: json.RawMessage(`{"instructions":"Assist"}`)},
			reply(map[string]string{"text": "answer.text", "continues": "answer.context"}),
		}}),
		// Dashboard-style pipeline quick reply.
		customCommand("hello", Pipeline{Steps: []PipelineStep{
			step("text", "text.template", map[string]string{"input": "trigger.args"}, `{"template":"Welcome, {input}!"}`),
			reply(map[string]string{"text": "text.text"}),
		}}),
		// Specialist AI command.
		customCommand("translate", Pipeline{Steps: []PipelineStep{
			step("question", "message.question", map[string]string{"text": "trigger.args", "message": "trigger.message"}, ""),
			step("answer", "ai.chat", map[string]string{"prompt": "question.prompt"}, `{"instructions":"Translate to French"}`),
			reply(map[string]string{"text": "answer.text"}),
		}}),
		// Restricted to a different conversation -> must not be exposed as a tool here.
		{
			Name:                 "secret",
			Kind:                 "fixed.reply",
			Enabled:              true,
			Instructions:         "Top secret",
			AllowedConversations: []string{"other-chat"},
		},
	}
	if err := e.SetConfiguration(c); err != nil {
		t.Fatal(err)
	}

	// Send via the short !a alias.
	m := incoming()
	m.Text = "!a greet Alice in French"
	if ok, err := e.Accept(context.Background(), m); !ok || err != nil {
		t.Fatalf("accept !a: ok=%t err=%v", ok, err)
	}

	// Mutate hello after acceptance to prove queued plan uses the acceptance snapshot.
	loaded, err := e.Configuration()
	if err != nil {
		t.Fatal(err)
	}
	loaded.Commands[1] = customCommand("hello", Pipeline{Steps: []PipelineStep{
		step("text", "text.template", map[string]string{"input": "trigger.args"}, `{"template":"MUTATED {input}"}`),
		reply(map[string]string{"text": "text.text"}),
	}})
	if err := e.SetConfiguration(loaded); err != nil {
		t.Fatal(err)
	}

	if err := e.Dispatch(context.Background()); err != nil {
		t.Fatal(err)
	}

	if !reflect.DeepEqual(seenToolNames, []string{"hello", "translate"}) {
		t.Fatalf("seen tools = %v, want [hello translate]", seenToolNames)
	}
	if !reflect.DeepEqual(toolOutputs, []string{"Welcome, Alice!", "Bonjour Alice"}) {
		t.Fatalf("tool outputs = %v", toolOutputs)
	}
	if len(fixture.Sent) != 1 || !sentContains(fixture, "Final: Bonjour Alice!") {
		t.Fatalf("sent = %v", fixture.Sent)
	}
	jobs, err := e.Store.Jobs()
	if err != nil || len(jobs) != 1 {
		t.Fatalf("jobs err=%v len=%d", err, len(jobs))
	}
	if !reflect.DeepEqual(jobs[0].UsedTools, []string{"!hello", "!translate"}) {
		t.Fatalf("used_tools = %v", jobs[0].UsedTools)
	}
	if jobs[0].Cost == nil || *jobs[0].Cost < 0.0034 || *jobs[0].Cost > 0.0036 {
		t.Fatalf("unexpected total cost = %v", jobs[0].Cost)
	}
}

func TestAskVideoDownloadToolAttachesVideoAndPropagatesApproval(t *testing.T) {
	t.Parallel()
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		switch calls {
		case 1:
			w.Write([]byte(`{"model":"synthetic/agent","choices":[{"finish_reason":"tool_calls","message":{"content":null,"tool_calls":[{"id":"vid_1","type":"function","function":{"name":"grab","arguments":"{\"url\":\"https://v.douyin.com/synthetic/\"}"}}]}}],"usage":{"prompt_tokens":15,"completion_tokens":8,"cost":0.001}}`))
		case 2:
			var body struct {
				Messages []struct {
					Role    string `json:"role"`
					Content any    `json:"content"`
				} `json:"messages"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			foundAttach := false
			for _, m := range body.Messages {
				if s, ok := m.Content.(string); m.Role == "tool" && ok && strings.Contains(s, "Attached file video.mp4") {
					foundAttach = true
				}
			}
			if !foundAttach {
				t.Errorf("expected tool output confirming attached video.mp4")
			}
			w.Write([]byte(`{"model":"synthetic/agent","choices":[{"finish_reason":"stop","message":{"content":"Downloaded your video."}}],"usage":{"prompt_tokens":25,"completion_tokens":6,"cost":0.001}}`))
		default:
			t.Fatalf("unexpected call %d", calls)
		}
	}))
	defer server.Close()

	e, fixture := setup(t)
	e.Provider = NewOpenRouter()
	arm(t, e)
	if err := e.SetCompatibleProvider(server.URL+"/v1", "SYNTHETIC_KEY", false); err != nil {
		t.Fatal(err)
	}
	model := Model{
		ID:               "compatible:synthetic/agent",
		Name:             "Synthetic agent",
		Provider:         "compatible",
		RemoteID:         "synthetic/agent",
		PricingKnown:     true,
		InputPrice:       0.000001,
		OutputPrice:      0.000001,
		Parameters:       []string{"tools"},
		ReasoningEfforts: []string{},
	}
	if err := e.Store.SetPublicSetting("model_catalog", []Model{model}); err != nil {
		t.Fatal(err)
	}

	grab := customCommand("grab", Pipeline{ExecutionMinutes: 10, Steps: []PipelineStep{
		step("link", "text.extract_url", map[string]string{"text": "trigger.args"}, ""),
		step("source", "video.download", map[string]string{"url": "link.url"}, ""),
		step("video", "video.compress", map[string]string{"source": "source.source"}, `{"preset":"fast"}`),
		reply(map[string]string{"file": "video.video"}),
	}})
	grab.Approval = true

	c := DefaultConfiguration()
	c.Settings.DefaultModel = model.ID
	c.Commands = []Command{
		{Name: "ask", Type: "ai", Enabled: true, Memory: true, Instructions: "Assist", Approval: false},
		grab,
	}
	if err := e.SetConfiguration(c); err != nil {
		t.Fatal(err)
	}
	tasks := &multiHoldTasks{holdsRemaining: 2}
	e.Tasks = tasks

	m := incoming()
	m.Text = "!ask please download https://v.douyin.com/synthetic/"
	if ok, err := e.Accept(context.Background(), m); !ok || err != nil {
		t.Fatal(ok, err)
	}
	id := store.Hash(m.ConversationID + "\x00" + m.ID)

	// Poll across yielded worker holds until the agent finishes and waits for approval.
	for i := 0; i < 10 && stateOf(t, e, id) == "queued"; i++ {
		if _, err := e.Store.DB.Exec("UPDATE job_work SET next_poll=0"); err != nil {
			t.Fatal(err)
		}
		if err := e.Dispatch(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if got := stateOf(t, e, id); got != "awaiting_approval" {
		t.Fatalf("state = %s, want awaiting_approval (propagated from grab tool)", got)
	}
	if len(fixture.Sent) != 0 {
		t.Fatal("sent before owner approval")
	}

	if err := e.Approve(id); err != nil {
		t.Fatal(err)
	}
	if err := e.Dispatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	finishDownloadUploads(t, e)
	if err := e.Dispatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := stateOf(t, e, id); got != "submitted" {
		t.Fatalf("final state = %s, want submitted", got)
	}
	if calls != 2 || len(fixture.Sent) != 1 || !sentContains(fixture, "Downloaded your video.") {
		t.Fatalf("calls=%d sent=%v", calls, fixture.Sent)
	}
	page, err := e.Store.OwnerMessagePage(m.ConversationID, "", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	foundAttachment := false
	for _, msg := range page.Messages {
		if msg.Direction == "outgoing" && len(msg.Attachments) == 1 && msg.Attachments[0].Name == "video.mp4" {
			foundAttachment = true
		}
	}
	if !foundAttachment {
		t.Fatalf("outgoing message did not carry video.mp4 attachment: %+v", page.Messages)
	}
}

func TestAskUnknownCostAndUnsupportedToolsFallback(t *testing.T) {
	t.Parallel()
	t.Run("unknown_cost_in_later_round_marks_total_unknown", func(t *testing.T) {
		var calls int
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			calls++
			w.Header().Set("Content-Type", "application/json")
			if calls == 1 {
				w.Write([]byte(`{"model":"synthetic/agent","choices":[{"finish_reason":"tool_calls","message":{"content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"hello","arguments":"{\"input\":\"x\"}"}}]}}],"usage":{"prompt_tokens":10,"completion_tokens":5,"cost":0.001}}`))
				return
			}
			// Round 2 omits cost.
			w.Write([]byte(`{"model":"synthetic/agent","choices":[{"finish_reason":"stop","message":{"content":"Done"}}],"usage":{"prompt_tokens":10,"completion_tokens":5}}`))
		}))
		defer server.Close()

		e, _ := setup(t)
		e.Provider = NewOpenRouter()
		arm(t, e)
		_ = e.SetCompatibleProvider(server.URL+"/v1", "SYNTHETIC_KEY", false)
		model := Model{ID: "compatible:synthetic/agent", Name: "Agent", Provider: "compatible", RemoteID: "synthetic/agent", PricingKnown: true, Parameters: []string{"tools"}, ReasoningEfforts: []string{}}
		_ = e.Store.SetPublicSetting("model_catalog", []Model{model})

		c := DefaultConfiguration()
		c.Settings.DefaultModel = model.ID
		c.Settings.DailyBudget = 0
		c.Commands = []Command{
			{Name: "ask", Type: "ai", Enabled: true, Instructions: "Assist"},
			{Name: "hello", Type: "fixed", Enabled: true, Instructions: "Hi {input}"},
		}
		if err := e.SetConfiguration(c); err != nil {
			t.Fatal(err)
		}
		m := incoming()
		m.Text = "!ask test"
		_, _ = e.Accept(context.Background(), m)
		if err := e.Dispatch(context.Background()); err != nil {
			t.Fatal(err)
		}
		jobs, _ := e.Store.Jobs()
		if len(jobs) != 1 || jobs[0].Cost != nil {
			t.Fatalf("expected nil (unknown) cost when round 2 omitted cost, got %+v", jobs[0].Cost)
		}
	})

	t.Run("endpoint_rejecting_tools_retries_without_tools", func(t *testing.T) {
		var calls int
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls++
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if _, hasTools := body["tools"]; hasTools {
				w.WriteHeader(http.StatusBadRequest)
				w.Write([]byte(`{"error":"tools not supported"}`))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"model":"synthetic/agent","choices":[{"finish_reason":"stop","message":{"content":"Plain fallback answer"}}],"usage":{"prompt_tokens":8,"completion_tokens":4,"cost":0.0001}}`))
		}))
		defer server.Close()

		e, fixture := setup(t)
		e.Provider = NewOpenRouter()
		arm(t, e)
		_ = e.SetCompatibleProvider(server.URL+"/v1", "SYNTHETIC_KEY", false)
		model := Model{ID: "compatible:synthetic/agent", Name: "Agent", Provider: "compatible", RemoteID: "synthetic/agent", PricingKnown: true, Parameters: []string{}, ReasoningEfforts: []string{}}
		_ = e.Store.SetPublicSetting("model_catalog", []Model{model})

		c := DefaultConfiguration()
		c.Settings.DefaultModel = model.ID
		c.Commands = []Command{
			{Name: "ask", Type: "ai", Enabled: true, Instructions: "Assist"},
			{Name: "hello", Type: "fixed", Enabled: true, Instructions: "Hi {input}"},
		}
		if err := e.SetConfiguration(c); err != nil {
			t.Fatal(err)
		}
		m := incoming()
		m.Text = "!ask what is 2+2"
		_, _ = e.Accept(context.Background(), m)
		if err := e.Dispatch(context.Background()); err != nil {
			t.Fatal(err)
		}
		if calls != 2 || len(fixture.Sent) != 1 || !sentContains(fixture, "Plain fallback answer") {
			t.Fatalf("calls=%d sent=%v", calls, fixture.Sent)
		}
	})
}

func TestAskApprovalReapproveAndWorkerToolFailureIsolation(t *testing.T) {
	t.Parallel()
	t.Run("parent_approval_still_requires_reapproval_after_calling_gated_tool", func(t *testing.T) {
		var calls int
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			calls++
			w.Header().Set("Content-Type", "application/json")
			if calls == 1 {
				w.Write([]byte(`{"model":"synthetic/agent","choices":[{"finish_reason":"tool_calls","message":{"content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"gated","arguments":"{\"input\":\"world\"}"},"extra_content":{"google":{"thought_signature":"sig-123"}}}],"reasoning_details":[{"type":"reasoning.text","text":"thinking"}]}}],"usage":{"prompt_tokens":10,"completion_tokens":5,"cost":0.001}}`))
				return
			}
			w.Write([]byte(`{"model":"synthetic/agent","choices":[{"finish_reason":"stop","message":{"content":"Used gated tool"}}],"usage":{"prompt_tokens":15,"completion_tokens":5,"cost":0.001}}`))
		}))
		defer server.Close()

		e, fixture := setup(t)
		e.Provider = NewOpenRouter()
		arm(t, e)
		_ = e.SetCompatibleProvider(server.URL+"/v1", "SYNTHETIC_KEY", false)
		model := Model{ID: "compatible:synthetic/agent", Name: "Agent", Provider: "compatible", RemoteID: "synthetic/agent", PricingKnown: true, InputPrice: 0.000001, OutputPrice: 0.000001, Parameters: []string{"tools"}, ReasoningEfforts: []string{}}
		_ = e.Store.SetPublicSetting("model_catalog", []Model{model})

		c := DefaultConfiguration()
		c.Settings.DefaultModel = model.ID
		c.Commands = []Command{
			{Name: "ask", Type: "ai", Enabled: true, Approval: true, Instructions: "Assist"},
			{Name: "gated", Type: "fixed", Enabled: true, Approval: true, Instructions: "Gated {input}"},
		}
		if err := e.SetConfiguration(c); err != nil {
			t.Fatal(err)
		}
		m := incoming()
		m.Text = "!ask run gated"
		if ok, err := e.Accept(context.Background(), m); !ok || err != nil {
			t.Fatal(ok, err)
		}
		id := store.Hash(m.ConversationID + "\x00" + m.ID)
		if err := e.Dispatch(context.Background()); err != nil {
			t.Fatal(err)
		}
		// Initial approval of !ask before generation.
		if stateOf(t, e, id) != "awaiting_approval" {
			t.Fatalf("expected initial awaiting_approval, got %s", stateOf(t, e, id))
		}
		if err := e.Approve(id); err != nil {
			t.Fatal(err)
		}
		if err := e.Dispatch(context.Background()); err != nil {
			t.Fatal(err)
		}
		// Must pause again for approval because !gated had Approval: true.
		if stateOf(t, e, id) != "awaiting_approval" || len(fixture.Sent) != 0 {
			t.Fatalf("gated tool bypassed post-tool approval: state=%s sent=%v", stateOf(t, e, id), fixture.Sent)
		}
		if err := e.Approve(id); err != nil {
			t.Fatal(err)
		}
		if err := e.Dispatch(context.Background()); err != nil {
			t.Fatal(err)
		}
		if stateOf(t, e, id) != "submitted" || !sentContains(fixture, "Used gated tool") {
			t.Fatalf("after second approval: state=%s sent=%v", stateOf(t, e, id), fixture.Sent)
		}
	})

	t.Run("worker_failure_inside_tool_reports_to_model_without_killing_ask", func(t *testing.T) {
		var calls int
		var sawToolFailure bool
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls++
			w.Header().Set("Content-Type", "application/json")
			if calls == 1 {
				w.Write([]byte(`{"model":"synthetic/agent","choices":[{"finish_reason":"tool_calls","message":{"content":null,"tool_calls":[{"id":"v1","type":"function","function":{"name":"grab","arguments":"{\"url\":\"https://v.douyin.com/gallery/\"}"}}]}}],"usage":{"prompt_tokens":10,"completion_tokens":5,"cost":0.001}}`))
				return
			}
			var body struct {
				Messages []struct {
					Role    string `json:"role"`
					Content string `json:"content"`
				} `json:"messages"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			for _, m := range body.Messages {
				if m.Role == "tool" && strings.Contains(m.Content, "download_worker_unavailable") {
					sawToolFailure = true
				}
			}
			w.Write([]byte(`{"model":"synthetic/agent","choices":[{"finish_reason":"stop","message":{"content":"Sorry, the video worker isn't running right now."}}],"usage":{"prompt_tokens":20,"completion_tokens":8,"cost":0.001}}`))
		}))
		defer server.Close()

		e, fixture := setup(t)
		e.Provider = NewOpenRouter()
		arm(t, e)
		_ = e.SetCompatibleProvider(server.URL+"/v1", "SYNTHETIC_KEY", false)
		model := Model{ID: "compatible:synthetic/agent", Name: "Agent", Provider: "compatible", RemoteID: "synthetic/agent", PricingKnown: true, InputPrice: 0.000001, OutputPrice: 0.000001, Parameters: []string{"tools"}, ReasoningEfforts: []string{}}
		_ = e.Store.SetPublicSetting("model_catalog", []Model{model})

		c := DefaultConfiguration()
		c.Settings.DefaultModel = model.ID
		c.Commands = []Command{
			{Name: "ask", Type: "ai", Enabled: true, Instructions: "Assist"},
			{Name: "grab", Type: "download", Enabled: true},
		}
		if err := e.SetConfiguration(c); err != nil {
			t.Fatal(err)
		}
		// e.Tasks is nil -> download_worker_unavailable isolated to tool output!
		m := incoming()
		m.Text = "!ask grab https://v.douyin.com/gallery/"
		_, _ = e.Accept(context.Background(), m)
		if err := e.Dispatch(context.Background()); err != nil {
			t.Fatal(err)
		}
		if !sawToolFailure || !sentContains(fixture, "Sorry, the video worker isn't running right now.") {
			t.Fatalf("sawToolFailure=%t sent=%v", sawToolFailure, fixture.Sent)
		}
	})
}

func TestPlaygroundAndCJKDescriptionWithTools(t *testing.T) {
	t.Parallel()
	e, _ := setup(t)
	c := DefaultConfiguration()
	c.Settings.DefaultModel = "fixture/model"
	c.Commands = []Command{
		{Name: "ask", Type: "ai", Enabled: false, Memory: true, Instructions: "Assist"},
		{Name: "grab", Type: "download", Enabled: true, Description: strings.Repeat("下", 120)},
	}
	if err := e.SetConfiguration(c); err != nil {
		t.Fatalf("valid 120-rune CJK description rejected: %v", err)
	}
	for _, cmd := range []string{"ask", "a"} {
		res, err := e.Playground(cmd, "hello")
		if err != nil || res["text"] != "Simulated AI response. No provider request was made." {
			t.Fatalf("playground %s failed: res=%v err=%v", cmd, res, err)
		}
	}
}
