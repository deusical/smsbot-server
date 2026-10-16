package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"smsbot-server/internal/transport"
	"strings"
	"testing"
	"time"
)

func TestInboxOwnerOnlyAndLegacyVisibility(t *testing.T) {
	s := apiSetup(t)
	for _, path := range []string{"/api/v1/inbox/settings", "/api/v1/inbox/conversations", "/api/v1/inbox/conversations/demo-chat/history"} {
		if w := req(s, "GET", path, "", "fixture-client", ""); w.Code != 403 {
			t.Fatal("client inbox access", w.Code)
		}
	}
	cfg, _ := s.Engine.InboxSettings()
	cfg.Enabled = true
	s.Engine.SetInboxSettings(context.Background(), cfg)
	s.Engine.Store.Enroll("demo-chat", "demo-peer")
	s.Engine.Observe(context.Background(), transport.Incoming{ID: "synthetic-message", ConversationID: "demo-chat", ParticipantID: "demo-peer", Text: "Synthetic private message", At: time.Now()})
	if w := req(s, "GET", "/api/v1/conversations/demo-chat/history", "", "fixture-client", ""); w.Code != 200 || strings.Contains(w.Body.String(), "Synthetic private message") {
		t.Fatal("private text exposed on legacy history")
	}
	if w := ownerRequest(s, "GET", "/api/v1/inbox/conversations/demo-chat/history", ""); w.Code != 200 || !strings.Contains(w.Body.String(), "Synthetic private message") {
		t.Fatal("owner inbox missing", w.Code)
	}
}
func ownerRequest(s *Server, method, path, body string) *httptest.ResponseRecorder {
	s.Engine.Store.AddCredential("synthetic-session", "test", "session", 0)
	r := httptest.NewRequest(method, "http://localhost:8787"+path, strings.NewReader(body))
	r.AddCookie(&http.Cookie{Name: "smsbot_session", Value: "synthetic-session"})
	r.Header.Set("Origin", "http://localhost:8787")
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}
func TestInboxSettingsOptimisticSave(t *testing.T) {
	s := apiSetup(t)
	cfg, _ := s.Engine.InboxSettings()
	cfg.Enabled = true
	b, _ := json.Marshal(cfg)
	if w := ownerRequest(s, "PUT", "/api/v1/inbox/settings", string(b)); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := ownerRequest(s, "PUT", "/api/v1/inbox/settings", string(b)); w.Code != 409 {
		t.Fatal("stale update accepted")
	}
}

func TestOwnerComposeUnicodeLimit(t *testing.T) {
	s := apiSetup(t)
	cfg, _ := s.Engine.InboxSettings()
	cfg.Enabled = true
	if err := s.Engine.SetInboxSettings(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	if err := s.Engine.Pause(false); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]string{"text": strings.Repeat("?", 12000), "request_id": "synthetic-unicode-request"})
	if w := ownerRequest(s, "POST", "/api/v1/inbox/conversations/demo-chat/compose", string(body)); w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
	body, _ = json.Marshal(map[string]string{"text": strings.Repeat("?", 12001), "request_id": "synthetic-over-limit-request"})
	if w := ownerRequest(s, "POST", "/api/v1/inbox/conversations/demo-chat/compose", string(body)); w.Code == 202 {
		t.Fatal("over-limit compose accepted")
	}
}

func TestInboxWithoutArchiveKeepsSnapshotPolling(t *testing.T) {
	s := apiSetup(t)
	cfg, err := s.Engine.InboxSettings()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Enabled = true
	if err := s.Engine.SetInboxSettings(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Engine.Store.Archive("demo-chat"); err == nil {
		t.Fatal("test requires no archive record")
	}
	path := "/api/v1/inbox/conversations/demo-chat/history"
	w := ownerRequest(s, "GET", path, "")
	if w.Code != 200 {
		t.Fatal("snapshot unavailable", w.Code)
	}
	var page map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if _, exists := page["change_cursor"]; exists {
		t.Fatal("snapshot advertises unavailable archive feed")
	}
	// A subsequent snapshot must include newly retained messages.
	if err := s.Engine.Store.SaveObservedFiles("demo-chat", "new-snapshot-message", "", "incoming", "Synthetic latest message", "phone", "", time.Now(), nil); err != nil {
		t.Fatal(err)
	}
	w = ownerRequest(s, "GET", path, "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Synthetic latest message") {
		t.Fatal("latest snapshot message missing", w.Code)
	}
}

func TestViewedRefreshOwnerConsentAndRangeValidation(t *testing.T) {
	s := apiSetup(t)
	path := "/api/v1/inbox/conversations/demo-chat/refresh"
	body := `{"since":"2026-01-01T00:00:00Z","until":"2026-01-02T00:00:00Z"}`
	if w := req(s, "POST", path, body, "fixture-client", "http://localhost:8787"); w.Code != 403 {
		t.Fatal("client requested private sync", w.Code)
	}
	if w := ownerRequest(s, "POST", path, body); w.Code != 409 {
		t.Fatal("sync enabled without consent", w.Code)
	}
	cfg, _ := s.Engine.InboxSettings()
	cfg.Enabled = true
	if err := s.Engine.SetInboxSettings(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	if w := ownerRequest(s, "POST", path, body); w.Code != 202 {
		t.Fatal("valid refresh rejected", w.Code)
	}
	for _, body := range []string{`{"since":"","until":""}`, `{"since":"2026-01-02T00:00:00Z","until":"2026-01-01T00:00:00Z"}`, `{"since":"2026-01-01T00:00:00Z","until":"2099-01-01T00:00:00Z"}`} {
		if w := ownerRequest(s, "POST", path, body); w.Code != 409 {
			t.Fatal("invalid range accepted", w.Code)
		}
	}
}
