package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/deusical/smsbot-server/internal/collection"
	"github.com/deusical/smsbot-server/internal/transport"
)

func demoRoster(t *testing.T, s *Server) string {
	t.Helper()
	c, err := transport.Lookup(t.Context(), s.Transport, "demo-group")
	if err != nil || c.Roster == "" {
		t.Fatalf("no group: %+v %v", c, err)
	}
	return c.Roster
}

func TestGroupEnrollmentNeedsARealMemberAndTheRosterTheOwnerSaw(t *testing.T) {
	t.Parallel()
	s := apiSetup(t)
	roster := demoRoster(t, s)
	post := func(body string) int { return req(s, "POST", "/api/v1/enrollment", body, "fixture-client", "").Code }
	for name, body := range map[string]string{
		"no roster":      `{"conversation_id":"demo-group","participant_id":"demo-member-a"}`,
		"someone else's": `{"conversation_id":"demo-group","participant_id":"demo-member-a","roster":"group:other"}`,
		"not a member":   fmt.Sprintf(`{"conversation_id":"demo-group","participant_id":"stranger","roster":%q}`, roster),
		"the group id":   fmt.Sprintf(`{"conversation_id":"demo-group","participant_id":"group:demo-group","roster":%q}`, roster),
	} {
		if code := post(body); code != 400 {
			t.Errorf("%s: %d", name, code)
		}
	}
	if s.Store.BotOn("demo-group") {
		t.Fatal("a refused enrollment turned the bot on")
	}
	if code := post(fmt.Sprintf(`{"conversation_id":"demo-group","participant_id":"demo-member-a","roster":%q}`, roster)); code != 200 {
		t.Fatalf("enroll: %d", code)
	}
	if !s.Store.BotOn("demo-group") {
		t.Fatal("not enrolled")
	}
}

