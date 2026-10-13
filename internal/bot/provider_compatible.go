package bot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"smsbot-server/internal/store"
)

const compatibleProviderID = "compatible"

// OpenAICompatible supports the Chat Completions and /models contracts used by
// OpenAI and compatible gateways. Its credentials are supplied by the owner.
type OpenAICompatible struct {
	client *http.Client
	base   string
	key    string
}

func NewOpenAICompatible(baseURL, key string) *OpenAICompatible {
	return &OpenAICompatible{
		client: &http.Client{Timeout: 95 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		base:   strings.TrimRight(baseURL, "/"),
		key:    key,
	}
}

func compatibleNumber(value json.RawMessage) (float64, bool) {
	if len(value) == 0 || string(value) == "null" {
		return 0, false
	}
	var number float64
	if json.Unmarshal(value, &number) == nil {
		return number, !math.IsNaN(number) && !math.IsInf(number, 0)
	}
	var text string
	if json.Unmarshal(value, &text) != nil {
		return 0, false
	}
	number, err := strconv.ParseFloat(text, 64)
	return number, err == nil && !math.IsNaN(number) && !math.IsInf(number, 0)
}

func (p *OpenAICompatible) request(ctx context.Context, path, key string, body any) ([]byte, error) {
	var content io.Reader
	method := http.MethodGet
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, errors.New("provider_request_invalid")
		}
		content = bytes.NewReader(b)
		method = http.MethodPost
	}
	req, err := http.NewRequestWithContext(ctx, method, p.base+path, content)
	if err != nil {
		return nil, errors.New("provider_request_invalid")
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := p.client.Do(req)
	if err != nil {
		return nil, errors.New("provider_connection_failed")
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, fmt.Errorf("provider_http_%d", res.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(res.Body, 4*1024*1024+1))
	if err != nil || len(b) > 4*1024*1024 {
		return nil, errors.New("provider_response_invalid")
	}
	return b, nil
}

func (p *OpenAICompatible) Generate(ctx context.Context, key string, plan store.Plan, history []store.Turn, model Model) (Result, error) {
	var generation Generation
	if json.Unmarshal(plan.Generation, &generation) != nil {
		return Result{}, errors.New("generation_invalid")
	}
	if err := validateModel(generation, model); err != nil {
		return Result{}, err
	}
	instruction := map[string]string{"short": "Prefer a short answer.", "standard": "Give a complete answer with enough detail to be useful.", "detailed": "Give a thorough answer with useful examples."}[generation.AnswerLength]
	system := plan.Instructions + "\nUse concise plain text suitable for messaging. Avoid Markdown tables and LaTeX. " + instruction + " Preserve exact numbers, dates and factual constraints."
	messages := []store.Turn{{Role: "system", Content: system}}
	for _, turn := range history {
		if turn.Role == "user" || turn.Role == "assistant" {
			messages = append(messages, turn)
		}
	}
	messages = append(messages, store.Turn{Role: "user", Content: plan.Input})
	body := map[string]any{"model": remoteModelID(plan, model), "messages": messages, "max_tokens": generation.Tokens}
	if generation.Temperature != nil {
		body["temperature"] = *generation.Temperature
	}
	if generation.Reasoning != "default" {
		if contains(model.Parameters, "reasoning_effort") {
			effort := generation.Reasoning
			if effort == "off" {
				effort = "none"
			}
			body["reasoning_effort"] = effort
		} else {
			reasoning := map[string]any{"exclude": true}
			if generation.Reasoning == "off" {
				reasoning["enabled"] = false
			} else {
				reasoning["effort"] = generation.Reasoning
			}
			body["reasoning"] = reasoning
		}
	}
	b, err := p.request(ctx, "/chat/completions", key, body)
	if err != nil {
		return Result{}, err
	}
	var raw struct {
		Model   string `json:"model"`
		Choices []struct {
			Finish  string `json:"finish_reason"`
			Message struct {
				Content json.RawMessage `json:"content"`
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
		return Result{}, errors.New("provider_response_invalid")
	}
	var cost *float64
	if len(raw.Usage.Cost) > 0 && string(raw.Usage.Cost) != "null" {
		parsed, ok := compatibleNumber(raw.Usage.Cost)
		if !ok {
			return Result{}, errors.New("provider_usage_invalid")
		}
		cost = &parsed
	}
	result := Result{Model: raw.Model, PromptTokens: raw.Usage.Prompt, CompletionTokens: raw.Usage.Completion, ReasoningTokens: raw.Usage.Details.Reasoning, Cost: cost, Finish: raw.Choices[0].Finish}
	if result.Model == "" {
		result.Model = remoteModelID(plan, model)
	}
	if result.PromptTokens < 0 || result.CompletionTokens < 0 || result.ReasoningTokens < 0 || result.Cost != nil && (*result.Cost < 0 || math.IsNaN(*result.Cost) || math.IsInf(*result.Cost, 0)) {
		return Result{}, errors.New("provider_usage_invalid")
	}
	if json.Unmarshal(raw.Choices[0].Message.Content, &result.Text) != nil {
		var parts []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if json.Unmarshal(raw.Choices[0].Message.Content, &parts) != nil {
			return result, errors.New("provider_response_invalid")
		}
		for _, part := range parts {
			if part.Type == "text" {
				result.Text += part.Text
			}
		}
	}
	result.Text = strings.TrimSpace(result.Text)
	if result.Text == "" {
		if result.Finish == "length" {
			return result, errors.New("provider_token_limit_empty")
		}
		return result, errors.New("provider_empty_answer")
	}
	if result.Finish == "length" {
		result.Text += "\n[AI token limit reached; increase the budget or lower reasoning effort.]"
	}
	return result, nil
}

func (p *OpenAICompatible) Models(ctx context.Context) ([]Model, error) {
	b, err := p.request(ctx, "/models", p.key, nil)
	if err != nil {
		return nil, err
	}
	var raw struct {
		Data []struct {
			ID         string   `json:"id"`
			Name       string   `json:"name"`
			Context    int      `json:"context_length"`
			Parameters []string `json:"supported_parameters"`
			Reasoning  *struct {
				Mandatory bool            `json:"mandatory"`
				Efforts   json.RawMessage `json:"supported_efforts"`
			} `json:"reasoning"`
			Architecture struct {
				Input  []string `json:"input_modalities"`
				Output []string `json:"output_modalities"`
			} `json:"architecture"`
			Pricing struct {
				Prompt     json.RawMessage `json:"prompt"`
				Completion json.RawMessage `json:"completion"`
			} `json:"pricing"`
		} `json:"data"`
	}
	if json.Unmarshal(b, &raw) != nil || raw.Data == nil {
		return nil, errors.New("catalog_invalid")
	}
	models := make([]Model, 0, len(raw.Data))
	for _, v := range raw.Data {
		if v.ID == "" || len(v.ID) > 180 {
			continue
		}
		if len(v.Architecture.Input) > 0 && !contains(v.Architecture.Input, "text") || len(v.Architecture.Output) > 0 && !contains(v.Architecture.Output, "text") {
			continue
		}
		name := v.Name
		if name == "" {
			name = v.ID
		}
		parameters := append([]string{}, v.Parameters...)
		m := Model{ID: compatibleProviderID + ":" + v.ID, Name: name, Provider: compatibleProviderID, RemoteID: v.ID, ContextLength: v.Context, Parameters: parameters, ReasoningEfforts: []string{}}
		if contains(parameters, "reasoning_effort") || contains(parameters, "reasoning") {
			m.HasReasoning = true
			m.ReasoningEfforts = nil
		}
		if v.Reasoning != nil {
			m.HasReasoning = true
			m.ReasoningMandatory = v.Reasoning.Mandatory
			m.ReasoningEfforts = nil
			if string(v.Reasoning.Efforts) == "null" {
				m.ReasoningEfforts = nil
			} else if len(v.Reasoning.Efforts) > 0 && json.Unmarshal(v.Reasoning.Efforts, &m.ReasoningEfforts) != nil {
				return nil, errors.New("catalog_invalid")
			}
		}
		ip, promptKnown := compatibleNumber(v.Pricing.Prompt)
		op, completionKnown := compatibleNumber(v.Pricing.Completion)
		m.PricingKnown = promptKnown && completionKnown && ip >= 0 && op >= 0
		if m.PricingKnown {
			m.InputPrice = ip
			m.OutputPrice = op
		}
		models = append(models, m)
	}
	return models, nil
}
