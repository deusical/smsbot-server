package httpapi

import (
	"context"
	"encoding/json"
	"github.com/deusical/smsbot-server/internal/bot"
	"github.com/deusical/smsbot-server/internal/transport"
	"testing"
	"time"
)

type botSettings = bot.InboxSettings

func fixtureOf(t *testing.T, s *Server) *transport.Fixture {
	t.Helper()
	f, ok := s.Engine.Transport.(*transport.Fixture)
	if !ok {
		t.Fatal("expected fixture transport")
	}
	return f
}

func setInbox(t *testing.T, s *Server, change func(*botSettings)) {
	t.Helper()
	cfg, err := s.Engine.InboxSettings()
	if err != nil {
		t.Fatal(err)
	}
	change(&cfg)
	if err := s.Engine.SetInboxSettings(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
}

func listConversations(t *testing.T, s *Server) map[string]transport.Conversation {
	t.Helper()
	w := ownerRequest(s, "GET", "/api/v1/inbox/conversations", "")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var page transport.ConversationPage
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	out := map[string]transport.Conversation{}
	for _, c := range page.Conversations {
		out[c.ID] = c
	}
	return out
}

func TestConversationListCarriesRecencyAndPreviewOnlyWithCollection(t *testing.T) {
	s := apiSetup(t)
	when := time.Now().Add(-time.Minute).UTC()
	setInbox(t, s, func(c *botSettings) { c.Enabled = true })
	if err := s.Engine.Observe(context.Background(), transport.Incoming{ID: "synthetic-preview", ConversationID: "demo-chat", ParticipantID: "demo-peer", Text: "Synthetic   preview\ntext", At: when}); err != nil {
		t.Fatal(err)
	}
	c := listConversations(t, s)["demo-chat"]
	if c.Preview != "Synthetic preview text" || c.PreviewFromMe {
		t.Fatalf("preview %q fromMe=%v", c.Preview, c.PreviewFromMe)
	}
	if at, err := time.Parse(time.RFC3339Nano, c.LastMessageAt); err != nil || at.Before(when.Add(-time.Second)) {
		t.Fatalf("last message %q %v", c.LastMessageAt, err)
	}
	// A saved archive stays readable after collection stops, so its preview stays too.
	// Conversations that were never collected have nothing saved and show no preview.
	setInbox(t, s, func(c *botSettings) { c.Enabled = false })
	after := listConversations(t, s)
	if after["demo-chat"].Preview != "Synthetic preview text" {
		t.Fatal("archived preview lost after collection stopped")
	}
	if p := after["demo-sms"].Preview; p != "" {
		t.Fatal("preview shown for a conversation that was never collected:", p)
	}
}

func TestStateRevisionMovesWithActivity(t *testing.T) {
	s := apiSetup(t)
	revision := func() string {
		w := ownerRequest(s, "GET", "/api/v1/state", "")
		var v map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
			t.Fatal(err)
		}
		r, _ := v["conversations_revision"].(string)
		return r
	}
	before := revision()
	if before == "" {
		t.Fatal("owner state has no conversations_revision")
	}
	fixtureOf(t, s).NoteActivity()
	if revision() == before {
		t.Fatal("phone activity did not change the revision")
	}
	after := revision()
	setInbox(t, s, func(c *botSettings) { c.Enabled = true })
	s.Engine.Observe(context.Background(), transport.Incoming{ID: "synthetic-revision", ConversationID: "demo-chat", ParticipantID: "demo-peer", Text: "Synthetic", At: time.Now()})
	if revision() == after {
		t.Fatal("saved message did not change the revision")
	}
}

