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
