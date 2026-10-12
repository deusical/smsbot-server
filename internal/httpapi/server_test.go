package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"smsbot-server/internal/bot"
	"smsbot-server/internal/store"
	"smsbot-server/internal/transport"
	"smsbot-server/internal/vault"
	"testing"
)

func apiSetup(t *testing.T) *Server {
	v, _ := vault.New(make([]byte, 32))
	s, e := store.Open(filepath.Join(t.TempDir(), "test.db"), v)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.DB.Close() })
	s.AddCredential("fixture-owner", "test", "owner", 0)
	s.AddCredential("fixture-client", "test", "client", 0)
	return New(bot.New(s, transport.NewFixture()), "http://localhost:8787", true)
}
func req(s *Server, method, path, body, token, origin string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://localhost:8787"+path, bytes.NewBufferString(body))
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}
func TestAuthAndHost(t *testing.T) {
	s := apiSetup(t)
	if w := req(s, "GET", "/api/v1/state", "", "", " "); w.Code != 401 {
		t.Fatal(w.Code)
	}
	if w := req(s, "GET", "/api/v1/state", "", "fixture-client", ""); w.Code != 200 {
		t.Fatal(w.Code)
	}
	r := httptest.NewRequest("GET", "http://evil.example/api/v1/state", nil)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("DNS rebind accepted")
	}
	if w := req(s, "POST", "/api/v1/pause", `{"paused":false}`, "fixture-client", "https://evil.example"); w.Code != 403 {
		t.Fatal("cross origin accepted")
	}
}
func TestClientCannotGrantCredentials(t *testing.T) {
	s := apiSetup(t)
	if w := req(s, "POST", "/api/v1/clients", `{"label":"test"}`, "fixture-client", ""); w.Code != 403 {
		t.Fatal(w.Code)
	}
}

func TestLoginCookieAndCSRF(t *testing.T) {
	s := apiSetup(t)
	w := req(s, "POST", "/api/v1/login", `{"token":"fixture-owner"}`, "", "http://localhost:8787")
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	cookie := w.Result().Cookies()[0]
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode {
		t.Fatal("cookie flags")
	}
	r := httptest.NewRequest("POST", "http://localhost:8787/api/v1/pause", bytes.NewBufferString(`{"paused":false}`))
	r.AddCookie(cookie)
	r.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("missing origin allowed")
	}
	r = httptest.NewRequest("POST", "http://localhost:8787/api/v1/pause", bytes.NewBufferString(`{"paused":false}`))
	r.AddCookie(cookie)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", s.Origin)
	w = httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
}

func TestRevokedBearerDoesNotFallBackToOwnerCookie(t *testing.T) {
	s := apiSetup(t)
	w := req(s, "POST", "/api/v1/login", `{"token":"fixture-owner"}`, "", s.Origin)
	cookie := w.Result().Cookies()[0]
	s.Engine.Store.Revoke(store.Hash("fixture-client"))
	r := httptest.NewRequest("GET", s.Origin+"/api/v1/state", nil)
	r.AddCookie(cookie)
	r.Header.Set("Authorization", "Bearer fixture-client")
	w = httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal("revoked client inherited owner session")
	}
}
func TestEnrollmentValidation(t *testing.T) {
	s := apiSetup(t)
	for _, body := range []string{`{"conversation_id":"demo-group","participant_id":"demo-peer"}`, `{"conversation_id":"demo-chat","participant_id":"wrong"}`, `{"conversation_id":"demo-chat","participant_id":"demo-peer","extra":true}`} {
		if w := req(s, "POST", "/api/v1/enrollment", body, "fixture-client", ""); w.Code != 400 {
			t.Fatal(w.Code)
		}
	}
	if w := req(s, "POST", "/api/v1/enrollment", `{"conversation_id":"demo-chat","participant_id":"demo-peer"}`, "fixture-client", ""); w.Code != 200 {
		t.Fatal(w.Code)
	}
}
func TestEndToEndFixture(t *testing.T) {
	s := apiSetup(t)
	req(s, "POST", "/api/v1/enrollment", `{"conversation_id":"demo-chat","participant_id":"demo-peer"}`, "fixture-client", "")
	req(s, "POST", "/api/v1/pause", `{"paused":false}`, "fixture-client", "")
	w := req(s, "POST", "/api/v1/fixture/incoming", `{"text":"!ping","conversation_id":"demo-chat","participant_id":"demo-peer"}`, "fixture-client", "")
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	if e := s.Engine.Dispatch(t.Context()); e != nil {
		t.Fatal(e)
	}
	w = req(s, "GET", "/api/v1/activity", "", "fixture-client", "")
	var jobs []store.Job
	json.Unmarshal(w.Body.Bytes(), &jobs)
	if len(jobs) != 1 || jobs[0].State != "submitted" {
		t.Fatal(w.Body.String())
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("cache enabled")
	}
}
func TestFixtureEndpointUnavailableInLiveMode(t *testing.T) {
	s := apiSetup(t)
	s.Fixture = false
	if w := req(s, "POST", "/api/v1/fixture/incoming", `{}`, "fixture-client", ""); w.Code != 404 {
		t.Fatal(w.Code)
	}
}
