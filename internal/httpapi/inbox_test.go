package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/deusical/smsbot-server/internal/store"
	"github.com/deusical/smsbot-server/internal/transport"
)

func TestInboxOwnerOnlyAndLegacyVisibility(t *testing.T) {
	t.Parallel()
	s := apiSetup(t)
	cfg, _ := s.Collection.Settings()
	cfg.Enabled = true
	s.Collection.SetSettings(context.Background(), cfg)
	s.Store.Enroll("demo-chat", "demo-peer")
	s.Collection.Observe(context.Background(), transport.Incoming{ID: "synthetic-message", ConversationID: "demo-chat", ParticipantID: "demo-peer", Text: "Synthetic private message", At: time.Now()})
	if w := req(s, "GET", "/api/v1/conversations/demo-chat/history", "", "fixture-client", ""); w.Code != 200 || strings.Contains(w.Body.String(), "Synthetic private message") {
		t.Fatal("private text exposed on legacy history")
	}
	if w := ownerRequest(s, "GET", "/api/v1/inbox/conversations/demo-chat/history", ""); w.Code != 200 || !strings.Contains(w.Body.String(), "Synthetic private message") {
		t.Fatal("owner inbox missing", w.Code)
	}
}
func ownerRequest(s *Server, method, path, body string) *httptest.ResponseRecorder {
	s.Store.AddCredential("synthetic-session", "test", "session", 0)
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
	t.Parallel()
	s := apiSetup(t)
	cfg, _ := s.Collection.Settings()
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
	t.Parallel()
	s := apiSetup(t)
	cfg, _ := s.Collection.Settings()
	cfg.Enabled = true
	if err := s.Collection.SetSettings(context.Background(), cfg); err != nil {
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
	t.Parallel()
	s := apiSetup(t)
	cfg, err := s.Collection.Settings()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Enabled = true
	if err := s.Collection.SetSettings(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Store.Archive("demo-chat"); err == nil {
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
	if err := s.Store.SaveObservedFiles("demo-chat", "new-snapshot-message", "", "incoming", "Synthetic latest message", "phone", "", time.Now(), nil); err != nil {
		t.Fatal(err)
	}
	w = ownerRequest(s, "GET", path, "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Synthetic latest message") {
		t.Fatal("latest snapshot message missing", w.Code)
	}
}

func TestViewedRefreshOwnerConsentAndRangeValidation(t *testing.T) {
	t.Parallel()
	s := apiSetup(t)
	path := "/api/v1/inbox/conversations/demo-chat/refresh"
	body := `{"since":"2026-01-01T00:00:00Z","until":"2026-01-02T00:00:00Z"}`
	if w := ownerRequest(s, "POST", path, body); w.Code != 409 {
		t.Fatal("sync enabled without consent", w.Code)
	}
	cfg, _ := s.Collection.Settings()
	cfg.Enabled = true
	if err := s.Collection.SetSettings(context.Background(), cfg); err != nil {
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

func TestInboxConversationsIdentityChanged(t *testing.T) {
	t.Parallel()
	s := apiSetup(t)
	cfg, err := s.Collection.Settings()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Enabled = true
	if err := s.Collection.SetSettings(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	// Archive demo-chat under an old participant ID
	oldConv := transport.Conversation{
		ID:            "demo-chat",
		ParticipantID: "old-stale-peer",
		Label:         "Demo conversation",
		Protocol:      "RCS",
		Writable:      true,
	}
	if _, err := s.Store.UpdateArchive("demo-chat", 30, func(a *store.Archive, exists bool) error {
		*a = store.NewArchive(oldConv)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// Enroll demo-chat under old participant ID
	if err := s.Store.Enroll("demo-chat", "old-stale-peer"); err != nil {
		t.Fatal(err)
	}

	w := ownerRequest(s, "GET", "/api/v1/inbox/conversations", "")
	if w.Code != 200 {
		t.Fatal("inbox conversations failed", w.Code)
	}
	var page transport.ConversationPage
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	var demo *transport.Conversation
	for i := range page.Conversations {
		if page.Conversations[i].ID == "demo-chat" {
			demo = &page.Conversations[i]
			break
		}
	}
	if demo == nil {
		t.Fatal("demo-chat missing from inbox conversations")
	}
	if !demo.IdentityChanged {
		t.Fatal("expected identity_changed to be true")
	}
	if demo.Writable {
		t.Fatal("expected writable to be false")
	}
	if !demo.Archived {
		t.Fatal("expected archived to be true")
	}
	if demo.Enrolled {
		t.Fatal("expected enrolled to be false due to participant mismatch")
	}
	if demo.ParticipantID != "demo-peer" {
		t.Fatalf("expected live participant ID 'demo-peer', got %q", demo.ParticipantID)
	}

	// Verify that adjacent conversations without identity changes are completely unaffected
	for _, c := range page.Conversations {
		if c.ID != "demo-chat" {
			if c.IdentityChanged {
				t.Fatalf("conversation %s unexpectedly flagged with identity_changed", c.ID)
			}
			if !c.Writable {
				t.Fatalf("conversation %s unexpectedly marked non-writable", c.ID)
			}
		}
	}

	// Also verify that requesting a refresh for this mismatched conversation returns 409 archive_identity_changed
	refreshBody := `{"since":"2026-01-01T00:00:00Z","until":"2026-01-02T00:00:00Z"}`
	refW := ownerRequest(s, "POST", "/api/v1/inbox/conversations/demo-chat/refresh", refreshBody)
	if refW.Code != 409 || !strings.Contains(refW.Body.String(), "archive_identity_changed") {
		t.Fatalf("expected 409 archive_identity_changed, got %d %s", refW.Code, refW.Body.String())
	}
}