func TestGroupApprovalAndRemovingOneMember(t *testing.T) {
	t.Parallel()
	s := apiSetup(t)
	fixture := s.Transport.(*transport.Fixture)
	roster := demoRoster(t, s)
	for _, member := range []string{"demo-member-a", "demo-member-b"} {
		if w := req(s, "POST", "/api/v1/enrollment", fmt.Sprintf(`{"conversation_id":"demo-group","participant_id":%q,"roster":%q}`, member, roster), "fixture-client", ""); w.Code != 200 {
			t.Fatalf("enroll %s: %d", member, w.Code)
		}
	}
	// The group changes: approving the old roster is refused, enrolling is blocked.
	fixture.SetGroupMembers("demo-member-a", "demo-member-b", "demo-member-c")
	if w := req(s, "POST", "/api/v1/enrollment/demo-group/approve", fmt.Sprintf(`{"roster":%q}`, roster), "fixture-client", ""); w.Code != 409 {
		t.Fatalf("approved a roster that is no longer the group's: %d", w.Code)
	}
	now := demoRoster(t, s)
	if w := req(s, "POST", "/api/v1/enrollment", fmt.Sprintf(`{"conversation_id":"demo-group","participant_id":"demo-member-c","roster":%q}`, now), "fixture-client", ""); w.Code != 409 {
		t.Fatalf("enrolled into an unapproved group: %d", w.Code)
	}
	if w := req(s, "POST", "/api/v1/enrollment/demo-group/approve", fmt.Sprintf(`{"roster":%q}`, now), "fixture-client", ""); w.Code != 200 {
		t.Fatalf("approve: %d", w.Code)
	}
	// The listing shows who is enrolled and under which roster.
	w := req(s, "GET", "/api/v1/inbox/conversations", "", "fixture-client", "")
	var page struct {
		Conversations []transport.Conversation `json:"conversations"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	var group transport.Conversation
	for _, c := range page.Conversations {
		if c.ID == "demo-group" {
			group = c
		}
	}
	if !group.Enrolled || len(group.EnrolledMembers) != 2 || group.BotRoster != now || group.Roster != now {
		t.Fatalf("listing: %+v", group)
	}
	if w = req(s, "DELETE", "/api/v1/enrollment/demo-group/members/demo-member-a", "", "fixture-client", ""); w.Code != 200 {
		t.Fatalf("remove: %d", w.Code)
	}
	if members, _, _ := s.Store.GroupBot("demo-group"); len(members) != 1 || members[0] != "demo-member-b" {
		t.Fatalf("after removing: %v", members)
	}
	if w = req(s, "DELETE", "/api/v1/enrollment/demo-group", "", "fixture-client", ""); w.Code != 200 || s.Store.BotOn("demo-group") {
		t.Fatalf("turning the bot off for the group: %d", w.Code)
	}
}

func TestAGroupSharesOneAIConversationOnlyWhenTheOwnerSwitchesItOn(t *testing.T) {
	t.Parallel()
	s := apiSetup(t)
	put := func(id, body string) int {
		return req(s, "PUT", "/api/v1/enrollment/"+id+"/shared", body, "fixture-client", "").Code
	}
	if s.Store.GroupShared("demo-group") {
		t.Fatal("a group shares by default")
	}
	for name, c := range map[string]struct{ id, body string }{
		"no choice":     {"demo-group", `{}`},
		"not a group":   {"demo-chat", `{"shared":true}`},
		"unknown group": {"nowhere", `{"shared":true}`},
	} {
		if code := put(c.id, c.body); code == 200 {
			t.Errorf("%s was accepted", name)
		}
	}
	if code := put("demo-group", `{"shared":true}`); code != 200 || !s.Store.GroupShared("demo-group") {
		t.Fatalf("switch on: %d", code)
	}
	w := req(s, "GET", "/api/v1/inbox/conversations", "", "fixture-client", "")
	var page struct {
		Conversations []transport.Conversation `json:"conversations"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	shown := false
	for _, c := range page.Conversations {
		if c.ID == "demo-group" {
			shown = c.SharedAI
		}
	}
	if !shown {
		t.Fatal("the listing does not show the group as shared")
	}
	if code := put("demo-group", `{"shared":false}`); code != 200 || s.Store.GroupShared("demo-group") {
		t.Fatalf("switch off: %d", code)
	}
}

type cachedCountingTransport struct {
	*transport.Fixture
	cached  map[string]transport.Conversation
	lookups atomic.Int32
	lists   atomic.Int32
}

func (c *cachedCountingTransport) CachedConversation(id string) (transport.Conversation, bool) {
	if !c.Ready() {
		return transport.Conversation{}, false
	}
	v, ok := c.cached[id]
	return v, ok
}

func (c *cachedCountingTransport) LookupConversation(ctx context.Context, id string) (transport.Conversation, error) {
	c.lookups.Add(1)
	if live, err := transport.Lookup(ctx, c.Fixture, id); err == nil {
		return live, nil
	}
	if v, ok := c.cached[id]; ok {
		return v, nil
	}
	return transport.Lookup(ctx, c.Fixture, id)
}

func (c *cachedCountingTransport) Conversations(ctx context.Context) ([]transport.Conversation, error) {
	c.lists.Add(1)
	return c.Fixture.Conversations(ctx)
}

func TestCachedTogglesAndBatchGroupMembersAvoidPhoneWakeups(t *testing.T) {
	t.Parallel()
	s := apiSetup(t)
	fixture := s.Transport.(*transport.Fixture)
	group, err := transport.Lookup(t.Context(), fixture, "demo-group")
	if err != nil {
		t.Fatal(err)
	}
	deepChat := transport.Conversation{
		OutgoingID:    "demo-self",
		ID:            "chat-page-2-row-30",
		ParticipantID: "peer-30",
		Label:         "Older conversation",
		Protocol:      "RCS",
		Writable:      true,
	}
	ct := &cachedCountingTransport{
		Fixture: fixture,
		cached: map[string]transport.Conversation{
			"demo-group":         group,
			"chat-page-2-row-30": deepChat,
		},
	}
	useTransport(s, ct)

	stateRevision := func() string {
		w := req(s, "GET", "/api/v1/state", "", "fixture-client", "")
		var v map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
			t.Fatal(err)
		}
		rev, _ := v["conversations_revision"].(string)
		return rev
	}

	rev0 := stateRevision()

	// Enrolling a conversation past the first 25 uses CachedConversation without waking the phone.
	if w := req(s, "POST", "/api/v1/enrollment", `{"conversation_id":"chat-page-2-row-30","participant_id":"peer-30"}`, "fixture-client", ""); w.Code != 200 {
		t.Fatalf("enroll deep chat: %d %s", w.Code, w.Body.String())
	}
	rev1 := stateRevision()
	if rev1 == rev0 {
		t.Fatal("enrolling did not advance conversations_revision")
	}

	// Batch group member update enrolls everyone in one request with zero phone lookups.
	putMembers := func(body string) int {
		return req(s, "PUT", "/api/v1/enrollment/demo-group/members", body, "fixture-client", "").Code
	}
	if code := putMembers(fmt.Sprintf(`{"members":["demo-member-a","demo-member-b"],"roster":%q}`, group.Roster)); code != 200 {
		t.Fatalf("batch enroll everyone: %d", code)
	}
	members, gotRoster, _ := s.Store.GroupBot("demo-group")
	if len(members) != 2 || gotRoster != group.Roster {
		t.Fatalf("batch enrolled: %v %q", members, gotRoster)
	}
	rev2 := stateRevision()
	if rev2 == rev1 {
		t.Fatal("batch group enrollment did not advance conversations_revision")
	}

	// Shared group AI toggle also uses CachedConversation without waking the phone.
	if w := req(s, "PUT", "/api/v1/enrollment/demo-group/shared", `{"shared":true}`, "fixture-client", ""); w.Code != 200 {
		t.Fatalf("shared group: %d", w.Code)
	}
	rev3 := stateRevision()
	if rev3 == rev2 {
		t.Fatal("shared group toggle did not advance conversations_revision")
	}

	// Turning everyone off in one batch request clears group_enrollment without touching 1:1 enrollment.
	if code := putMembers(`{"members":[]}`); code != 200 {
		t.Fatalf("batch clear everyone: %d", code)
	}
	if s.Store.BotOn("demo-group") {
		t.Fatal("group remained enrolled after empty members PUT")
	}
	if !s.Store.Enrolled("chat-page-2-row-30", "peer-30") {
		t.Fatal("clearing group members disturbed individual enrollment")
	}

	if ct.lookups.Load() != 0 || ct.lists.Load() != 0 {
		t.Fatalf("expected 0 phone lookups/lists for cached toggles, got lookups=%d lists=%d", ct.lookups.Load(), ct.lists.Load())
	}

	// Approving a changed group MUST still check the phone live.
	if code := putMembers(fmt.Sprintf(`{"members":["demo-member-a"],"roster":%q}`, group.Roster)); code != 200 {
		t.Fatalf("re-enroll member: %d", code)
	}
	fixture.SetGroupMembers("demo-member-a", "demo-member-b", "demo-member-c")
	updated, err := transport.Lookup(t.Context(), fixture, "demo-group")
	if err != nil {
		t.Fatal(err)
	}
	if code := putMembers(fmt.Sprintf(`{"members":["demo-member-a","demo-member-c"],"roster":%q}`, updated.Roster)); code != 409 {
		t.Fatalf("expected 409 group_needs_approval before approving changed roster, got %d", code)
	}
	beforeApprove := ct.lookups.Load()
	if w := req(s, "POST", "/api/v1/enrollment/demo-group/approve", fmt.Sprintf(`{"roster":%q}`, updated.Roster), "fixture-client", ""); w.Code != 200 {
		t.Fatalf("approve changed group: %d %s", w.Code, w.Body.String())
	}
	if ct.lookups.Load() <= beforeApprove {
		t.Fatal("approveGroup did not verify live on the phone")
	}
}

