package transport

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"go.mau.fi/mautrix-gmessages/pkg/libgm/gmproto"
	"google.golang.org/protobuf/proto"
)

func individual(id string) *gmproto.Conversation {
	return &gmproto.Conversation{
		ConversationID: id, DefaultOutgoingID: "self",
		Participants: []*gmproto.Participant{{ID: &gmproto.SmallInfo{ParticipantID: "peer"}, IsVisible: true}},
	}
}

// A list record carries everything listing needs, so no per-conversation
// lookup is made; groups and read-only conversations are labelled as such.
func TestListConversationProjectsListRecord(t *testing.T) {
	t.Parallel()
	c := individual("chat-1")
	c.Name = "Synthetic Name"
	c.LastMessageTimestamp = 1_700_000_000_000_000
	got := listConversation(c)
	if got.ID != "chat-1" || got.ParticipantID != "peer" || got.Label != "Synthetic Name" || !got.Writable || got.Group || got.LastMessageAt == "" {
		t.Fatalf("unexpected projection: %+v", got)
	}
	group := individual("group-1")
	group.IsGroupChat = true
	if g := listConversation(group); !g.Writable || !g.Group || g.Label != "Group conversation" {
		t.Fatalf("group not listed as sendable: %+v", g)
	}
	readOnly := individual("ro-1")
	readOnly.ReadOnly = true
	if r := listConversation(readOnly); r.Writable || r.Label != "Individual conversation" {
		t.Fatalf("read-only listed as writable: %+v", r)
	}
}

func TestConversationEventKeepsSendBaselineOnIdentityChange(t *testing.T) {
	t.Parallel()
	g := NewGoogle(nil, "")
	c := individual("changed")
	g.cacheDetail(c)
	changed := proto.Clone(c).(*gmproto.Conversation)
	changed.IsGroupChat = true
	if !g.rememberConversationEvent(changed) {
		t.Fatal("identity change did not require pause")
	}
	if g.conversations["changed"] != c {
		t.Fatal("identity change replaced send baseline")
	}
	renamed := proto.Clone(c).(*gmproto.Conversation)
	renamed.Name = "Synthetic new label"
	if g.rememberConversationEvent(renamed) || g.conversations["changed"] != renamed {
		t.Fatal("ordinary update not applied")
	}
}

func TestCacheListedPreservesIdentityAndRosterBaselines(t *testing.T) {
	t.Parallel()
	g := NewGoogle(nil, "")
	g.ready.Store(true)
	g.connected = func() bool { return true }
	c := individual("chat-1")
	g.mu.Lock()
	g.updateBaselineLocked(c, false)
	g.mu.Unlock()
	if got, ok := g.CachedConversation("chat-1"); !ok || got.ParticipantID != "peer" {
		t.Fatalf("expected listed conversation in cache, got %+v ok=%v", got, ok)
	}

	// A peer or SIM change in a list page must mark CachedConversation stale while
	// keeping the old baseline so rememberConversationEvent still triggers pause.
	peerChanged := proto.Clone(c).(*gmproto.Conversation)
	peerChanged.Participants[0].ID.ParticipantID = "stranger"
	g.mu.Lock()
	g.updateBaselineLocked(peerChanged, false)
	g.mu.Unlock()
	if _, ok := g.CachedConversation("chat-1"); ok {
		t.Fatal("CachedConversation served pre-change record after identity changed")
	}
	if !g.rememberConversationEvent(peerChanged) {
		t.Fatal("discovery list overwrite hid identity change from rememberConversationEvent")
	}

	grp := group("g1", "a", "b")
	g.mu.Lock()
	g.updateBaselineLocked(grp, false)
	moved := group("g1", "a", "b", "c")
	g.updateBaselineLocked(moved, false)
	g.mu.Unlock()
	if roster(g.conversations["g1"]) != roster(grp) {
		t.Fatal("discovery list overwrite replaced group send baseline")
	}
	if _, ok := g.CachedConversation("g1"); ok {
		t.Fatal("CachedConversation served pre-change group roster")
	}
	// A verified single-conversation lookup clears the stale mark.
	g.cacheDetail(moved)
	if got, ok := g.CachedConversation("g1"); !ok || got.Roster != roster(moved) {
		t.Fatalf("verified cacheDetail did not refresh CachedConversation: %+v ok=%v", got, ok)
	}
}

func TestOutgoingIdentitySnapshottedWithoutJSONExposure(t *testing.T) {
	t.Parallel()
	c := individual("chat-1")
	c.DefaultOutgoingID = "synthetic-secret-sim-1"
	listed := listConversation(c)
	detail := detailConversation("chat-1", c)
	if listed.OutgoingID != "synthetic-secret-sim-1" || detail.OutgoingID != "synthetic-secret-sim-1" {
		t.Fatalf("outgoing identity not projected: %q %q", listed.OutgoingID, detail.OutgoingID)
	}
	b, err := json.Marshal(listed)
	if err != nil || strings.Contains(string(b), "synthetic-secret-sim-1") || strings.Contains(string(b), "outgoing") {
		t.Fatalf("outgoing identity leaked in JSON: %s %v", string(b), err)
	}
}

