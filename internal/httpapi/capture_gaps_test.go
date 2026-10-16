package httpapi

import (
	"net/http"
	"net/http/httptest"
	"smsbot-server/internal/store"
	"strings"
	"testing"
)

func TestCaptureGapDetailsAreCookieOwnerOnlyAndHideCheckpoints(t *testing.T) {
	s := apiSetup(t)
	if err := s.Engine.Store.SaveCaptureGap(store.CaptureGap{Conversation: "synthetic-private-conversation", Since: "2026-10-16T12:00:00Z", Until: "2026-10-16T12:01:00Z", Reason: "capture_delayed", State: "queued", Cursor: "synthetic-private-cursor", Generation: 7}); err != nil {
		t.Fatal(err)
	}
	client := req(s, "GET", "/api/v1/state", "", "fixture-client", "")
	if strings.Contains(client.Body.String(), "inbox_capture_gap") || strings.Contains(client.Body.String(), "synthetic-private") {
		t.Fatal("client received owner-only repair data")
	}
	login := req(s, "POST", "/api/v1/login", `{"token":"fixture-owner"}`, "", "http://localhost:8787")
	r := httptest.NewRequest("GET", "http://localhost:8787/api/v1/state", nil)
	for _, cookie := range login.Result().Cookies() {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "synthetic-private-conversation") || !strings.Contains(w.Body.String(), "capture_delayed") {
		t.Fatal("owner did not receive repair status")
	}
	if strings.Contains(w.Body.String(), "synthetic-private-cursor") || strings.Contains(w.Body.String(), "generation") {
		t.Fatal("private repair checkpoint exposed")
	}
}
