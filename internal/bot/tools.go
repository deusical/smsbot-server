package bot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/deusical/cmdflow"
	"github.com/deusical/smsbot-server/internal/media"
	"github.com/deusical/smsbot-server/internal/store"
	"github.com/deusical/smsbot-server/internal/transport"
)

const (
	maxAgentRounds   = 10
	maxToolTextBytes = 16000
)

// ToolSpec is an OpenAI-compatible function tool definition passed to a model.
type ToolSpec struct {
	Type     string       `json:"type"`
	Function ToolFunction `json:"function"`
}

// ToolFunction describes one callable function.
type ToolFunction struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Parameters  any    `json:"parameters"`
}

// ToolCall is one tool invocation returned by the model. ExtraContent preserves
// provider-specific metadata such as Gemini thought signatures across turns.
type ToolCall struct {
	ID           string           `json:"id"`
	Type         string           `json:"type"`
	Function     ToolCallFunction `json:"function"`
	ExtraContent json.RawMessage  `json:"extra_content,omitempty"`
}

// ToolCallFunction names the function the model called and carries its arguments.
type ToolCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// ChatRequest is a provider-agnostic chat-completions request with optional tools.
type ChatRequest struct {
	Model           string
	ModelInfo       Model
	Messages        []any
	Tools           []ToolSpec
	ToolChoice      string
	MaxTokens       int
	Temperature     *float64
	ReasoningEffort string
}

// ChatResponse is a chat-completions response that may contain tool calls.
type ChatResponse struct {
	Text             string
	ToolCalls        []ToolCall
	ReasoningContent json.RawMessage
	ReasoningDetails json.RawMessage
	Model            string
	PromptTokens     int
	CompletionTokens int
	ReasoningTokens  int
	Cost             *float64
	Finish           string
}

// ChatCompleter extends Provider with tool-calling support.
type ChatCompleter interface {
	ChatComplete(ctx context.Context, key string, req ChatRequest) (ChatResponse, error)
}