func TestReadReceiptsRespectConsentSettingAndThrottle(t *testing.T) {
	s := apiSetup(t)
	f := fixtureOf(t, s)
	body := `{"through":"` + time.Now().UTC().Format(time.RFC3339Nano) + `"}`
	path := "/api/v1/inbox/conversations/demo-chat/read"
	if w := ownerRequest(s, "POST", path, body); w.Code != 409 {
		t.Fatal("read accepted without collection", w.Code)
	}
	setInbox(t, s, func(c *botSettings) { c.Enabled = true })
	if w := ownerRequest(s, "POST", path, `{"through":"nonsense"}`); w.Code != 400 {
		t.Fatal("bad time accepted", w.Code)
	}
	if w := ownerRequest(s, "POST", path, body); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := ownerRequest(s, "POST", path, body); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if f.Read["demo-chat"] != 1 {
		t.Fatalf("throttle failed: %d marks", f.Read["demo-chat"])
	}
	off := false
	setInbox(t, s, func(c *botSettings) { c.MarkRead = &off })
	f.Read = map[string]int{}
	if w := ownerRequest(s, "POST", path, body); w.Code != 409 || f.Read["demo-chat"] != 0 {
		t.Fatal("read marked while setting is off", w.Code)
	}
	if w := ownerRequest(s, "POST", "/api/v1/inbox/conversations/demo-group/read", body); w.Code != 409 {
		t.Fatal("group conversation marked read", w.Code)
	}
}

func TestReadReceiptsDefaultOnForOldSettings(t *testing.T) {
	var cfg botSettings
	if err := json.Unmarshal([]byte(`{"enabled":true,"scope":"all"}`), &cfg); err != nil {
		t.Fatal(err)
	}
	if !cfg.ReadReceipts() {
		t.Fatal("settings saved before the option existed must keep read receipts on")
	}
}

func TestTypingOnlyForOptedInConversations(t *testing.T) {
	s := apiSetup(t)
	f := fixtureOf(t, s)
	f.SetTyping("demo-chat", true)
	typing := func() []string {
		w := ownerRequest(s, "GET", "/api/v1/inbox/typing", "")
		if w.Code != 200 {
			t.Fatal(w.Code)
		}
		var v struct {
			Conversations []string `json:"conversations"`
		}
		json.Unmarshal(w.Body.Bytes(), &v)
		return v.Conversations
	}
	if got := typing(); len(got) != 0 {
		t.Fatal("typing exposed without collection", got)
	}
	setInbox(t, s, func(c *botSettings) { c.Enabled = true })
	if got := typing(); len(got) != 1 || got[0] != "demo-chat" {
		t.Fatal("typing missing", got)
	}
	f.SetTyping("demo-chat", false)
	if got := typing(); len(got) != 0 {
		t.Fatal("stopped typing still reported", got)
	}
}

type refreshingFixture struct {
	*transport.Fixture
	refreshes int
}

func (f *refreshingFixture) RequestConversationRefresh() bool { f.refreshes++; return true }
func (f *refreshingFixture) PhoneRequests() map[string]transport.PhoneRequestCount {
	return map[string]transport.PhoneRequestCount{"list": {Total: 3, LastHour: 2}}
}

func TestConversationListForcesPhoneReadOnlyWhenAskedAndStateReportsPhoneRequests(t *testing.T) {
	s := apiSetup(t)
	setInbox(t, s, func(c *botSettings) { c.Enabled = true })
	f := &refreshingFixture{Fixture: fixtureOf(t, s)}
	s.Engine.Transport = f
	for _, path := range []string{"/api/v1/inbox/conversations", "/api/v1/inbox/conversations?refresh=0", "/api/v1/inbox/conversations?refresh=1"} {
		if w := ownerRequest(s, "GET", path, ""); w.Code != 200 {
			t.Fatal(path, w.Code)
		}
	}
	if f.refreshes != 1 {
		t.Fatalf("forced %d phone reads", f.refreshes)
	}
	w := ownerRequest(s, "GET", "/api/v1/state", "")
	var state struct {
		PhoneRequests map[string]transport.PhoneRequestCount `json:"phone_requests"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &state) != nil || state.PhoneRequests["list"] != (transport.PhoneRequestCount{Total: 3, LastHour: 2}) {
		t.Fatal("state omitted phone request counts", w.Body.String())
	}
}
