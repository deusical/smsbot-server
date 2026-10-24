package httpapi

import (
	"bytes"
	"encoding/json"
	"github.com/deusical/smsbot-server/internal/app"
	"github.com/deusical/smsbot-server/internal/store"
	"github.com/deusical/smsbot-server/internal/transport"
	"github.com/deusical/smsbot-server/internal/vault"
	"net/http"
	"net/http/httptest"
	"path/filepath"
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
	s.AddCredential("fixture-client", "test", "session", 0)
	service := app.New(s, transport.NewFixture(), filepath.Join(filepath.Dir(s.Path), "archive"))
	if err := service.Init(); err != nil {
		t.Fatal(err)
	}
	return New(service.Engine, service.Collection, "http://localhost:8787", true)
}
func req(s *Server, method, path, body, token, origin string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://localhost:8787"+path, bytes.NewBufferString(body))
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		r.AddCookie(&http.Cookie{Name: "smsbot_session", Value: token})
	}
	if origin == "" && method != "GET" {
		origin = "http://localhost:8787"
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
func TestClientCredentialEndpointsAreGone(t *testing.T) {
	s := apiSetup(t)
	for _, c := range []struct{ method, path string }{{"POST", "/api/v1/clients"}, {"GET", "/api/v1/clients"}, {"DELETE", "/api/v1/clients/x"}, {"GET", "/api/v1/conversations"}} {
		if w := req(s, c.method, c.path, "", "fixture-client", ""); w.Code < 400 {
			t.Fatal(c.method, c.path, w.Code)
		}
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

func TestFailedLoginsDoNotLockOutAndWhitespaceIsTrimmed(t *testing.T) {
	s := apiSetup(t)
	for i := 0; i < 10; i++ {
		if w := req(s, "POST", "/api/v1/login", `{"token":"wrong"}`, "", s.Origin); w.Code != 401 {
			t.Fatal(w.Code)
		}
	}
	if w := req(s, "POST", "/api/v1/login", `{"token":" fixture-owner\r\n"}`, "", s.Origin); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestRememberedLoginNeverExpiresAndRevokesEverywhere(t *testing.T) {
	s := apiSetup(t)
	short := req(s, "POST", "/api/v1/login", `{"token":"fixture-owner"}`, "", s.Origin).Result().Cookies()[0]
	long := req(s, "POST", "/api/v1/login", `{"token":"fixture-owner","remember":true}`, "", s.Origin).Result().Cookies()[0]
	if short.MaxAge != 12*3600 || long.MaxAge != 400*24*3600 {
		t.Fatal(short.MaxAge, long.MaxAge)
	}
	if expires, ok := s.Engine.Store.SessionExpiry(long.Value); !ok || expires != 0 {
		t.Fatal("remembered session expires", expires)
	}
	get := func(c *http.Cookie) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", s.Origin+"/api/v1/state", nil)
		r.AddCookie(c)
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		return w
	}
	// Use renews only the remembered cookie.
	if w := get(long); w.Code != 200 || len(w.Result().Cookies()) != 1 || w.Result().Cookies()[0].MaxAge != 400*24*3600 {
		t.Fatal(w.Code, w.Result().Cookies())
	}
	if w := get(short); w.Code != 200 || len(w.Result().Cookies()) != 0 {
		t.Fatal(w.Code, w.Result().Cookies())
	}
	// Signing out from a remembered session must not also renew its cookie.
	r := httptest.NewRequest("POST", s.Origin+"/api/v1/sessions/revoke", nil)
	r.AddCookie(long)
	r.Header.Set("Origin", s.Origin)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if cs := w.Result().Cookies(); w.Code != 200 || len(cs) != 1 || cs[0].MaxAge != -1 {
		t.Fatal(w.Code, cs)
	}
	if get(long).Code != 401 || get(short).Code != 401 {
		t.Fatal("sessions survived revoke")
	}
}

func TestInstallableAppShellHeaders(t *testing.T) {
	s := apiSetup(t)
	w := req(s, "GET", "/manifest.webmanifest", "", "", "")
	if w.Code != 200 || w.Header().Get("Content-Type") != "application/manifest+json" || w.Header().Get("Cache-Control") != "no-cache" {
		t.Fatal(w.Code, w.Header())
	}
	if w := req(s, "GET", "/sw.js", "", "", ""); w.Code != 200 || w.Header().Get("Cache-Control") != "no-cache" {
		t.Fatal(w.Code, w.Header())
	}
	if w := req(s, "GET", "/api/v1/state", "", "fixture-client", ""); w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal(w.Header())
	}
}

func TestBearerCredentialsAreNeverHonoured(t *testing.T) {
	s := apiSetup(t)
	w := req(s, "POST", "/api/v1/login", `{"token":"fixture-owner"}`, "", s.Origin)
	cookie := w.Result().Cookies()[0]
	// A bearer header alone never authenticates, even with a known credential value.
	r := httptest.NewRequest("GET", s.Origin+"/api/v1/state", nil)
	r.Header.Set("Authorization", "Bearer fixture-client")
	w = httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal("bearer header authenticated", w.Code)
	}
	// And it does not interfere with a valid browser session.
	r = httptest.NewRequest("GET", s.Origin+"/api/v1/state", nil)
	r.AddCookie(cookie)
	r.Header.Set("Authorization", "Bearer anything")
	w = httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal("bearer header broke the session", w.Code)
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
