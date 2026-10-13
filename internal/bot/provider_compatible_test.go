package bot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"smsbot-server/internal/store"
)

func TestCompatibleProviderModelsAndGeneration(t *testing.T) {
	var request map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer SYNTHETIC_KEY" {
			t.Error("authorization missing")
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/models":
			w.Write([]byte(`{"data":[{"id":"vendor/model","name":"Synthetic model","context_length":8192,"supported_parameters":["temperature"],"pricing":{"prompt":"0.000001","completion":"0.000002"}},{"id":"vendor/numeric-prices","pricing":{"prompt":0.000003,"completion":0.000004}}]}`))
		case "/v1/chat/completions":
			if r.Method != http.MethodPost {
				t.Errorf("generation method = %s", r.Method)
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Error(err)
			}
			w.Write([]byte(`{"model":"vendor/model-alias","choices":[{"message":{"content":[{"type":"text","text":"Synthetic "},{"type":"text","text":"answer"}]},"finish_reason":"stop"}],"usage":{"prompt_tokens":12,"completion_tokens":8,"completion_tokens_details":{"reasoning_tokens":0},"cost":0.001}}`))
		default:
			t.Errorf("unexpected provider path %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	p := NewOpenAICompatible(server.URL+"/v1", "SYNTHETIC_KEY")
	models, err := p.Models(context.Background())
	if err != nil || len(models) != 2 {
		t.Fatalf("models=%v error=%v", models, err)
	}
	if !models[1].PricingKnown || models[1].InputPrice != 0.000003 || models[1].OutputPrice != 0.000004 {
		t.Fatalf("numeric pricing metadata was not parsed: %+v", models[1])
	}
	model := models[0]
	if model.ID != "compatible:vendor/model" || model.Provider != "compatible" || model.RemoteID != "vendor/model" || model.Name != "Synthetic model" || !model.PricingKnown || model.Parameters[0] != "temperature" {
		t.Fatalf("unexpected compatible model: %+v", model)
	}
	if p.client.Timeout != 95*time.Second {
		t.Fatalf("compatible provider timeout = %v", p.client.Timeout)
	}
	g := DefaultGeneration()
	temperature := 0.3
	g.Temperature = &temperature
	generation, _ := json.Marshal(g)
	result, err := p.Generate(context.Background(), "SYNTHETIC_KEY", store.Plan{Model: model.ID, Generation: generation, Input: "Synthetic input", Instructions: "Synthetic instructions"}, []store.Turn{{Role: "user", Content: "Synthetic history"}, {Role: "tool", Content: "Excluded"}}, model)
	if err != nil || result.Text != "Synthetic answer" || result.Model != "vendor/model-alias" || result.PromptTokens != 12 || result.CompletionTokens != 8 || result.Cost == nil || *result.Cost != 0.001 {
		t.Fatalf("result=%+v error=%v", result, err)
	}
	if request["model"] != "vendor/model" || request["max_tokens"] != float64(2048) || request["temperature"] != 0.3 {
		t.Fatal("compatible request did not retain model and generation settings")
	}
	if _, ok := request["provider"]; ok {
		t.Fatal("OpenRouter-only provider constraint was sent to the compatible endpoint")
	}
	if len(request["messages"].([]any)) != 3 {
		t.Fatal("non-conversation history role was sent")
	}
}

func TestCompatibleProviderSettingsAreValidatedAndSecret(t *testing.T) {
	e, _ := setup(t)
	status, err := e.CompatibleProviderStatus()
	if err != nil || status.Configured || status.BaseURL != defaultCompatibleURL {
		t.Fatalf("default status=%+v error=%v", status, err)
	}
	if err = e.SetCompatibleProvider("http://127.0.0.1:8317/v1", "SYNTHETIC_KEY", false); err != nil {
		t.Fatal(err)
	}
	status, err = e.CompatibleProviderStatus()
	if err != nil || !status.Configured || status.BaseURL != "http://127.0.0.1:8317/v1" {
		t.Fatalf("saved status=%+v error=%v", status, err)
	}
	if strings.Contains(strings.ToLower(strings.Join([]string{status.BaseURL}, " ")), "synthetic_key") {
		t.Fatal("provider status exposed the key")
	}
	var stored string
	if err = e.Store.DB.QueryRow("SELECT value FROM settings WHERE key=?", compatibleSettingKey).Scan(&stored); err != nil || strings.Contains(stored, "SYNTHETIC_KEY") {
		t.Fatal("provider key was not stored as ciphertext")
	}
	if err = e.SetCompatibleProvider("https://gateway.example.test/v1", "", false); err != nil {
		t.Fatal(err)
	}
	if !e.CompatibleProviderConfigured() {
		t.Fatal("blank key update did not preserve the saved key")
	}
	if err = e.SetCompatibleProvider("https://gateway.example.test/v1", "", true); err != nil || e.CompatibleProviderConfigured() {
		t.Fatal("key removal failed")
	}
	for _, invalid := range []string{
		"http://gateway.example.test/v1",
		"https://user:pass@gateway.example.test/v1",
		"https://gateway.example.test/v1?key=SYNTHETIC_KEY",
		"file:///tmp/provider",
	} {
		if _, err := normalizeCompatibleBaseURL(invalid); err == nil {
			t.Errorf("accepted invalid endpoint %q", invalid)
		}
	}
}

func TestFixtureCannotRefreshCompatibleCatalog(t *testing.T) {
	requests := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests <- struct{}{}
		w.Write([]byte(`{"data":[]}`))
	}))
	defer server.Close()
	e, _ := setup(t)
	if err := e.SetCompatibleProvider(server.URL+"/v1", "SYNTHETIC_KEY", false); err != nil {
		t.Fatal(err)
	}
	if _, err := e.RefreshCompatibleModels(context.Background()); err == nil || err.Error() != "fixture_catalog_is_offline" {
		t.Fatalf("fixture catalog refresh error = %v", err)
	}
	select {
	case <-requests:
		t.Fatal("fixture provider made an HTTP request")
	default:
	}
}