func TestVerifySendTargetChecksAcceptedOutgoingIdentityAndLegacyBaseline(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	g := NewGoogle(nil, "")
	initial := individual("chat-1")
	initial.DefaultOutgoingID = "synthetic-sim-1"
	g.cacheDetail(initial)
	accepted := detailConversation("chat-1", initial)

	// Simulate Engine.check running LookupConversation right before send after
	// the phone switched the conversation's sending SIM: cacheDetail overwrites
	// g.conversations["chat-1"], yet the snapshot from acceptance still refuses.
	switched := proto.Clone(initial).(*gmproto.Conversation)
	switched.DefaultOutgoingID = "synthetic-sim-2"
	g.cacheDetail(switched)

	if got := verifySendTarget(WithOutgoingID(ctx, accepted.OutgoingID), switched, g.conversations["chat-1"]); got != "outgoing_identity_changed" {
		t.Fatalf("switched outgoing identity allowed: %q", got)
	}
	if got := verifySendTarget(WithOutgoingID(ctx, accepted.OutgoingID), initial, initial); got != "" {
		t.Fatalf("unchanged outgoing identity refused: %q", got)
	}

	// Plans accepted before snapshots existed have OutgoingID == "" and keep
	// comparing against the cached baseline.
	legacyCtx := WithOutgoingID(ctx, "")
	if got := verifySendTarget(legacyCtx, switched, switched); got != "" {
		t.Fatalf("legacy plan refused when baseline matched: %q", got)
	}
	if got := verifySendTarget(legacyCtx, switched, initial); got != "outgoing_identity_changed" {
		t.Fatalf("legacy plan missed baseline mismatch: %q", got)
	}

	// Participant and group roster checks remain enforced even when OutgoingID matches.
	otherPeer := proto.Clone(initial).(*gmproto.Conversation)
	otherPeer.Participants[0].ID.ParticipantID = "other-peer"
	if got := verifySendTarget(WithOutgoingID(ctx, accepted.OutgoingID), otherPeer, initial); got != "participant_changed" {
		t.Fatalf("participant check changed: %q", got)
	}
	grp := group("g1", "a", "b")
	movedGrp := group("g1", "a", "b", "c")
	if got := verifySendTarget(WithOutgoingID(WithRoster(ctx, roster(movedGrp)), "self"), movedGrp, grp); got != "roster_changed" {
		t.Fatalf("roster baseline check changed: %q", got)
	}
	if got := verifySendTarget(WithOutgoingID(WithRoster(ctx, roster(grp)), "self"), movedGrp, movedGrp); got != "roster_mismatch" {
		t.Fatalf("expected roster check changed: %q", got)
	}

	// Offline Fixture mirrors the snapshot refusal and legacy fallback.
	f := NewFixture()
	convs, err := f.Conversations(ctx)
	if err != nil {
		t.Fatal(err)
	}
	snap := convs[0].OutgoingID
	f.SetOutgoingID("synthetic-sim-2")
	_, err = f.Send(WithOutgoingID(ctx, snap), "demo-chat", "txn-refused", "hello")
	if !errors.Is(err, ErrNotSubmitted) || RefusalReason(err) != "outgoing_identity_changed" {
		t.Fatalf("fixture send did not refuse changed outgoing identity: %v (%q)", err, RefusalReason(err))
	}
	if _, err := f.Send(WithOutgoingID(ctx, ""), "demo-chat", "txn-legacy", "hello"); err != nil {
		t.Fatalf("fixture rejected legacy plan without snapshot: %v", err)
	}
}

func TestLookupConversationIDMismatchReturnsTypedIneligibleError(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for _, tc := range []struct {
		name string
		conv *gmproto.Conversation
	}{
		{"different_id", individual("other-chat")},
		{"empty_id", individual("")},
		{"nil_record", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			g := NewGoogle(nil, "")
			g.ready.Store(true)
			g.connected = func() bool { return true }
			g.getConversation = func(context.Context, string) (*gmproto.Conversation, error) {
				return tc.conv, nil
			}
			_, err := g.LookupConversation(ctx, "chat-1")
			if !errors.Is(err, ErrIneligibleConversation) || errors.Is(err, ErrLookupFailed) || errors.Is(err, ErrUnavailable) {
				t.Fatalf("%s returned %v, want ErrIneligibleConversation", tc.name, err)
			}
			if len(g.conversations) != 0 {
				t.Fatalf("%s cached mismatched conversation baseline: %+v", tc.name, g.conversations)
			}
		})
	}
}