func TestSavingUnchangedSelectedConversationsSkipsPhoneLookups(t *testing.T) {
	t.Parallel()
	s := apiSetup(t)
	fixture := s.Transport.(*transport.Fixture)
	chat, err := transport.Lookup(t.Context(), fixture, "demo-chat")
	if err != nil {
		t.Fatal(err)
	}
	ct := &cachedCountingTransport{Fixture: fixture, cached: map[string]transport.Conversation{"demo-chat": chat}}
	useTransport(s, ct)

	cfg, err := s.Collection.Settings()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Enabled = true
	cfg.Scope = "selected"
	cfg.Conversations = []collection.Binding{{Conversation: "demo-chat", Participant: "demo-peer"}}
	if err := s.Collection.SetSettings(t.Context(), cfg); err != nil {
		t.Fatal(err)
	}
	initialLookups := ct.lookups.Load()
	if initialLookups != 1 {
		t.Fatalf("expected 1 lookup on initial selection, got %d", initialLookups)
	}

	// Saving unrelated preferences with the same selected conversation should not wake the phone again.
	saved, err := s.Collection.Settings()
	if err != nil {
		t.Fatal(err)
	}
	off := false
	saved.MarkRead = &off
	saved.AutomaticReplies = false
	saved.ArchiveRetentionDays = 30
	if err := s.Collection.SetSettings(t.Context(), saved); err != nil {
		t.Fatal(err)
	}
	if ct.lookups.Load() != initialLookups {
		t.Fatalf("unrelated settings save triggered phone lookups: before=%d after=%d", initialLookups, ct.lookups.Load())
	}
}