func TestCompatibleCatalogRefreshPreservesOtherProviders(t *testing.T) {
	e, _ := setup(t)
	models := []Model{
		{ID: "openai/test", Name: "OpenRouter model", Provider: "openrouter", RemoteID: "openai/test"},
		{ID: "compatible:old", Name: "Old compatible model", Provider: "compatible", RemoteID: "old"},
	}
	if err := e.Store.SetPublicSetting("model_catalog", models); err != nil {
		t.Fatal(err)
	}
	if _, err := e.replaceProviderModels("compatible", []Model{{ID: "compatible:new", Name: "New compatible model", Provider: "compatible", RemoteID: "new"}}); err != nil {
		t.Fatal(err)
	}
	got, err := e.Models()
	if err != nil || len(got) != 2 || got[0].Provider != "openrouter" || got[1].ID != "compatible:new" {
		t.Fatalf("catalog merge lost provider models: %+v error=%v", got, err)
	}
}

func TestDispatchRoutesNamespacedModelThroughCompatibleProvider(t *testing.T) {
	providerCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer SYNTHETIC_KEY" {
			t.Errorf("unexpected request path or auth: %s", r.URL.Path)
		}
		providerCalls++
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"model":"synthetic/model","choices":[{"message":{"content":"Synthetic answer"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":4,"cost":0.001}}`))
	}))
	defer server.Close()
	e, fixture := setup(t)
	e.Provider = NewOpenRouter()
	arm(t, e)
	if err := e.SetCompatibleProvider(server.URL+"/v1", "SYNTHETIC_KEY", false); err != nil {
		t.Fatal(err)
	}
	model := Model{ID: "compatible:synthetic/model", Name: "Synthetic model", Provider: "compatible", RemoteID: "synthetic/model", PricingKnown: true, InputPrice: 0.000001, OutputPrice: 0.000001, Parameters: []string{}, ReasoningEfforts: []string{}}
	if err := e.Store.SetPublicSetting("model_catalog", []Model{model}); err != nil {
		t.Fatal(err)
	}
	c := DefaultConfiguration()
	c.Settings.DefaultModel = model.ID
	c.Commands = []Command{{Name: "ask", Type: "ai", Enabled: true, Instructions: "Synthetic instructions"}}
	if err := e.SetConfiguration(c); err != nil {
		t.Fatal(err)
	}
	incoming := incoming()
	incoming.Text = "!ask Synthetic request"
	accepted, err := e.Accept(context.Background(), incoming)
	if err != nil || !accepted {
		t.Fatalf("accepted=%t error=%v", accepted, err)
	}
	if err = e.Dispatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	submitted := false
	for _, text := range fixture.Sent {
		submitted = submitted || text == "Synthetic answer"
	}
	if providerCalls != 1 || !submitted || len(fixture.Sent) != 1 {
		t.Fatal("compatible model was not generated and submitted through fixture transport")
	}
}

