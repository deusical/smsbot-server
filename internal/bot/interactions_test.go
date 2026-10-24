package bot

import (
	"context"
	"github.com/deusical/smsbot-server/internal/store"
	"github.com/deusical/smsbot-server/internal/transport"
	"testing"
	"time"
)

func interactionMessage(id, protocol string) transport.Incoming {
	return transport.Incoming{ID: id, ConversationID: "demo-chat", ParticipantID: "demo-peer", Text: "Synthetic interaction", At: time.Now(), Interaction: &transport.MessageInteraction{UpstreamID: id, Protocol: protocol, OwnerID: "fixture-self", ObservedAt: time.Now(), Reactions: []transport.Reaction{}}}
}
func TestReplyProtocolAndIntentIdentity(t *testing.T) {
	e, f := setup(t)
	enableInbox(t, e)
	e.Pause(false)
	ctx := context.Background()
	for _, protocol := range []string{"RCS", "SMS", "MMS", "unknown"} {
		m := interactionMessage("synthetic-"+protocol, protocol)
		if err := e.Collection.Observe(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	for _, protocol := range []string{"SMS", "MMS", "unknown"} {
		target := store.Hash("demo-chat\x00synthetic-" + protocol)
		a, err := e.MessageActions(ctx, "demo-chat", target)
		if err != nil || a.Reply || a.React || a.Edit {
			t.Fatal("non-RCS action allowed", protocol, err)
		}
		if _, err = e.ComposeInboxFiles(ctx, "demo-chat", "Synthetic reply", "synthetic-reply-request-"+protocol, nil, target); err == nil {
			t.Fatal("non-RCS quoted send allowed")
		}
	}
	target := store.Hash("demo-chat\x00synthetic-RCS")
	id, err := e.ComposeInboxFiles(ctx, "demo-chat", "Synthetic reply", "synthetic-reply-request", nil, target)
	if err != nil {
		t.Fatal(err)
	}
	_, plan, err := e.Store.JobPlan(id)
	if err != nil || plan.ReplyWireTarget != "synthetic-RCS" {
		t.Fatal("reply target not persisted", err)
	}
	if _, err = e.ComposeInboxFiles(ctx, "demo-chat", "Synthetic reply", "synthetic-reply-request", nil, ""); err == nil {
		t.Fatal("changed target reused request ID")
	}
	if _, err = e.ComposeInboxFiles(ctx, "demo-chat", "Synthetic reply", "synthetic-cross-thread", nil, store.Hash("other\x00synthetic-RCS")); err == nil {
		t.Fatal("cross-thread reply accepted")
	}
	if err = e.Dispatch(ctx); err != nil {
		t.Fatal(err)
	}
	if len(f.Sent) != 1 {
		t.Fatal("reply not submitted")
	}
	e.Pause(true)
	again, err := e.ComposeInboxFiles(ctx, "demo-chat", "Synthetic reply", "synthetic-reply-request", nil, target)
	if err != nil || again != id {
		t.Fatal("retry lost original intent", err)
	}
	p, err := e.Store.OwnerMessagePage("demo-chat", "", "", 50)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range p.Messages {
		if m.Quote != nil && m.Quote.ID == target {
			found = true
		}
	}
	if !found {
		t.Fatal("sent reply relationship lost")
	}
}

func TestReactionIntentReconciliationAndUncertainty(t *testing.T) {
	for _, failure := range []string{"", "unknown", "definite"} {
		t.Run("result_"+failure, func(t *testing.T) {
			e, f := setup(t)
			enableInbox(t, e)
			e.Pause(false)
			ctx := context.Background()
			m := interactionMessage("synthetic-react", "RCS")
			if err := e.Collection.Observe(ctx, m); err != nil {
				t.Fatal(err)
			}
			target := store.Hash("demo-chat\x00" + m.ID)
			a, err := e.MessageActions(ctx, "demo-chat", target)
			if err != nil || !a.React {
				t.Fatal("reaction capability absent", err)
			}
			id, err := e.QueueReaction(ctx, "demo-chat", target, "👍", "add", a.Version, "synthetic-reaction-request")
			if err != nil {
				t.Fatal(err)
			}
			if _, err = e.QueueReaction(ctx, "demo-chat", target, "❤️", "add", a.Version, "synthetic-reaction-request"); err == nil {
				t.Fatal("changed intent accepted")
			}
			if _, err = e.QueueReaction(ctx, "demo-chat", target, "❤️", "add", a.Version, "synthetic-conflicting-request"); err == nil {
				t.Fatal("conflicting action accepted")
			}
			f.Fail = failure
			if err = e.Dispatch(ctx); err != nil {
				t.Fatal(err)
			}
			expected := map[string]string{"": "submitted", "unknown": "unknown_submission", "definite": "failed"}[failure]
			status, err := e.ReactionStatus("demo-chat", id)
			if err != nil || status["state"] != expected {
				t.Fatal("wrong submission outcome", status, err)
			}
			again, err := e.QueueReaction(ctx, "demo-chat", target, "👍", "add", a.Version, "synthetic-reaction-request")
			if err != nil || again != id {
				t.Fatal("action retry not idempotent", err)
			}
			if err = e.Dispatch(ctx); err != nil {
				t.Fatal(err)
			}
			if failure == "definite" {
				return
			}
			// A stale read cannot confirm the action or overwrite its newer snapshot.
			m.Interaction.Reactions = []transport.Reaction{{Emoji: "👍", Participants: []string{"fixture-self"}}}
			m.Interaction.ObservedAt = time.Now().Add(-time.Minute)
			e.Collection.Observe(ctx, m)
			status, _ = e.ReactionStatus("demo-chat", id)
			if status["state"] != expected {
				t.Fatal("stale scan confirmed action")
			}
			m.Interaction.ObservedAt = time.Now()
			if err = e.Collection.Observe(ctx, m); err != nil {
				t.Fatal(err)
			}
			status, _ = e.ReactionStatus("demo-chat", id)
			if status["state"] != "reaction_confirmed" {
				t.Fatal("phone snapshot did not reconcile", status)
			}
			a, _ = e.MessageActions(ctx, "demo-chat", target)
			if _, err = e.QueueReaction(ctx, "demo-chat", target, "👍", "remove", a.Version, "synthetic-remove-request"); err != nil {
				t.Fatal("remove after confirmation unavailable", err)
			}
		})
	}
}

func TestReactionCanonicalEmojiConfirmation(t *testing.T) {
	e, _ := setup(t)
	enableInbox(t, e)
	e.Pause(false)
	ctx := context.Background()
	m := interactionMessage("synthetic-heart", "RCS")
	e.Collection.Observe(ctx, m)
	target := store.Hash("demo-chat\x00" + m.ID)
	a, _ := e.MessageActions(ctx, "demo-chat", target)
	id, err := e.QueueReaction(ctx, "demo-chat", target, "❤", "add", a.Version, "synthetic-heart-request")
	if err != nil {
		t.Fatal(err)
	}
	if err = e.Dispatch(ctx); err != nil {
		t.Fatal(err)
	}
	m.Interaction.ObservedAt = time.Now()
	m.Interaction.Reactions = []transport.Reaction{{Emoji: "❤️", Participants: []string{"fixture-self"}}}
	if err = e.Collection.Observe(ctx, m); err != nil {
		t.Fatal(err)
	}
	result, _ := e.ReactionStatus("demo-chat", id)
	if result["state"] != "reaction_confirmed" {
		t.Fatal("equivalent emoji form not reconciled")
	}
}

func TestReactionFinalChecksAndRestart(t *testing.T) {
	e, _ := setup(t)
	enableInbox(t, e)
	e.Pause(false)
	ctx := context.Background()
	m := interactionMessage("synthetic-check", "RCS")
	e.Collection.Observe(ctx, m)
	target := store.Hash("demo-chat\x00" + m.ID)
	a, _ := e.MessageActions(ctx, "demo-chat", target)
	id, err := e.QueueReaction(ctx, "demo-chat", target, "👍", "add", a.Version, "synthetic-final-request")
	if err != nil {
		t.Fatal(err)
	}
	m.Interaction.Reactions = []transport.Reaction{{Emoji: "❤️", Participants: []string{"fixture-self"}}}
	m.Interaction.ObservedAt = time.Now()
	e.Collection.Observe(ctx, m)
	if err = e.Dispatch(ctx); err != nil {
		t.Fatal(err)
	}
	status, _ := e.ReactionStatus("demo-chat", id)
	if status["state"] != "failed" {
		t.Fatal("stale intent submitted")
	}
	a, _ = e.MessageActions(ctx, "demo-chat", target)
	id, err = e.QueueReaction(ctx, "demo-chat", target, "👍", "switch", a.Version, "synthetic-restart-request")
	if err != nil {
		t.Fatal(err)
	}
	intent, err := e.Store.Mutation(id)
	if err != nil {
		t.Fatal(err)
	}
	if err = e.Store.StartMutation(id, intent); err != nil {
		t.Fatal(err)
	}
	e.Store.Recover()
	status, _ = e.ReactionStatus("demo-chat", id)
	if status["state"] != "unknown_submission" {
		t.Fatal("restart replayable")
	}
	if _, err = e.QueueReaction(ctx, "demo-chat", target, "👍", "switch", a.Version, "synthetic-after-restart"); err == nil {
		t.Fatal("uncertain target not locked")
	}
}

type changedProtocolFixture struct {
	*transport.Fixture
	protocol    string
	participant string
	group       bool
}

func (f *changedProtocolFixture) Conversations(ctx context.Context) ([]transport.Conversation, error) {
	return []transport.Conversation{{ID: "demo-chat", ParticipantID: f.participant, Protocol: f.protocol, Writable: true, Group: f.group}}, nil
}
func TestInteractionSubmissionRechecksPolicy(t *testing.T) {
	for _, change := range []string{"paused", "consent", "protocol", "identity", "group"} {
		t.Run(change, func(t *testing.T) {
			e, f := setup(t)
			enableInbox(t, e)
			e.Pause(false)
			ctx := context.Background()
			wrapper := &changedProtocolFixture{Fixture: f, protocol: "RCS", participant: "demo-peer"}
			useTransport(e, wrapper)
			m := interactionMessage("synthetic-policy", "RCS")
			if err := e.Collection.Observe(ctx, m); err != nil {
				t.Fatal(err)
			}
			target := store.Hash("demo-chat\x00" + m.ID)
			a, _ := e.MessageActions(ctx, "demo-chat", target)
			if _, err := e.QueueReaction(ctx, "demo-chat", target, "👍", "add", a.Version, "synthetic-policy-request"); err != nil {
				t.Fatal(err)
			}
			switch change {
			case "paused":
				e.Pause(true)
			case "consent":
				s, _ := e.Collection.Settings()
				s.Enabled = false
				e.Collection.SetSettings(ctx, s)
			case "protocol":
				wrapper.protocol = "SMS"
			case "identity":
				wrapper.participant = "different-peer"
			case "group":
				wrapper.group = true
			}
			if err := e.Dispatch(ctx); err != nil {
				t.Fatal(err)
			}
			if len(f.Sent) != 0 {
				t.Fatal("reaction submitted after policy changed")
			}
		})
	}
}

func TestObservedEditOrReactionNeverExecutesCommand(t *testing.T) {
	e, _ := setup(t)
	enableInbox(t, e)
	arm(t, e)
	ctx := context.Background()
	m := interactionMessage("synthetic-replay", "RCS")
	if accepted, err := e.Receive(ctx, m); err != nil || accepted {
		t.Fatal("ordinary message became command", err)
	}
	m.Text = "!ping"
	m.Interaction.ObservedAt = time.Now()
	m.Interaction.Reactions = []transport.Reaction{{Emoji: "👍", Participants: []string{"demo-peer"}}}
	if accepted, err := e.Receive(ctx, m); err != nil || accepted {
		t.Fatal("observed edit executed command", err)
	}
	p, err := e.Store.OwnerMessagePage("demo-chat", "", "", 50)
	if err != nil || len(p.Messages) != 1 || p.Messages[0].Revision != 1 {
		t.Fatal("observed edit not retained", err)
	}
	jobs, _ := e.Store.Jobs()
	if len(jobs) != 0 {
		t.Fatal("edit or reaction enqueued a bot command")
	}
}

func TestOldOutgoingReactionObservation(t *testing.T) {
	for _, queued := range []bool{false, true} {
		t.Run(map[bool]string{false: "receive", true: "capture_worker"}[queued], func(t *testing.T) {
			e, _ := setup(t)
			enableInbox(t, e)
			arm(t, e)
			ctx := context.Background()
			m := interactionMessage("synthetic-old-outgoing", "RCS")
			m.Outgoing = true
			m.Historical = true
			m.At = time.Now().Add(-10 * time.Minute)
			if err := e.Collection.Observe(ctx, m); err != nil {
				t.Fatal(err)
			}
			m.Historical = false
			m.Text = "!ping"
			m.Interaction.ObservedAt = time.Now()
			m.Interaction.Reactions = []transport.Reaction{{Emoji: "👍", Participants: []string{"demo-peer"}}}
			if queued {
				if accepted, err := e.Ingest(ctx, m); err != nil || accepted {
					t.Fatal("outgoing snapshot became command", err)
				}
				// The capture worker rejects delayed live messages, so the
				// snapshot only lands if it was queued as historical.
				workerCtx, stop := context.WithCancel(ctx)
				done := make(chan struct{})
				go func() { defer close(done); e.Collection.RunCapture(workerCtx) }()
				deadline := time.Now().Add(5 * time.Second)
				for {
					p, _ := e.Store.OwnerMessagePage("demo-chat", "", "", 50)
					if len(p.Messages) == 1 && len(p.Messages[0].Reactions) == 1 {
						break
					}
					if time.Now().After(deadline) {
						t.Fatal("queued snapshot not captured")
					}
					time.Sleep(10 * time.Millisecond)
				}
				stop()
				<-done
			} else if accepted, err := e.Receive(ctx, m); err != nil || accepted {
				t.Fatal("outgoing snapshot became command", err)
			}
			p, err := e.Store.OwnerMessagePage("demo-chat", "", "", 50)
			if err != nil || len(p.Messages) != 1 || len(p.Messages[0].Reactions) != 1 {
				t.Fatal("old outgoing reaction lost", err)
			}
			jobs, _ := e.Store.Jobs()
			if len(jobs) != 0 {
				t.Fatal("outgoing snapshot executed command")
			}
		})
	}
}

type ownerResolvingFixture struct {
	*transport.Fixture
	lookedUp bool
}

func (f *ownerResolvingFixture) Conversations(ctx context.Context) ([]transport.Conversation, error) {
	f.lookedUp = true
	return f.Fixture.Conversations(ctx)
}
func (f *ownerResolvingFixture) InteractionOwner(conv, peer string) (string, error) {
	if !f.lookedUp || conv != "demo-chat" || peer != "demo-peer" {
		return "", transport.ErrUnavailable
	}
	return "fixture-self", nil
}
func TestUncachedInteractionOwnerResolvedAfterVerifiedLookup(t *testing.T) {
	e, f := setup(t)
	enableInbox(t, e)
	e.Pause(false)
	ctx := context.Background()
	source := &ownerResolvingFixture{Fixture: f}
	useTransport(e, source)
	m := interactionMessage("synthetic-uncached", "RCS")
	m.Interaction.OwnerID = ""
	if err := e.Collection.Observe(ctx, m); err != nil {
		t.Fatal(err)
	}
	target := store.Hash("demo-chat\x00" + m.ID)
	v, err := e.Store.Interaction("demo-chat", target)
	if err != nil || v == nil || v.OwnerID != "fixture-self" {
		t.Fatal("owner not resolved", err)
	}
	if m.Interaction.OwnerID != "" {
		t.Fatal("shared transport snapshot mutated")
	}
	a, err := e.MessageActions(ctx, "demo-chat", target)
	if err != nil || !a.React {
		t.Fatal("reactions disabled after owner lookup", err)
	}
	id, err := e.QueueReaction(ctx, "demo-chat", target, "👍", "add", a.Version, "synthetic-resolved-request")
	if err != nil {
		t.Fatal(err)
	}
	if err = e.Dispatch(ctx); err != nil {
		t.Fatal(err)
	}
	m.Interaction.ObservedAt = time.Now()
	m.Interaction.Reactions = []transport.Reaction{{Emoji: "👍", Participants: []string{"fixture-self"}}}
	if err = e.Collection.Observe(ctx, m); err != nil {
		t.Fatal(err)
	}
	status, err := e.ReactionStatus("demo-chat", id)
	if err != nil || status["state"] != "reaction_confirmed" {
		t.Fatal("owner-less stream snapshot not reconciled", err)
	}
}
