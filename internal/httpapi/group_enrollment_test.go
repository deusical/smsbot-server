package httpapi

import (
	"encoding/json"
	"fmt"
	"testing"

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
