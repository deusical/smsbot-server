package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"smsbot-server/internal/bot"
	"testing"
)

func TestConfigurationRecipeAndOfflineExecutionAPI(t *testing.T) {
	s := apiSetup(t)
	c := bot.DefaultConfiguration()
	c.Commands = []bot.Command{{Name: "sample", Type: "fixed", Enabled: true, Instructions: "Synthetic {input}"}}
	b, _ := json.Marshal(c)
	if w := req(s, "PUT", "/api/v1/config", string(b), "fixture-client", ""); w.Code != 200 {
		t.Fatal(w.Code)
	}
	w := req(s, "POST", "/api/v1/playground", `{"command":"sample","input":"preview"}`, "fixture-client", "")
	var preview map[string]any
	json.Unmarshal(w.Body.Bytes(), &preview)
	if w.Code != 200 || preview["text"] != "Synthetic preview" || preview["simulated"] != true {
		t.Fatal("playground contract")
	}
	w = req(s, "GET", "/api/v1/recipes/export", "", "fixture-client", "")
	var recipe bot.Configuration
	json.Unmarshal(w.Body.Bytes(), &recipe)
	if recipe.Commands[0].Enabled || len(recipe.Commands[0].AllowedConversations) > 0 {
		t.Fatal("recipe export restrictions")
	}
	b, _ = json.Marshal(map[string]any{"configuration": c})
	w = req(s, "POST", "/api/v1/recipes/import", string(b), "fixture-client", "")
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	saved, _ := s.Engine.Configuration()
	if saved.Commands[0].Enabled {
		t.Fatal("recipe imported enabled")
	}
	c.Commands[0].AllowedConversations = []string{"fixture-private-identity"}
	b, _ = json.Marshal(map[string]any{"configuration": c})
	if req(s, "POST", "/api/v1/recipes/import", string(b), "fixture-client", "").Code != 400 {
		t.Fatal("sender identity imported")
	}
}
func TestProviderKeyOwnerOnlyAndNoReadback(t *testing.T) {
	s := apiSetup(t)
	login := req(s, "POST", "/api/v1/login", `{"token":"fixture-owner"}`, "", "http://localhost:8787")
	cookie := login.Result().Cookies()[0]
	r := httptest.NewRequest("PUT", "http://localhost:8787/api/v1/provider-key", bytes.NewBufferString(`{"key":"SYNTHETIC_KEY"}`))
	r.AddCookie(cookie)
	r.Header.Set("Origin", "http://localhost:8787")
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 200 || bytes.Contains(w.Body.Bytes(), []byte("SYNTHETIC_KEY")) || !s.Engine.ProviderConfigured() {
		t.Fatal("owner key mutation failed or leaked key")
	}
	r = httptest.NewRequest("GET", "http://localhost:8787/api/v1/provider-key", nil)
	r.AddCookie(cookie)
	w = httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 200 || bytes.Contains(w.Body.Bytes(), []byte("SYNTHETIC_KEY")) {
		t.Fatal("key readback leaked")
	}
	if req(s, "POST", "/api/v1/models/refresh", "{}", "fixture-client", "").Code != 409 {
		t.Fatal("fixture contacted catalog")
	}
}
func TestCompatibleProviderConfigurationIsOwnerOnlyAndNeverReturnsKey(t *testing.T) {
	s := apiSetup(t)
	login := req(s, "POST", "/api/v1/login", `{"token":"fixture-owner"}`, "", "http://localhost:8787")
	cookie := login.Result().Cookies()[0]
	request := func(method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://localhost:8787"+path, bytes.NewBufferString(body))
		r.AddCookie(cookie)
		r.Header.Set("Origin", "http://localhost:8787")
		if body != "" {
			r.Header.Set("Content-Type", "application/json")
		}
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		return w
	}
	w := request("PUT", "/api/v1/compatible-provider", `{"base_url":"http://127.0.0.1:8317/v1","key":"SYNTHETIC_KEY"}`)
	if w.Code != 200 || bytes.Contains(w.Body.Bytes(), []byte("SYNTHETIC_KEY")) || !bytes.Contains(w.Body.Bytes(), []byte(`"configured":true`)) {
		t.Fatalf("compatible provider save leaked or failed: %d %s", w.Code, w.Body.String())
	}
	w = request("GET", "/api/v1/compatible-provider", "")
	if w.Code != 200 || bytes.Contains(w.Body.Bytes(), []byte("SYNTHETIC_KEY")) {
		t.Fatal("compatible provider status returned the saved key")
	}
	w = request("PUT", "/api/v1/compatible-provider", `{"base_url":"http://gateway.example.test/v1","key":"SYNTHETIC_KEY"}`)
	if w.Code != 400 || bytes.Contains(w.Body.Bytes(), []byte("SYNTHETIC_KEY")) {
		t.Fatal("insecure external endpoint accepted or echoed")
	}
}
func TestHistoryImportNeverCreatesJobs(t *testing.T) {
	s := apiSetup(t)
	if req(s, "POST", "/api/v1/conversations/demo-chat/history/import", `{"cursor":""}`, "fixture-client", "").Code != 409 {
		t.Fatal("unenrolled history imported")
	}
	s.Engine.Enroll("demo-chat", "demo-peer")
	w := req(s, "POST", "/api/v1/conversations/demo-chat/history/import", `{"cursor":""}`, "fixture-client", "")
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	jobs, err := s.Engine.Store.Jobs()
	if err != nil || len(jobs) != 0 {
		t.Fatal("historical command executed")
	}
	messages, err := s.Engine.Store.Messages("demo-chat")
	if err != nil || len(messages) != 1 {
		t.Fatal("history not retained")
	}
	w = req(s, "GET", "/api/v1/conversations/demo-chat/history?limit=50", "", "fixture-client", "")
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
}
func TestNativeJSONMediaTypeSupportsUTF8Charset(t *testing.T) {
	s := apiSetup(t)
	for _, contentType := range []string{"application/json; charset=utf-8", "application/json; charset=UTF-8"} {
		r := httptest.NewRequest("POST", "http://localhost:8787/api/v1/pause", bytes.NewBufferString(`{"paused":true}`))
		r.AddCookie(&http.Cookie{Name: "smsbot_session", Value: "fixture-client"})
		r.Header.Set("Origin", "http://localhost:8787")
		r.Header.Set("Content-Type", contentType)
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatalf("native JSON content type rejected: %d", w.Code)
		}
	}
}
