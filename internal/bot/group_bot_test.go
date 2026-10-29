package bot

import (
	"context"
	"errors"
	"testing"

	"github.com/deusical/smsbot-server/internal/store"
	"github.com/deusical/smsbot-server/internal/transport"
)

// botGroup is an engine with the demo group's member A enrolled under its
// current roster, and the bot live.
func botGroup(t *testing.T) (*Engine, *transport.Fixture, string) {
	t.Helper()
	e, f := setup(t)
	arm(t, e)
	c, err := transport.Lookup(context.Background(), f, "demo-group")
	if err != nil || c.Roster == "" {
		t.Fatalf("no demo group: %+v %v", c, err)
	}
	if err = e.EnrollMember("demo-group", "demo-member-a", c.Roster); err != nil {
		t.Fatal(err)
	}
	return e, f, c.Roster
}

func groupCommand(id, member, text string) transport.Incoming {
	m := incoming()
	m.ID, m.ConversationID, m.ParticipantID, m.Text = id, "demo-group", member, text
	return m
}

func TestAnEnrolledMemberRunsCommandsInAGroupAndTheGroupSeesTheAnswer(t *testing.T) {
	e, f, roster := botGroup(t)
	ctx := context.Background()
	ok, err := e.Accept(ctx, groupCommand("group-ping-1", "demo-member-a", "!ping"))
	if err != nil || !ok {
		t.Fatalf("accept %t %v", ok, err)
	}
	_, p, _ := e.Store.JobPlan(store.Hash("demo-group\x00group-ping-1"))
	if p.Participant != "demo-member-a" || p.Roster != roster || !p.GroupBotJob() {
		t.Fatalf("plan: %+v", p)
	}
	if err = e.Dispatch(ctx); err != nil {
		t.Fatal(err)
	}
	if len(f.Sent) != 1 || stateOf(t, e, store.Hash("demo-group\x00group-ping-1")) != "submitted" {
		t.Fatalf("sent %v", f.Sent)
	}
}

func TestOnlyEnrolledMembersAndNotTheOwnerWithoutTheSettingRunCommandsInAGroup(t *testing.T) {
	e, f, _ := botGroup(t)
	ctx := context.Background()
	for name, m := range map[string]transport.Incoming{
		"another member":  groupCommand("group-b-1", "demo-member-b", "!ping"),
		"someone outside": groupCommand("group-x-1", "stranger", "!ping"),
		"the owner's own": func() transport.Incoming { m := groupCommand("group-o-1", "me", "!ping"); m.Outgoing = true; return m }(),
		"an old message": func() transport.Incoming {
			m := groupCommand("group-h-1", "demo-member-a", "!ping")
			m.Historical = true
			return m
		}(),
		"not a command": groupCommand("group-n-1", "demo-member-a", "hello all"),
	} {
		if ok, err := e.Accept(ctx, m); err != nil || ok {
			t.Errorf("%s: accepted=%t err=%v", name, ok, err)
		}
	}
	if err := e.Dispatch(ctx); err != nil || len(f.Sent) != 0 {
		t.Fatalf("something was sent: %v %v", f.Sent, err)
	}
}

func ownerGroupCommand(id, text string) transport.Incoming {
	m := groupCommand(id, "me", text)
	m.Outgoing, m.TransactionID, m.Status = true, "phone-"+id, "submitted"
	return m
}

func TestTheOwnerRunsCommandsInAnyGroupTheyCanSendTo(t *testing.T) {
	e, f, roster := botGroup(t)
	ctx := context.Background()
	allowOwnerCommands(t, e, false)
	if ok, err := e.Accept(ctx, ownerGroupCommand("group-own-1", "!ping")); err != nil || ok {
		t.Fatalf("answered with commands from the phone off: %t %v", ok, err)
	}
	allowOwnerCommands(t, e, true)
	if ok, err := e.Accept(ctx, ownerGroupCommand("group-own-2", "!ping")); err != nil || !ok {
		t.Fatalf("not answering the owner: %t %v", ok, err)
	}
	id := store.Hash("demo-group\x00group-own-2")
	if _, p, _ := e.Store.JobPlan(id); p.Participant != store.OwnerMember || p.Roster != roster || !p.Owner || p.MemberOf() != store.OwnerMember {
		t.Fatalf("plan: %+v", p)
	}
	if err := e.Dispatch(ctx); err != nil || len(f.Sent) != 1 || stateOf(t, e, id) != "submitted" {
		t.Fatalf("sent %v %v", f.Sent, err)
	}
	// The bot's own answer comes back as an outgoing message and runs nothing.
	if ok, err := e.Accept(ctx, ownerGroupCommand("group-own-echo", "pong")); err != nil || ok {
		t.Fatalf("ran its own answer: %t %v", ok, err)
	}
	// Someone joins before the answer goes out: it is cancelled, not sent to them.
	if ok, err := e.Accept(ctx, ownerGroupCommand("group-own-3", "!ping")); err != nil || !ok {
		t.Fatalf("not queued: %t %v", ok, err)
	}
	f.SetGroupMembers("demo-member-a", "demo-member-b", "demo-member-c")
	if err := e.Dispatch(ctx); err != nil || len(f.Sent) != 1 {
		t.Fatalf("sent to the new people: %v %v", f.Sent, err)
	}
	if j, _, _ := e.Store.JobPlan(store.Hash("demo-group\x00group-own-3")); j.State != "cancelled" || j.Code != "identity_changed" {
		t.Fatalf("job: %s %s", j.State, j.Code)
	}
	// A new command is answered to the group as it is now.
	if ok, err := e.Accept(ctx, ownerGroupCommand("group-own-4", "!ping")); err != nil || !ok {
		t.Fatalf("not answering after the change: %t %v", ok, err)
	}
	// Not a group, not a conversation, or not writable: nothing.
	other := ownerGroupCommand("group-own-5", "!ping")
	other.ConversationID = "no-such-chat"
	if ok, err := e.Accept(ctx, other); ok {
		t.Fatalf("answered an unknown conversation: %t %v", ok, err)
	}
}