func TestCompatibleProviderFailureNeverSubmits(t *testing.T) {
	providerCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		providerCalls++
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("unexpected provider path %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte("SYNTHETIC_PRIVATE_PROVIDER_BODY"))
	}))
	defer server.Close()
	e, fixture := setup(t)
	e.Provider = NewOpenRouter()
	arm(t, e)
	if err := e.SetCompatibleProvider(server.URL+"/v1", "SYNTHETIC_KEY", false); err != nil {
		t.Fatal(err)
	}
	model := Model{ID: "compatible:synthetic/model", Name: "Synthetic model", Provider: "compatible", RemoteID: "synthetic/model", PricingKnown: true, Parameters: []string{}, ReasoningEfforts: []string{}}
	if err := e.Store.SetPublicSetting("model_catalog", []Model{model}); err != nil {
		t.Fatal(err)
	}
	c := DefaultConfiguration()
	c.Settings.DefaultModel = model.ID
	c.Commands = []Command{{Name: "ask", Type: "ai", Enabled: true, Instructions: "Synthetic instructions"}}
	if err := e.SetConfiguration(c); err != nil {
		t.Fatal(err)
	}
	incoming := incoming()
	incoming.Text = "!ask Synthetic request"
	accepted, err := e.Accept(context.Background(), incoming)
	if err != nil || !accepted {
		t.Fatalf("accepted=%t error=%v", accepted, err)
	}
	if err = e.Dispatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	jobID := store.Hash(incoming.ConversationID + "\x00" + incoming.ID)
	if providerCalls != 1 || len(fixture.Sent) != 0 || stateOf(t, e, jobID) != "failed" {
		t.Fatal("provider failure submitted a reply or left the job runnable")
	}
	var code string
	if err = e.Store.DB.QueryRow("SELECT code FROM jobs WHERE id=?", jobID).Scan(&code); err != nil || code != "provider_http_401" {
		t.Fatalf("provider failure code leaked or was incorrect: %q, %v", code, err)
	}
}

func TestCompatibleProviderErrorsDoNotEchoBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte("SYNTHETIC_PRIVATE_PROVIDER_BODY"))
	}))
	defer server.Close()
	p := NewOpenAICompatible(server.URL+"/v1", "SYNTHETIC_KEY")
	_, err := p.Models(context.Background())
	if err == nil || err.Error() != "provider_http_401" || strings.Contains(err.Error(), "PRIVATE_PROVIDER") {
		t.Fatalf("unsafe provider error: %v", err)
	}
}

func TestCompatibleCatalogHonorsCallerCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer server.Close()
	p := NewOpenAICompatible(server.URL+"/v1", "SYNTHETIC_KEY")
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := p.Models(ctx)
	if err == nil || err.Error() != "provider_connection_failed" || time.Since(started) > time.Second {
		t.Fatalf("compatible catalog did not honor deadline: error=%v elapsed=%s", err, time.Since(started))
	}
}