// parseChatResponse decodes the shared Chat Completions JSON into a ChatResponse.
func parseChatResponse(b []byte) (ChatResponse, error) {
	var raw struct {
		Model   string `json:"model"`
		Choices []struct {
			Finish  string `json:"finish_reason"`
			Message struct {
				Content          json.RawMessage `json:"content"`
				ToolCalls        []ToolCall      `json:"tool_calls"`
				ReasoningContent json.RawMessage `json:"reasoning_content"`
				ReasoningDetails json.RawMessage `json:"reasoning_details"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			Prompt     int             `json:"prompt_tokens"`
			Completion int             `json:"completion_tokens"`
			Cost       json.RawMessage `json:"cost"`
			Details    struct {
				Reasoning int `json:"reasoning_tokens"`
			} `json:"completion_tokens_details"`
		} `json:"usage"`
	}
	if json.Unmarshal(b, &raw) != nil || len(raw.Choices) == 0 {
		return ChatResponse{}, errors.New("provider_response_invalid")
	}
	msg := raw.Choices[0].Message
	for i := range msg.ToolCalls {
		if msg.ToolCalls[i].ID == "" {
			msg.ToolCalls[i].ID = fmt.Sprintf("call_%d", i+1)
		}
		if msg.ToolCalls[i].Type == "" {
			msg.ToolCalls[i].Type = "function"
		}
	}
	resp := ChatResponse{
		ToolCalls:        msg.ToolCalls,
		ReasoningContent: msg.ReasoningContent,
		ReasoningDetails: msg.ReasoningDetails,
		Model:            raw.Model,
		PromptTokens:     raw.Usage.Prompt,
		CompletionTokens: raw.Usage.Completion,
		ReasoningTokens:  raw.Usage.Details.Reasoning,
		Finish:           raw.Choices[0].Finish,
	}
	if len(raw.Usage.Cost) > 0 && string(raw.Usage.Cost) != "null" {
		parsed, ok := compatibleNumber(raw.Usage.Cost)
		if !ok {
			return ChatResponse{}, errors.New("provider_usage_invalid")
		}
		resp.Cost = &parsed
	}
	if resp.PromptTokens < 0 || resp.CompletionTokens < 0 || resp.ReasoningTokens < 0 ||
		resp.Cost != nil && (*resp.Cost < 0 || math.IsNaN(*resp.Cost) || math.IsInf(*resp.Cost, 0)) {
		return ChatResponse{}, errors.New("provider_usage_invalid")
	}
	if len(msg.Content) > 0 && string(msg.Content) != "null" {
		if json.Unmarshal(msg.Content, &resp.Text) != nil {
			var parts []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			}
			if json.Unmarshal(msg.Content, &parts) != nil {
				return resp, errors.New("provider_response_invalid")
			}
			for _, part := range parts {
				if part.Type == "text" {
					resp.Text += part.Text
				}
			}
		}
		resp.Text = strings.TrimSpace(resp.Text)
	}
	return resp, nil
}

// toolSnapshot is an enabled command snapshotted at acceptance so an AI step
// runs the exact recipe that existed when the message was accepted.
type toolSnapshot struct {
	Name             string   `json:"name"`
	Description      string   `json:"description"`
	Approval         bool     `json:"approval,omitempty"`
	ExecutionMinutes int      `json:"execution_minutes,omitempty"`
	HostMaxBytes     int64    `json:"host_max_bytes,omitempty"`
	VideoURL         bool     `json:"video_url,omitempty"`
	Pipeline         Pipeline `json:"pipeline"`
}

// agentState persists multi-round tool execution across yielded worker polls.
type agentState struct {
	Round            int               `json:"round"`
	Messages         []json.RawMessage `json:"messages,omitempty"`
	PendingCalls     []ToolCall        `json:"pending_calls,omitempty"`
	ActiveCall       *ToolCall         `json:"active_call,omitempty"`
	ActiveTool       string            `json:"active_tool,omitempty"`
	SubRun           *cmdflow.Run      `json:"sub_run,omitempty"`
	CallSeq          int               `json:"call_seq,omitempty"`
	Model            string            `json:"model,omitempty"`
	PromptTokens     int               `json:"prompt_tokens,omitempty"`
	CompletionTokens int               `json:"completion_tokens,omitempty"`
	ReasoningTokens  int               `json:"reasoning_tokens,omitempty"`
	Cost             *float64          `json:"cost,omitempty"`
	CostKnown        bool              `json:"cost_known"`
	ToolsDisabled    bool              `json:"tools_disabled,omitempty"`
	NeedsApproval    bool              `json:"needs_approval,omitempty"`
	UsedTools        []string          `json:"used_tools,omitempty"`
}

func (a *agentState) recordUsage(resp ChatResponse) {
	a.PromptTokens += resp.PromptTokens
	a.CompletionTokens += resp.CompletionTokens
	a.ReasoningTokens += resp.ReasoningTokens
	if resp.Model != "" {
		if a.Model == "" {
			a.Model = resp.Model
		} else if !strings.Contains(", "+a.Model+", ", ", "+resp.Model+", ") {
			a.Model += ", " + resp.Model
		}
	}
	if resp.Cost == nil {
		a.CostKnown = false
		a.Cost = nil
		return
	}
	if !a.CostKnown {
		return
	}
	sum := *resp.Cost
	if a.Cost != nil {
		sum += *a.Cost
	}
	a.Cost = &sum
}

func snapshotCommandTools(e *Engine, cfg Configuration, exclude, conv string) []toolSnapshot {
	var hostMax int64
	if e != nil && e.Collection != nil {
		if host, err := e.Collection.Settings(); err == nil {
			hostMax = videoHostLimit(host.FileLimits())
		}
	}
	var out []toolSnapshot
	for _, cmd := range cfg.Commands {
		if !cmd.Enabled || cmd.Name == exclude || (conv != "" && !allowed(cmd, conv)) {
			continue
		}
		pl := commandPipeline(cmd)
		if len(pl.Steps) == 0 {
			continue
		}
		sub, ok := prepareToolPipeline(e, cfg, pl)
		if !ok {
			continue
		}
		minutes := cmd.ExecutionMinutes
		if sub.ExecutionMinutes > 0 {
			minutes = sub.ExecutionMinutes
		}
		out = append(out, toolSnapshot{
			Name:             cmd.Name,
			Description:      describeCommand(cmd, pl),
			Approval:         cmd.Approval,
			ExecutionMinutes: minutes,
			HostMaxBytes:     hostMax,
			VideoURL:         pl.Has("text.extract_url") || pl.Has("video.download"),
			Pipeline:         sub,
		})
	}
	return out
}

func prepareToolPipeline(e *Engine, cfg Configuration, pl Pipeline) (Pipeline, bool) {
	sub := clonePipeline(pl)
	for i := range sub.Steps {
		step := &sub.Steps[i]
		if step.Module == "context.rolling" || step.Module == "context.thread" {
			step.Module = "context.none"
			step.Settings = nil
			continue
		}
		if step.Module != "ai.chat" {
			continue
		}
		var set aiSettings
		if len(step.Settings) > 0 && json.Unmarshal(step.Settings, &set) != nil {
			return Pipeline{}, false
		}
		model, instructions, g := resolveAI(cfg, set)
		if e != nil {
			if model == "" {
				return Pipeline{}, false
			}
			selected, err := e.model(model)
			if err != nil || !e.ProviderConfiguredFor(selected) || validateModel(g, selected) != nil {
				return Pipeline{}, false
			}
		}
		// Freeze resolved model, instructions and generation into the acceptance
		// snapshot so later profile/default edits never alter a queued job.
		frozen := aiSettings{Model: model, Instructions: instructions, Generation: &g}
		step.Settings, _ = json.Marshal(frozen)
	}
	if validatePipeline(&sub, pipelineEnv{}) != nil {
		return Pipeline{}, false
	}
	return sub, true
}

func describeCommand(cmd Command, pl Pipeline) string {
	if d := strings.TrimSpace(cmd.Description); d != "" {
		return d
	}
	switch {
	case pl.Has("video.download"):
		return "Download and compress a Douyin or Xiaohongshu video from a URL"
	case pl.Has("ai.chat"):
		return "Run the !" + cmd.Name + " AI command"
	case pl.Has("text.template"):
		return "Run the !" + cmd.Name + " preset reply"
	default:
		return "Run the !" + cmd.Name + " command"
	}
}

func toolSpecs(tools []toolSnapshot) []ToolSpec {
	if len(tools) == 0 {
		return nil
	}
	specs := make([]ToolSpec, 0, len(tools))
	for _, t := range tools {
		param := "input"
		desc := "Text input to pass to the command (pass empty string if none needed)"
		if t.VideoURL {
			param = "url"
			desc = "Douyin or Xiaohongshu video URL (or share text containing the link)"
		}
		specs = append(specs, ToolSpec{
			Type: "function",
			Function: ToolFunction{
				Name:        t.Name,
				Description: t.Description,
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						param: map[string]any{"type": "string", "description": desc},
					},
					"required":             []string{param},
					"additionalProperties": false,
				},
			},
		})
	}
	return specs
}

func clampUTF8(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	s = s[:maxBytes]
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}

func extractToolArg(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	var parsed map[string]any
	if json.Unmarshal([]byte(raw), &parsed) == nil {
		for _, k := range []string{"url", "input", "text", "query", "prompt"} {
			if val, exists := parsed[k]; exists {
				if s, ok := val.(string); ok {
					return clampUTF8(s, maxToolTextBytes)
				}
			}
		}
		if len(parsed) == 1 {
			for _, v := range parsed {
				if s, ok := v.(string); ok {
					return clampUTF8(s, maxToolTextBytes)
				}
			}
		}
		return ""
	}
	var plain string
	if json.Unmarshal([]byte(raw), &plain) == nil {
		return clampUTF8(plain, maxToolTextBytes)
	}
	return ""
}

func prefixToolPipeline(pl Pipeline, seq int) Pipeline {
	out := clonePipeline(pl)
	prefix := "t" + strconv.Itoa(seq) + "_"
	used := map[string]bool{"trigger": true}
	renames := make(map[string]string, len(out.Steps))
	for i := range out.Steps {
		old := out.Steps[i].ID
		next := prefix + old
		if len(next) > 24 {
			next = next[:24]
		}
		for n := 2; used[next]; n++ {
			suffix := "_" + strconv.Itoa(n)
			base := prefix + old
			if len(base) > 24-len(suffix) {
				base = base[:24-len(suffix)]
			}
			next = base + suffix
		}
		used[next] = true
		renames[old] = next
		out.Steps[i].ID = next
	}
	for i := range out.Steps {
		if len(out.Steps[i].Inputs) == 0 {
			continue
		}
		updated := make(map[string]string, len(out.Steps[i].Inputs))
		for port, binding := range out.Steps[i].Inputs {
			from, outPort, ok := strings.Cut(binding, ".")
			if ok {
				if renamed, found := renames[from]; found {
					binding = renamed + "." + outPort
				}
			}
			updated[port] = binding
		}
		out.Steps[i].Inputs = updated
	}
	return out
}

type subToolHost struct {
	e             *Engine
	j             store.Job
	p             *store.Plan
	parentStep    *cmdflow.Step
	st            *aiState
	attachedName  string
	limitExceeded bool
	failCode      string
}

func (h *subToolHost) Effect(s *cmdflow.Step) error {
	if v, ok := s.Take("file"); ok {
		var f *media.File
		if v.Handle != "" {
			if staged, found := h.p.StagedFile(v.Handle); found {
				f = &staged
			}
		} else {
			f = valueMedia(v)
		}
		if f == nil {
			h.p.Staged = nil
			return cmdflow.ErrFailed
		}
		inbox, err := h.e.Collection.Settings()
		candidate := append(append([]media.File(nil), h.p.Attachments...), *f)
		if err == nil && inbox.FileLimits().Check(candidate) == nil {
			h.p.Attachments = candidate
			h.attachedName = f.Name
		} else {
			h.limitExceeded = true
		}
	}
	h.p.Staged = nil
	return nil
}

func (h *subToolHost) Halt(s *cmdflow.Step, v cmdflow.Value) error {
	if s.Outputs == nil {
		s.Outputs = map[string]cmdflow.Value{}
	}
	s.Outputs["halt"] = v
	h.p.Staged = nil
	return nil
}

func (h *subToolHost) Save() error {
	if err := h.parentStep.Save(*h.st); err != nil {
		return err
	}
	if h.j.ID == "" {
		return nil
	}
	return h.e.Store.SavePlan(h.j.ID, *h.p)
}

func (h *subToolHost) Fail(code string) error {
	h.failCode = code
	return nil
}

func summarizeSubRun(run *cmdflow.Run, name string, host *subToolHost) string {
	if host.failCode != "" {
		return "Tool !" + name + " failed (" + host.failCode + ")."
	}
	if msg, halted := haltedText(run); halted {
		return clampUTF8(msg, maxToolTextBytes)
	}
	parts := []string{}
	if text := strings.TrimSpace(stepText(run, "reply", "text")); text != "" {
		parts = append(parts, clampUTF8(text, maxToolTextBytes))
	}
	if host.attachedName != "" {
		parts = append(parts, fmt.Sprintf("Attached file %s (ready to send with your reply).", host.attachedName))
	} else if host.limitExceeded {
		parts = append(parts, "Could not attach file: message attachment limit reached.")
	}
	if len(parts) == 0 {
		return "Done."
	}
	return strings.Join(parts, "\n")
}

func appendRawMessage(list []json.RawMessage, msg any) []json.RawMessage {
	b, err := json.Marshal(msg)
	if err != nil {
		return list
	}
	return append(list, b)
}

func decodeRawMessages(list []json.RawMessage) []any {
	out := make([]any, 0, len(list))
	for _, raw := range list {
		var v any
		if json.Unmarshal(raw, &v) == nil {
			out = append(out, v)
		}
	}
	return out
}

func agentMessagesLen(list []json.RawMessage) int {
	n := 0
	for _, raw := range list {
		n += len(raw) + 32
	}
	return n
}

func toolUnsupportedError(err error) bool {
	if err == nil {
		return false
	}
	switch err.Error() {
	case "provider_http_400", "provider_http_404", "provider_http_422":
		return true
	}
	return false
}

func deterministicZeroCostError(err error) bool {
	if err == nil {
		return false
	}
	code := err.Error()
	return strings.HasPrefix(code, "provider_http_4") || code == "provider_request_invalid"
}

func toolStageName(name string) string {
	clean := "running_" + strings.ReplaceAll(name, "-", "_")
	if len(clean) > 48 {
		clean = clean[:48]
	}
	return clean
}

func agentInProgress(p store.Plan) bool {
	if p.Pipeline == nil {
		return false
	}
	for i, s := range p.Pipeline.Steps {
		if i > 0 && s.Module == "ai.chat" && !s.Finished() {
			return true
		}
	}
	return false
}

func (e *Engine) approvalWindow(p store.Plan) string {
	window := e.now().Add(2 * time.Minute)
	if p.ExecutionDeadline != "" {
		if execDeadline, err := time.Parse(time.RFC3339Nano, p.ExecutionDeadline); err == nil && execDeadline.Before(window) {
			window = execDeadline
		}
	}
	return window.UTC().Format(time.RFC3339Nano)
}

func (e *Engine) ensureAgentDeadline(j store.Job, p *store.Plan, minutes int) {
	if minutes <= 0 {
		minutes = 15
	}
	created, err := time.Parse(time.RFC3339Nano, j.CreatedAt)
	if err != nil {
		return
	}
	candidate := created.Add(time.Duration(minutes) * time.Minute)
	if p.ExecutionDeadline != "" {
		if existing, perr := time.Parse(time.RFC3339Nano, p.ExecutionDeadline); perr == nil && !candidate.After(existing) {
			return
		}
	}
	p.ExecutionDeadline = candidate.UTC().Format(time.RFC3339Nano)
}

// runSubToolAI executes a specialist AI step inside a tool sub-pipeline. Usage
// rolls into the parent AI step, and tool-level provider errors report back to
// the parent agent rather than terminating the whole job.
func (e *Engine) runSubToolAI(r *runCtx, st aiState, commit func(string)) (cmdflow.Outcome, error) {
	m, err := e.model(st.Model)
	if err != nil {
		return cmdflow.Fail("model_unavailable"), nil
	}
	var g Generation
	if json.Unmarshal(st.Generation, &g) != nil || validateModel(g, m) != nil {
		return cmdflow.Fail("generation_unsupported"), nil
	}
	reserve := m.OutputPrice*float64(g.Tokens) + m.InputPrice*float64(len(st.Input)+len(st.Instructions)+512)
	day := e.now().UTC().Format("2006-01-02") + "T00:00:00Z"
	spent, unknown, err := e.Store.SpendSince(day)
	if err != nil {
		return cmdflow.Yield(), err
	}
	if r.cfg.Settings.DailyBudget > 0 && (!m.PricingKnown || unknown > 0 || spent+reserve > r.cfg.Settings.DailyBudget) {
		return yielded(true, e.Store.Complete(r.j.ID, "failed", "budget_exceeded_or_unknown"))
	}
	prov, key, err := e.providerForModel(m)
	if err != nil || len(key) == 0 {
		return cmdflow.Fail("provider_key_missing"), nil
	}
	ok, err := e.Store.Transition(r.j.ID, "queued", "generating")
	if err != nil || !ok {
		return cmdflow.Yield(), err
	}
	genctx, cancel := context.WithTimeout(r.ctx, 95*time.Second)
	e.pending[r.j.ID] = cancel
	e.mu.Unlock()

	request := *r.p
	request.Model, request.Instructions, request.Generation, request.Input, request.File = st.Model, st.Instructions, st.Generation, st.Input, nil
	res, generr := prov.Generate(genctx, key, request, nil, m, nil)

	cancel()
	e.mu.Lock()
	delete(e.pending, r.j.ID)

	reportedModel := res.Model
	if reportedModel == "" {
		reportedModel = st.Model
	}
	cost := res.Cost
	if generr != nil && deterministicZeroCostError(generr) && res.PromptTokens == 0 && res.CompletionTokens == 0 {
		zero := 0.0
		cost = &zero
	}
	if r.subUsage != nil && (generr == nil || cost == nil || res.PromptTokens > 0 || res.CompletionTokens > 0) {
		r.subUsage(ChatResponse{
			Model:            reportedModel,
			PromptTokens:     res.PromptTokens,
			CompletionTokens: res.CompletionTokens,
			ReasoningTokens:  res.ReasoningTokens,
			Cost:             cost,
		})
	}
	current, loaderr := e.Store.Job(r.j.ID)
	if loaderr != nil && current.State == "generating" {
		return cmdflow.Yield(), loaderr
	}
	if loaderr != nil || current.State != "generating" {
		return cmdflow.Yield(), nil
	}
	if _, err = e.Store.Transition(r.j.ID, "generating", "queued"); err != nil {
		return cmdflow.Yield(), err
	}
	if generr != nil {
		return cmdflow.Fail(safeProviderCode(generr)), nil
	}
	commit(res.Text)
	return cmdflow.Done(), nil
}

// runAgentStep runs an AI step that has snapshotted command tools, yielding
// cleanly whenever a called tool polls a background worker.
func (e *Engine) runAgentStep(ctx context.Context, j store.Job, p *store.Plan, cfg *Configuration, step *cmdflow.Step, st *aiState, history []store.Turn, commit func(string)) (done bool, err error) {
	if st.Agent == nil {
		zero := 0.0
		st.Agent = &agentState{CostKnown: true, Cost: &zero}
	}
	ag := st.Agent

	m, err := e.model(st.Model)
	if err != nil {
		return true, e.Store.Complete(j.ID, "failed", "model_unavailable")
	}
	var g Generation
	if json.Unmarshal(st.Generation, &g) != nil || validateModel(g, m) != nil {
		return true, e.Store.Complete(j.ID, "failed", "generation_unsupported")
	}
	if (m.Provider == "" || m.Provider == "openrouter") && len(m.Parameters) > 0 && !contains(m.Parameters, "tools") {
		ag.ToolsDisabled = true
	}
	call := aiCall{Step: step.ID, Model: st.Model, Instructions: st.Instructions, Generation: st.Generation, Input: st.Input, File: st.File}
	if ag.ToolsDisabled || len(st.Tools) == 0 {
		return e.generateReply(ctx, j, p, cfg, call, history, commit)
	}
	prov, key, err := e.providerForModel(m)
	if err != nil || len(key) == 0 {
		return true, e.Store.Complete(j.ID, "failed", "provider_key_missing")
	}
	completer, ok := prov.(ChatCompleter)
	if !ok {
		return e.generateReply(ctx, j, p, cfg, call, history, commit)
	}

	recordSubUsage := func(sub ChatResponse) {
		ag.recordUsage(sub)
		reported := ag.Model
		if reported == "" {
			reported = st.Model
		}
		_ = e.Store.StepUsage(j.ID, step.ID, reported, ag.PromptTokens, ag.CompletionTokens, ag.ReasoningTokens, ag.Cost)
	}

	var loadedImage *Attachment
	var imageLoaded bool

	for {
		for ag.SubRun != nil || len(ag.PendingCalls) > 0 {
			var hostMax int64
			if ag.SubRun == nil {
				tc := ag.PendingCalls[0]
				ag.PendingCalls = ag.PendingCalls[1:]
				var matched *toolSnapshot
				for i := range st.Tools {
					if st.Tools[i].Name == tc.Function.Name {
						matched = &st.Tools[i]
						break
					}
				}
				if matched == nil {
					ag.Messages = appendRawMessage(ag.Messages, map[string]any{
						"role":         "tool",
						"tool_call_id": tc.ID,
						"content":      "Unknown tool: " + tc.Function.Name,
					})
					continue
				}
				// Re-verify live enablement, conversation allowlist and approval.
				liveCmd, _, _, _, liveOK := resolve(*cfg, matched.Name)
				if !liveOK || !allowed(liveCmd, j.ConversationID) {
					ag.Messages = appendRawMessage(ag.Messages, map[string]any{
						"role":         "tool",
						"tool_call_id": tc.ID,
						"content":      "Tool !" + matched.Name + " is unavailable in this conversation.",
					})
					continue
				}
				arg := extractToolArg(tc.Function.Arguments)
				hostMax = matched.HostMaxBytes
				ag.CallSeq++
				subPl := prefixToolPipeline(matched.Pipeline, ag.CallSeq)
				subRun := cmdflow.Snapshot(subPl, triggerValues(arg, transport.Incoming{ConversationID: j.ConversationID}))
				host := &subToolHost{e: e, j: j, p: p, parentStep: step, st: st}
				acceptCtx := &runCtx{
					e:               e,
					ctx:             ctx,
					p:               p,
					cfg:             cfg,
					m:               transport.Incoming{ConversationID: j.ConversationID},
					cmd:             Command{Name: matched.Name},
					pipeline:        subPl,
					subTool:         true,
					subHostMaxBytes: hostMax,
				}
				res, aerr := commands.Accept(acceptCtx, subRun, host)
				if aerr != nil {
					ag.Messages = appendRawMessage(ag.Messages, map[string]any{
						"role":         "tool",
						"tool_call_id": tc.ID,
						"content":      "Tool !" + matched.Name + " could not start.",
					})
					continue
				}
				if res == cmdflow.Declined {
					ag.Messages = appendRawMessage(ag.Messages, map[string]any{
						"role":         "tool",
						"tool_call_id": tc.ID,
						"content":      "Tool !" + matched.Name + " is unavailable.",
					})
					continue
				}
				if res == cmdflow.Halted {
					msg, _ := haltedText(subRun)
					if msg == "" {
						msg = "Tool halted."
					}
					ag.Messages = appendRawMessage(ag.Messages, map[string]any{
						"role":         "tool",
						"tool_call_id": tc.ID,
						"content":      clampUTF8(msg, maxToolTextBytes),
					})
					continue
				}
				bang := "!" + matched.Name
				if !contains(ag.UsedTools, bang) {
					ag.UsedTools = append(ag.UsedTools, bang)
					p.UsedTools = append([]string(nil), ag.UsedTools...)
					_ = e.Store.SetPublicSetting("used_tools:"+j.ID, ag.UsedTools)
				}
				if matched.Approval || liveCmd.Approval {
					ag.NeedsApproval = true
				}
				if longRunning(matched.Pipeline) || matched.Pipeline.Has("ai.chat") {
					e.ensureAgentDeadline(j, p, matched.ExecutionMinutes)
				}
				_ = e.Store.DeferWork(j.ID, toolStageName(matched.Name), 0)
				ag.ActiveCall = &tc
				ag.ActiveTool = matched.Name
				ag.SubRun = subRun
				if err := host.Save(); err != nil {
					return true, err
				}
			}

			host := &subToolHost{e: e, j: j, p: p, parentStep: step, st: st}
			subCtx := &runCtx{e: e, ctx: ctx, p: p, cfg: cfg, j: j, subTool: true, subUsage: recordSubUsage}
			subDone, subErr := commands.Resume(subCtx, ag.SubRun, host)
			subFailed := host.failCode != "" || errors.Is(subErr, cmdflow.ErrFailed) || errors.Is(subErr, cmdflow.ErrFormat) || errors.Is(subErr, cmdflow.ErrNotAccepted)
			if subDone && !subFailed {
				if current, lerr := e.Store.Job(j.ID); lerr == nil && current.State == "queued" {
					_ = host.Save()
				}
				return true, subErr
			}
			if subErr != nil && !subFailed {
				return true, subErr
			}
			if subFailed && host.failCode == "" {
				host.failCode = "tool_step_failed"
			}
			output := summarizeSubRun(ag.SubRun, ag.ActiveTool, host)
			ag.Messages = appendRawMessage(ag.Messages, map[string]any{
				"role":         "tool",
				"tool_call_id": ag.ActiveCall.ID,
				"content":      output,
			})
			ag.SubRun = nil
			ag.ActiveCall = nil
			ag.ActiveTool = ""
			if err := host.Save(); err != nil {
				return true, err
			}
		}

		if ag.Round >= maxAgentRounds {
			_ = e.Store.DeferWork(j.ID, "", 0)
			return true, e.Store.Complete(j.ID, "failed", "provider_empty_answer")
		}
		if ag.Round > 0 {
			checked, code, cerr := e.check(ctx, j, *p, cachedLookup)
			*cfg = checked
			if cerr != nil {
				return true, cerr
			}
			if code != "" && code != "approval_required" {
				state := "cancelled"
				if code == "stale" || code == "execution_expired" {
					state = "expired"
				}
				return true, e.Store.Complete(j.ID, state, code)
			}
		}

		reserve := m.OutputPrice*float64(g.Tokens) + m.InputPrice*float64(len(st.Input)+len(st.Instructions)+512+agentMessagesLen(ag.Messages))
		if st.File != nil {
			reserve += m.InputPrice * float64(fileTokens(st.File))
		}
		for _, turn := range history {
			reserve += m.InputPrice * float64(len(turn.Content)+64)
		}
		day := e.now().UTC().Format("2006-01-02") + "T00:00:00Z"
		spent, unknown, err := e.Store.SpendSince(day)
		if err != nil {
			return true, err
		}
		if cfg.Settings.DailyBudget > 0 && (!m.PricingKnown || unknown > 0 || spent+reserve > cfg.Settings.DailyBudget) {
			return true, e.Store.Complete(j.ID, "failed", "budget_exceeded_or_unknown")
		}

		_ = e.Store.DeferWork(j.ID, "", 0)
		ok, err := e.Store.Transition(j.ID, "queued", "generating")
		if err != nil || !ok {
			return true, err
		}
		if ag.Round == 0 {
			if err = e.Store.StepUsage(j.ID, step.ID, st.Model, 0, 0, 0, nil); err != nil {
				return true, err
			}
			if err = e.Store.SetPublicSetting("requested_reasoning:"+j.ID, g.Reasoning); err != nil {
				return true, err
			}
		}

		genctx, cancel := context.WithTimeout(ctx, 95*time.Second)
		e.pending[j.ID] = cancel
		e.mu.Unlock()

		var resp ChatResponse
		var imageErr error
		if !imageLoaded {
			loadedImage, imageErr = e.loadFile(genctx, j.ConversationID, st.File, cfg.Settings.AIPhotoMaxMB)
			if imageErr == nil {
				imageLoaded = true
			}
		}
		var generr error
		disabledRetry := false
		if imageErr == nil {
			request := *p
			request.Model, request.Instructions, request.Generation, request.Input, request.File = st.Model, st.Instructions, st.Generation, st.Input, st.File
			buildMessages := func(img *Attachment) []any {
				return append(chatMessages(request, g, history, img), decodeRawMessages(ag.Messages)...)
			}
			var activeTools []ToolSpec
			toolChoice := ""
			if !ag.ToolsDisabled {
				activeTools = toolSpecs(st.Tools)
				if ag.Round == maxAgentRounds-1 && len(activeTools) > 0 {
					toolChoice = "none"
				}
			}
			req := ChatRequest{
				Model:           remoteModelID(request, m),
				ModelInfo:       m,
				Messages:        buildMessages(loadedImage),
				Tools:           activeTools,
				ToolChoice:      toolChoice,
				MaxTokens:       g.Tokens,
				Temperature:     g.Temperature,
				ReasoningEffort: g.Reasoning,
			}
			resp, generr = completer.ChatComplete(genctx, key, req)
			if generr != nil && loadedImage != nil && loadedImage.Kind == kindPDF && m.Provider == compatibleProviderID && providerRefused(generr) {
				asImage := *loadedImage
				asImage.AsImage = true
				loadedImage = &asImage
				req.Messages = buildMessages(loadedImage)
				resp, generr = completer.ChatComplete(genctx, key, req)
			}
			if generr != nil && loadedImage == nil && ag.Round == 0 && len(activeTools) > 0 && toolUnsupportedError(generr) {
				disabledRetry = true
				req.Tools = nil
				req.ToolChoice = ""
				resp, generr = completer.ChatComplete(genctx, key, req)
			}
		}

		cancel()
		e.mu.Lock()
		delete(e.pending, j.ID)

		if disabledRetry {
			ag.ToolsDisabled = true
		}
		if imageErr != nil {
			if ag.Round == 0 {
				zero := 0.0
				resp.Cost = &zero
			}
		} else if generr != nil {
			if deterministicZeroCostError(generr) && resp.PromptTokens == 0 && resp.CompletionTokens == 0 {
				zero := 0.0
				resp.Cost = &zero
			}
			ag.recordUsage(resp)
		} else {
			ag.recordUsage(resp)
		}
		reportedModel := ag.Model
		if reportedModel == "" {
			reportedModel = st.Model
		}
		if err = e.Store.StepUsage(j.ID, step.ID, reportedModel, ag.PromptTokens, ag.CompletionTokens, ag.ReasoningTokens, ag.Cost); err != nil {
			return true, err
		}

		current, loaderr := e.Store.Job(j.ID)
		if loaderr != nil && current.State == "generating" {
			return true, loaderr
		}
		if loaderr != nil || current.State != "generating" {
			return true, nil
		}

		if imageErr != nil {
			p.Attachments = nil
			resp, generr = ChatResponse{Text: fileFailureReply(imageErr), Model: st.Model}, nil
		} else if generr != nil && loadedImage != nil && ag.Round == 0 && providerRefused(generr) {
			p.Attachments = nil
			resp, generr = ChatResponse{Text: fileRefusalReply(generr), Model: st.Model}, nil
		}
		if generr != nil {
			return true, e.Store.Complete(j.ID, "failed", safeProviderCode(generr))
		}

		ag.Round++
		if len(resp.ToolCalls) > 0 {
			e.ensureAgentDeadline(j, p, 15)
			assistantMsg := map[string]any{"role": "assistant", "content": nil, "tool_calls": resp.ToolCalls}
			if resp.Text != "" {
				assistantMsg["content"] = resp.Text
			}
			if len(resp.ReasoningContent) > 0 && string(resp.ReasoningContent) != "null" {
				assistantMsg["reasoning_content"] = resp.ReasoningContent
			}
			if len(resp.ReasoningDetails) > 0 && string(resp.ReasoningDetails) != "null" {
				assistantMsg["reasoning_details"] = resp.ReasoningDetails
			}
			ag.Messages = appendRawMessage(ag.Messages, assistantMsg)
			ag.PendingCalls = resp.ToolCalls
			if err = step.Save(*st); err != nil {
				return true, err
			}
			if err = e.Store.SavePlan(j.ID, *p); err != nil {
				return true, err
			}
			if _, err = e.Store.Transition(j.ID, "generating", "queued"); err != nil {
				return true, err
			}
			continue
		}

		finalText := strings.TrimSpace(resp.Text)
		if finalText == "" && len(p.Attachments) > 0 {
			finalText = "Here is the video."
		}
		if finalText == "" {
			code := "provider_empty_answer"
			if resp.Finish == "length" {
				code = "provider_token_limit_empty"
			}
			return true, e.Store.Complete(j.ID, "failed", code)
		}
		if resp.Finish == "length" {
			finalText += "\n[AI token limit reached; increase the budget or lower reasoning effort.]"
		}
		needToolApproval := ag.NeedsApproval
		if !needToolApproval {
			for _, used := range p.UsedTools {
				if tcmd, _, _, _, tok := resolve(*cfg, strings.TrimPrefix(used, "!")); tok && tcmd.Approval {
					needToolApproval = true
					break
				}
			}
		}
		if needToolApproval {
			p.Approval = true
			p.Approved = false
		}
		st.Agent = nil
		if err = step.Save(*st); err != nil {
			return true, err
		}
		commit(finalText)
		checked, code, err := e.check(ctx, j, *p, liveLookup)
		*cfg = checked
		if err != nil {
			_ = e.Store.Complete(j.ID, "failed", "final_check_failed")
			return true, err
		}
		if code != "" {
			if code == "approval_required" {
				p.Approval = true
				p.Approved = false
				p.ApprovalDeadline = e.approvalWindow(*p)
				if err = e.Store.SavePlan(j.ID, *p); err != nil {
					return true, err
				}
				_, err = e.Store.Transition(j.ID, "generating", "awaiting_approval")
			} else {
				state := "cancelled"
				if code == "stale" || code == "execution_expired" {
					state = "expired"
				}
				err = e.Store.Complete(j.ID, state, code)
			}
			return true, err
		}
		if err = e.Store.SavePlan(j.ID, *p); err != nil {
			return true, err
		}
		if _, err = e.Store.Transition(j.ID, "generating", "queued"); err != nil {
			return true, err
		}
		return false, nil
	}
}