func TestTheBotGoesSilentInAGroupWhosePeopleChangedUntilTheOwnerApproves(t *testing.T) {
	e, f, roster := botGroup(t)
	ctx := context.Background()
	queued, err := e.Accept(ctx, groupCommand("group-ping-2", "demo-member-a", "!ping"))
	if err != nil || !queued {
		t.Fatal("not queued", err)
	}
	// Someone joins before the answer goes out.
	f.SetGroupMembers("demo-member-a", "demo-member-b", "demo-member-c")
	if err = e.Dispatch(ctx); err != nil {
		t.Fatal(err)
	}
	if j, _, _ := e.Store.JobPlan(store.Hash("demo-group\x00group-ping-2")); j.State != "cancelled" || j.Code != "identity_changed" || len(f.Sent) != 0 {
		t.Fatalf("queued answer: %s %s sent %v", j.State, j.Code, f.Sent)
	}
	// New commands are ignored while the roster is not the approved one.
	if ok, err := e.Accept(ctx, groupCommand("group-ping-3", "demo-member-a", "!ping")); err != nil || ok {
		t.Fatalf("answered an unapproved group: %t %v", ok, err)
	}
	// Enrolling more people under the new roster needs the approval first.
	c, _ := transport.Lookup(ctx, f, "demo-group")
	if err = e.EnrollMember("demo-group", "demo-member-b", c.Roster); !errors.Is(err, store.ErrGroupNeedsApproval) {
		t.Fatalf("enrolled into an unapproved group: %v", err)
	}
	present := []string{"demo-member-a", "demo-member-b", "demo-member-c"}
	if err = e.ApproveGroup("demo-group", c.Roster, present); err != nil {
		t.Fatal(err)
	}
	if got, _ := e.Store.GroupMemberEnrollment("demo-group", "demo-member-a"); got != c.Roster || got == roster {
		t.Fatalf("approved roster %q", got)
	}
	if ok, err := e.Accept(ctx, groupCommand("group-ping-4", "demo-member-a", "!ping")); err != nil || !ok {
		t.Fatalf("not answering after approval: %t %v", ok, err)
	}
}

func TestApprovingAGroupDropsMembersWhoLeft(t *testing.T) {
	e, f, _ := botGroup(t)
	ctx := context.Background()
	c, _ := transport.Lookup(ctx, f, "demo-group")
	if err := e.EnrollMember("demo-group", "demo-member-b", c.Roster); err != nil {
		t.Fatal(err)
	}
	f.SetGroupMembers("demo-member-a")
	c, _ = transport.Lookup(ctx, f, "demo-group")
	if err := e.ApproveGroup("demo-group", c.Roster, []string{"demo-member-a"}); err != nil {
		t.Fatal(err)
	}
	members, roster, _ := e.Store.GroupBot("demo-group")
	if len(members) != 1 || members[0] != "demo-member-a" || roster != c.Roster {
		t.Fatalf("enrolled %v under %q", members, roster)
	}
}

