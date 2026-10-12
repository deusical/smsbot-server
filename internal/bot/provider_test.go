package bot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"smsbot-server/internal/store"
	"strings"
	"testing"
)

func TestProviderRequestAndUsageContracts(t *testing.T) {
	var request map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer SYNTHETIC_KEY" {
			t.Error("authorization missing")
		}
		json.NewDecoder(r.Body).Decode(&request)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"model":"fixture/model","choices":[{"message":{"content":[{"type":"text","text":"Synthetic answer"}]},"finish_reason":"length"}],"usage":{"prompt_tokens":12,"completion_tokens":20,"completion_tokens_details":{"reasoning_tokens":10},"cost":0.01}}`))
	}))
	defer server.Close()
	p := &OpenRouter{client: server.Client(), base: server.URL}
	g := DefaultGeneration()
	g.Reasoning = "high"
	temperature := 0.3
	g.Temperature = &temperature
	b, _ := json.Marshal(g)
	result, err := p.Generate(context.Background(), "SYNTHETIC_KEY", store.Plan{Model: "fixture/model", Generation: b, Input: "Synthetic input", Instructions: "Synthetic instructions"}, []store.Turn{{Role: "user", Content: "Synthetic prior"}, {Role: "tool", Content: "Excluded synthetic tool"}}, Model{HasReasoning: true, ReasoningEfforts: nil, Parameters: []string{"temperature"}})
	if err != nil || result.ReasoningTokens != 10 || result.Cost == nil || *result.Cost != 0.01 || !strings.Contains(result.Text, "token limit") {
		t.Fatalf("result contract failed %v", err)
	}
	if request["model"] != "fixture/model" || request["max_tokens"] != float64(2048) || request["provider"].(map[string]any)["require_parameters"] != true || request["reasoning"].(map[string]any)["exclude"] != true {
		t.Fatal("parameter enforcement missing")
	}
	if len(request["messages"].([]any)) != 3 {
		t.Fatal("unapproved history role sent")
	}
}
func TestProviderDefaultAndFailures(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		want       string
	}{
		{"empty-length", `{"choices":[{"message":{"content":null},"finish_reason":"length"}],"usage":{"cost":0.01}}`, 200, "provider_token_limit_empty"},
		{"negative-cost", `{"choices":[{"message":{"content":"Synthetic"}}],"usage":{"cost":-1}}`, 200, "provider_usage_invalid"},
		{"bad-json", `not-json`, 200, "provider_response_invalid"},
		{"authentication", `SYNTHETIC_SECRET_DETAIL`, 401, "provider_http_401"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				json.NewDecoder(r.Body).Decode(&body)
				if _, ok := body["reasoning"]; ok {
					t.Error("default reasoning was explicitly sent")
				}
				if _, ok := body["temperature"]; ok {
					t.Error("default temperature was explicitly sent")
				}
				w.WriteHeader(tc.status)
				w.Write([]byte(tc.body))
			}))
			defer server.Close()
			p := &OpenRouter{client: server.Client(), base: server.URL}
			b, _ := json.Marshal(DefaultGeneration())
			_, err := p.Generate(context.Background(), "SYNTHETIC_KEY", store.Plan{Model: "fixture/model", Generation: b}, nil, Model{})
			if err == nil || err.Error() != tc.want {
				t.Fatalf("error %v", err)
			}
		})
	}
}
func TestUnsupportedGenerationNeverContactsProvider(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls++ }))
	defer server.Close()
	p := &OpenRouter{client: server.Client(), base: server.URL}
	g := DefaultGeneration()
	g.Reasoning = "off"
	b, _ := json.Marshal(g)
	if _, err := p.Generate(context.Background(), "SYNTHETIC_KEY", store.Plan{Generation: b}, nil, Model{HasReasoning: true, ReasoningMandatory: true}); err == nil {
		t.Fatal("mandatory reasoning disabled")
	}
	temperature := 0.5
	g = DefaultGeneration()
	g.Temperature = &temperature
	b, _ = json.Marshal(g)
	if _, err := p.Generate(context.Background(), "SYNTHETIC_KEY", store.Plan{Generation: b}, nil, Model{}); err == nil {
		t.Fatal("unsupported temperature accepted")
	}
	if calls != 0 {
		t.Fatal("invalid controls contacted provider")
	}
}
func TestModelCatalogReasoningNullAndAbsent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":[{"id":"fixture/all","name":"All","architecture":{"input_modalities":["text"],"output_modalities":["text"]},"reasoning":{"supported_efforts":null},"pricing":{"prompt":"0.1","completion":"0.2"}},{"id":"fixture/none","name":"None","architecture":{"input_modalities":["text"],"output_modalities":["text"]},"reasoning":{}}]}`))
	}))
	defer server.Close()
	p := &OpenRouter{client: server.Client(), base: server.URL}
	models, err := p.Models(context.Background())
	if err != nil || len(models) != 2 || models[0].ReasoningEfforts != nil || models[1].ReasoningEfforts == nil || !models[0].PricingKnown || models[1].PricingKnown {
		t.Fatal("model capabilities incorrectly inferred")
	}
}
