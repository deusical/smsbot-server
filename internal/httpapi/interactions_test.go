package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/deusical/smsbot-server/internal/store"
	"github.com/deusical/smsbot-server/internal/transport"
	"net/http/httptest"
	"testing"
	"time"
)

func TestInteractionOwnerAPIAndReplyCompatibility(t *testing.T) {
	s := apiSetup(t)
	ctx := context.Background()
	cfg, _ := s.Collection.Settings()
	cfg.Enabled = true
	if err := s.Collection.SetSettings(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	s.Engine.Pause(false)
	m := transport.Incoming{ID: "synthetic-actions", ConversationID: "demo-chat", ParticipantID: "demo-peer", Text: "Synthetic action target", At: time.Now(), Interaction: &transport.MessageInteraction{UpstreamID: "synthetic-actions", Protocol: "RCS", OwnerID: "synthetic-self", ObservedAt: time.Now(), Reactions: []transport.Reaction{}}}
	if err := s.Collection.Observe(ctx, m); err != nil {
		t.Fatal(err)
	}
	target := store.Hash("demo-chat\x00" + m.ID)
	base := "/api/v1/inbox/conversations/demo-chat/messages/" + target
	for _, path := range []string{base + "/actions", base + "/window"} {
		w := httptest.NewRecorder()
		s.ServeHTTP(w, httptest.NewRequest("GET", "http://localhost:8787"+path, nil))
		if w.Code != 401 {
			t.Fatal("anonymous action access", w.Code)
		}
	}
	w := ownerRequest(s, "GET", base+"/actions", "")
	if w.Code != 200 {
		t.Fatal("capability request failed", w.Code)
	}
	var a struct {
		Reply   bool   `json:"reply"`
		React   bool   `json:"react"`
		Edit    bool   `json:"edit"`
		Version string `json:"version"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &a); err != nil || !a.Reply || !a.React || a.Edit {
		t.Fatal("capabilities incorrect", err)
	}
	body, _ := json.Marshal(map[string]string{"text": "Synthetic quoted reply", "request_id": "synthetic-quoted-api-request", "reply_to": target})
	if w = ownerRequest(s, "POST", "/api/v1/inbox/conversations/demo-chat/compose", string(body)); w.Code != 202 {
		t.Fatal("quoted compose rejected", w.Code)
	}
	if w = ownerRequest(s, "POST", "/api/v1/inbox/conversations/demo-chat/compose", `{"text":"Synthetic plain send","request_id":"synthetic-legacy-api-request"}`); w.Code != 202 {
		t.Fatal("legacy compose regressed", w.Code)
	}
	body, _ = json.Marshal(map[string]string{"emoji": "👍", "operation": "add", "version": a.Version, "request_id": "synthetic-reaction-api-request"})
	if w = ownerRequest(s, "POST", base+"/reactions", string(body)); w.Code != 202 {
		t.Fatal("reaction rejected", w.Code)
	}
	var result struct {
		ID string `json:"id"`
	}
	json.Unmarshal(w.Body.Bytes(), &result)
	if w = ownerRequest(s, "GET", "/api/v1/inbox/conversations/demo-chat/actions/"+result.ID, ""); w.Code != 200 {
		t.Fatal("action status unavailable")
	}
	if w = ownerRequest(s, "GET", "/api/v1/inbox/conversations/demo-sms/actions/"+result.ID, ""); w.Code != 404 {
		t.Fatal("cross-conversation status exposed")
	}
	if w = ownerRequest(s, "GET", fmt.Sprintf("/api/v1/inbox/conversations/demo-sms/messages/%s/window", target), ""); w.Code != 404 {
		t.Fatal("cross-conversation message exposed")
	}
	if w = ownerRequest(s, "GET", base+"/window", ""); w.Code != 200 {
		t.Fatal("local window unavailable", w.Code)
	}
}
