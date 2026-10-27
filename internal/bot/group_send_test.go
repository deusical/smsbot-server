package bot

import (
	"context"
	"testing"

	"github.com/deusical/smsbot-server/internal/store"
	"github.com/deusical/smsbot-server/internal/transport"
)

func groupSetup(t *testing.T) (*Engine, *transport.Fixture, string) {
	t.Helper()
	e, f := setup(t)
	s, _ := e.Collection.Settings()
	s.Enabled, s.Scope, s.Groups = true, "all", true
	if err := e.Collection.SetSettings(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	e.Pause(false)
	c, err := transport.Lookup(context.Background(), f, "demo-group")
	if err != nil || c.Roster == "" {
		t.Fatalf("no demo group: %+v %v", c, err)
	}
	return e, f, c.Roster
}

func jobCount(t *testing.T, e *Engine) int {
	t.Helper()
	jobs, err := e.Store.Jobs()
	if err != nil {
		t.Fatal(err)
	}
	return len(jobs)
}

func TestTheOwnerSendsToAGroupAsTheyAreShownIt(t *testing.T) {
	e, f, roster := groupSetup(t)
	ctx := context.Background()
	id, err := e.ComposeInboxTo(ctx, "demo-group", roster, "hello everyone", "group-request-0001", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, p, err := e.Store.JobPlan(id); err != nil || p.Roster != roster || p.Participant != "group:demo-group" {
		t.Fatalf("plan: %+v %v", p, err)
	}
	if err = e.Dispatch(ctx); err != nil {
		t.Fatal(err)
	}
	j, _, _ := e.Store.JobPlan(id)
	if j.State != "submitted" || len(f.Sent) != 1 {
		t.Fatalf("state %s, sent %v", j.State, f.Sent)
	}
}

func TestASendToAGroupNeedsTheRosterTheOwnerSawAndNothingElse(t *testing.T) {
	e, f, roster := groupSetup(t)
	ctx := context.Background()
	for name, given := range map[string]string{"missing": "", "someone else's": "group:not-this-one"} {
		if _, err := e.ComposeInboxTo(ctx, "demo-group", given, "hello", "group-request-"+name+"-1", nil); err == nil || err.Error() != "group_changed" {
			t.Errorf("%s roster: %v", name, err)
		}
	}
	// The old entry point knows nothing of rosters, so it can't send to a group.
	if _, err := e.ComposeInboxFiles(ctx, "demo-group", "hello", "group-request-legacy-1", nil); err == nil || err.Error() != "group_changed" {
		t.Errorf("legacy entry point: %v", err)
	}
	// People joined since the owner looked.
	f.SetGroupMembers("demo-member-a", "demo-member-b", "demo-member-c")
	if _, err := e.ComposeInboxTo(ctx, "demo-group", roster, "hello", "group-request-late-0001", nil); err == nil || err.Error() != "group_changed" {
		t.Errorf("stale roster: %v", err)
	}
	if jobCount(t, e) != 0 || len(f.Sent) != 0 {
		t.Fatalf("something was queued or sent: %d jobs, %v", jobCount(t, e), f.Sent)
	}
}

func TestAQueuedGroupSendIsCancelledIfThePeopleChange(t *testing.T) {
	e, f, roster := groupSetup(t)
	ctx := context.Background()
	id, err := e.ComposeInboxTo(ctx, "demo-group", roster, "hello", "group-request-0002", nil)
	if err != nil {
		t.Fatal(err)
	}
	f.SetGroupMembers("demo-member-a")
	if err = e.Dispatch(ctx); err != nil {
		t.Fatal(err)
	}
	j, _, _ := e.Store.JobPlan(id)
	if j.State != "cancelled" || j.Code != "identity_changed" || len(f.Sent) != 0 {
		t.Fatalf("state %s code %s sent %v", j.State, j.Code, f.Sent)
	}
}

func TestOnlyTheOwnerSendsToAGroupAndOnlyWhereItIsSaved(t *testing.T) {
	e, f := setup(t)
	e.Pause(false)
	ctx := context.Background()
	c, _ := transport.Lookup(ctx, f, "demo-group")
	// Groups not allowed: nothing to send to.
	if _, err := e.ComposeInboxTo(ctx, "demo-group", c.Roster, "hello", "group-request-0003", nil); err == nil || err.Error() != "inbox_access_disabled" {
		t.Fatalf("group not allowed: %v", err)
	}
	// An individual conversation takes no roster.
	enableInbox(t, e)
	id, err := e.ComposeInboxTo(ctx, "demo-chat", "", "hello", "individual-request-0001", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, p, _ := e.Store.JobPlan(id); p.Roster != "" {
		t.Fatalf("an individual got a roster: %q", p.Roster)
	}
}

func TestTheBotStillNeverActsInAGroup(t *testing.T) {
	e, f, _ := groupSetup(t)
	ctx := context.Background()
	if err := e.Store.Enroll("demo-group", "group:demo-group"); err != nil {
		t.Fatal(err)
	}
	if valid, err := e.validConversation(ctx, "demo-group", "group:demo-group", "", false); err != nil || valid {
		t.Fatalf("the bot may act in a group: %t %v", valid, err)
	}
	m := incoming()
	m.ConversationID, m.ParticipantID, m.Text, m.ID = "demo-group", "demo-member-a", "!ping", store.ID()
	if ok, err := e.Receive(ctx, m); err != nil || ok {
		t.Fatalf("a command in a group: %t %v", ok, err)
	}
	if len(f.Sent) != 0 {
		t.Fatalf("bot sent %v", f.Sent)
	}
}