func TestStoppingOneMemberCancelsWaitingWorkAndOthersKeepTheirAccess(t *testing.T) {
	e, f, roster := botGroup(t)
	ctx := context.Background()
	if err := e.EnrollMember("demo-group", "demo-member-b", roster); err != nil {
		t.Fatal(err)
	}
	if ok, err := e.Accept(ctx, groupCommand("group-ping-5", "demo-member-a", "!ping")); err != nil || !ok {
		t.Fatalf("not queued: %t %v", ok, err)
	}
	if err := e.UnenrollMember("demo-group", "demo-member-a"); err != nil {
		t.Fatal(err)
	}
	if err := e.Dispatch(ctx); err != nil || len(f.Sent) != 0 {
		t.Fatalf("sent after unenrolling: %v %v", f.Sent, err)
	}
	if ok, _ := e.Accept(ctx, groupCommand("group-ping-6", "demo-member-a", "!ping")); ok {
		t.Fatal("a removed member still ran a command")
	}
	if ok, err := e.Accept(ctx, groupCommand("group-ping-7", "demo-member-b", "!ping")); err != nil || !ok {
		t.Fatalf("the other member lost access: %t %v", ok, err)
	}
	// Turning the bot off for the whole group removes everyone.
	if err := e.Unenroll("demo-group"); err != nil {
		t.Fatal(err)
	}
	if e.Store.BotOn("demo-group") {
		t.Fatal("the bot is still on in the group")
	}
}

func TestEachMemberOfAGroupHasTheirOwnAIMemory(t *testing.T) {
	a := store.Plan{Participant: "demo-member-a", Roster: "group:r"}
	b := store.Plan{Participant: "demo-member-b", Roster: "group:r"}
	individual := store.Plan{Participant: "demo-peer"}
	owner := store.Plan{Participant: "group:demo-group", Roster: "group:r", InboxOwnerOnly: true}
	if memoryScope("c", "!ask", "m", a.MemberOf()) == memoryScope("c", "!ask", "m", b.MemberOf()) {
		t.Fatal("two members share memory")
	}
	if memoryScope("c", "!ask", "m", individual.MemberOf()) != memoryScope("c", "!ask", "m", "") || owner.MemberOf() != "" {
		t.Fatal("an individual's memory moved")
	}
}

func groupPrompt(id, member, text, wire string) transport.Incoming {
	m := threadPrompt(id, text, wire)
	m.ConversationID, m.ParticipantID = "demo-group", member
	return m
}

func TestAnAIThreadInAGroupBelongsToWhoStartedIt(t *testing.T) {
	e, _, roster := botGroup(t)
	configureAI(t, e, true, false)
	provider := &transcriptProvider{}
	e.Provider = provider
	ctx := context.Background()
	if err := e.EnrollMember("demo-group", "demo-member-b", roster); err != nil {
		t.Fatal(err)
	}
	root := groupPrompt("g-root", "demo-member-a", "!ask First question", "")
	if p := threadAccept(t, e, root); p.Thread == nil || p.Participant != "demo-member-a" || p.Roster != roster {
		t.Fatalf("root plan: %+v", p)
	}
	if err := e.Dispatch(ctx); err != nil {
		t.Fatal(err)
	}
	threadEcho(t, e, root, "g-answer-root")
	// The asker carries on the conversation with its context.
	own := threadAccept(t, e, groupPrompt("g-own", "demo-member-a", "Make it shorter", "g-answer-root"))
	if len(own.Context) != 2 || own.Context[0].Content != "First question" || own.Reply != "" {
		t.Fatalf("the asker lost their thread: %+v", own)
	}
	// Someone else replying to that answer does not inherit the asker's thread.
	other := threadAccept(t, e, groupPrompt("g-other", "demo-member-b", "What did they ask?", "g-answer-root"))
	if len(other.Context) != 0 || other.Reply == "" || other.Model != "" {
		t.Fatalf("another member read someone else's thread: %+v", other)
	}
}

func TestTheOwnerContinuesAnAIThreadInAGroupByReplyingToTheAnswer(t *testing.T) {
	e, _, _ := botGroup(t)
	configureAI(t, e, true, false)
	cfg, err := e.Configuration()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Settings.OwnerCommands = true
	if err = e.SetConfiguration(cfg); err != nil {
		t.Fatal(err)
	}
	provider := &transcriptProvider{}
	e.Provider = provider
	root := ownerGroupCommand("g-own-root", "!ask First question")
	root.Interaction = &transport.MessageInteraction{UpstreamID: root.ID, Protocol: "RCS", Sender: "me"}
	if p := threadAccept(t, e, root); p.Participant != store.OwnerMember || p.Thread == nil {
		t.Fatalf("root plan: %+v", p)
	}
	if err = e.Dispatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	threadEcho(t, e, root, "g-own-answer")
	follow := ownerGroupCommand("g-own-follow", "Make it shorter")
	follow.Interaction = &transport.MessageInteraction{UpstreamID: follow.ID, Protocol: "RCS", ReplyTo: "g-own-answer", Sender: "me"}
	p := threadAccept(t, e, follow)
	if len(p.Context) != 2 || p.Context[0].Content != "First question" || p.Participant != store.OwnerMember {
		t.Fatalf("the reply did not continue the thread: %+v", p)
	}
}
