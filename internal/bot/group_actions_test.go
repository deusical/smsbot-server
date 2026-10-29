package bot

import (
	"context"
	"testing"
	"time"

	"github.com/deusical/smsbot-server/internal/store"
	"github.com/deusical/smsbot-server/internal/transport"
)

func groupMessage(id string) transport.Incoming {
	return transport.Incoming{ID: id, ConversationID: "demo-group", ParticipantID: "demo-member-a", Text: "Synthetic group message", At: time.Now(), Interaction: &transport.MessageInteraction{UpstreamID: id, Protocol: "RCS", OwnerID: "fixture-self", Sender: "demo-member-a", ObservedAt: time.Now(), Reactions: []transport.Reaction{}}}
}

func TestTheOwnerRepliesAndReactsInAGroupAsTheyAreShownIt(t *testing.T) {
	e, f, roster := groupSetup(t)
	ctx := context.Background()
	m := groupMessage("synthetic-group-action")
	if err := e.Collection.Observe(ctx, m); err != nil {
		t.Fatal(err)
	}
	target := store.Hash("demo-group\x00" + m.ID)
	a, err := e.MessageActions(ctx, "demo-group", target)
	if err != nil || !a.Reply || !a.React {
		t.Fatalf("actions %+v %v", a, err)
	}

	// A reply to a message in the group goes as a reply, to the people shown.
	id, err := e.ComposeInboxTo(ctx, "demo-group", roster, "a reply", "group-reply-request-1", nil, target)
	if err != nil {
		t.Fatal(err)
	}
	if _, p, _ := e.Store.JobPlan(id); p.ReplyWireTarget != m.ID || p.Roster != roster {
		t.Fatalf("plan: %+v", p)
	}
	if err = e.Dispatch(ctx); err != nil || len(f.Sent) != 1 {
		t.Fatalf("reply not sent: %v %v", f.Sent, err)
	}

	// A reaction needs the roster the owner saw, like a message does.
	for name, shown := range map[string]string{"none": "", "someone else's": "group:other"} {
		if _, err = e.QueueReactionIn(ctx, "demo-group", shown, target, "👍", "add", a.Version, "group-reaction-"+name+"-1"); err == nil || err.Error() != "group_changed" {
			t.Fatalf("%s: queued a reaction to people the owner hadn't seen: %v", name, err)
		}
	}
	rid, err := e.QueueReactionIn(ctx, "demo-group", roster, target, "👍", "add", a.Version, "group-reaction-request-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, p, _ := e.Store.JobPlan(rid); p.Roster != roster || p.Mutation == nil {
		t.Fatalf("reaction plan: %+v", p)
	}
	if err = e.Dispatch(ctx); err != nil {
		t.Fatal(err)
	}
	if status, err := e.ReactionStatus("demo-group", rid); err != nil || status["state"] != "submitted" {
		t.Fatalf("reaction: %v %v", status, err)
	}
}

func TestAGroupThatChangedBeforeAReactionGoesGetsNoReaction(t *testing.T) {
	e, f, roster := groupSetup(t)
	ctx := context.Background()
	m := groupMessage("synthetic-group-change")
	if err := e.Collection.Observe(ctx, m); err != nil {
		t.Fatal(err)
	}
	target := store.Hash("demo-group\x00" + m.ID)
	a, _ := e.MessageActions(ctx, "demo-group", target)
	rid, err := e.QueueReactionIn(ctx, "demo-group", roster, target, "👍", "add", a.Version, "group-reaction-request-2")
	if err != nil {
		t.Fatal(err)
	}
	f.SetGroupMembers("demo-member-a", "demo-member-b", "demo-member-c")
	if err = e.Dispatch(ctx); err != nil {
		t.Fatal(err)
	}
	if j, _, _ := e.Store.JobPlan(rid); j.State != "cancelled" || j.Code != "identity_changed" || len(f.Sent) != 0 {
		t.Fatalf("job %s %s, sent %v", j.State, j.Code, f.Sent)
	}
	// Told to react to the old people, the engine refuses outright.
	if _, err = e.QueueReactionIn(ctx, "demo-group", roster, target, "❤️", "add", a.Version, "group-reaction-request-3"); err == nil || err.Error() != "group_changed" {
		t.Fatalf("queued for people who are no longer there: %v", err)
	}
}
