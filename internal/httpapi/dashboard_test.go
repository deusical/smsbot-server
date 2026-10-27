package httpapi

import (
	"net/http"
	"strings"
	"testing"
)

func TestDashboardPagesServeTheAppAndNothingElseDoes(t *testing.T) {
	s := apiSetup(t)
	home := req(s, "GET", "/", "", "", "")
	if home.Code != 200 || !strings.Contains(home.Body.String(), "<title>SmsBot</title>") {
		t.Fatalf("home: %d", home.Code)
	}
	for _, path := range []string{"/conversations", "/conversations/", "/conversations/demo-chat", "/conversations/a%2Fb%20c", "/commands", "/activity", "/archive", "/settings"} {
		r := req(s, "GET", path, "", "", "")
		if r.Code != 200 || r.Body.String() != home.Body.String() {
			t.Errorf("%s: %d, not the dashboard", path, r.Code)
		}
		if r.Header().Get("Cache-Control") != "no-cache" {
			t.Errorf("%s: cache control %q", path, r.Header().Get("Cache-Control"))
		}
	}
	// Anything else, including mistyped API paths, is still not found.
	for _, path := range []string{"/nonsense", "/settingsx", "/api/v1/nonsense", "/assets/missing.js", "/conversations.js"} {
		if r := req(s, "GET", path, "", "", ""); r.Code != http.StatusNotFound {
			t.Errorf("%s: %d, want 404", path, r.Code)
		}
	}
	if r := req(s, "HEAD", "/settings", "", "", ""); r.Code != 200 {
		t.Errorf("HEAD /settings: %d", r.Code)
	}
	if r := req(s, "POST", "/settings", "", "", ""); r.Code == 200 {
		t.Errorf("POST /settings served the dashboard")
	}
}
