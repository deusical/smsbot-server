package transport

import (
	"context"
	"testing"

	"go.mau.fi/mautrix-gmessages/pkg/libgm/gmproto"
)

// TestD1VerifySendTargetIdentityChange simulates:
// 1. Conversation baseline known (in g.conversations)
// 2. Live phone record changes participant, roster (group) or outgoing ID
// 3. LookupConversation (which calls cacheDetail) runs as compose/check would
// 4. Send is attempted
//
// This test documents whether verifySendTarget really catches the change
// or if cacheDetail overwrites the baseline beforehand, rendering the guard ineffective.
func TestD1VerifySendTargetIdentityChange(t *testing.T) {
	t.Parallel()

	simCard := &gmproto.SIMCard{
		SIMData: &gmproto.SIMData{
			SIMPayload: &gmproto.SIMPayload{SIMNumber: 1},
		},
	}

	successSendMessage := func(ctx context.Context, req *gmproto.SendMessageRequest) (*gmproto.SendMessageResponse, error) {
		return &gmproto.SendMessageResponse{Status: gmproto.SendMessageResponse_SUCCESS}, nil
	}

	t.Run("participant changed", func(t *testing.T) {
		g := NewGoogle(nil, "")
		g.ready.Store(true)
		g.connected = func() bool { return true }

		// 1. Baseline known
		base := individual("c1")
		base.SimCard = simCard
		g.cacheDetail(base)

		// 2. Live phone record changes participant
		live := individual("c1")
		live.SimCard = simCard
		live.Participants = []*gmproto.Participant{{ID: &gmproto.SmallInfo{ParticipantID: "new-peer"}, IsVisible: true}}

		g.getConversation = func(ctx context.Context, id string) (*gmproto.Conversation, error) {
			return live, nil
		}

		// 3. LookupConversation runs as compose/check would
		_, err := g.LookupConversation(context.Background(), "c1")
		if err != nil {
			t.Fatalf("LookupConversation failed: %v", err)
		}

		// 4. Send
		var lastDiag SendDiagnostic
		g.SendLog = func(d SendDiagnostic) { lastDiag = d }
		_, sendErr := g.sendPartsWith(context.Background(), "c1", "txn1", "hi", nil, 0, g.conversationGetter(), successSendMessage)

		t.Logf("participant change outcome: err=%v, diag_result=%q", sendErr, lastDiag.Result)
	})

	t.Run("roster changed", func(t *testing.T) {
		g := NewGoogle(nil, "")
		g.ready.Store(true)
		g.connected = func() bool { return true }

		// 1. Baseline known
		base := group("g1", "a", "b")
		base.SimCard = simCard
		g.cacheDetail(base)

		// 2. Live phone record changes roster (new member joins)
		live := group("g1", "a", "b", "c")
		live.SimCard = simCard

		g.getConversation = func(ctx context.Context, id string) (*gmproto.Conversation, error) {
			return live, nil
		}

		// 3. LookupConversation runs
		_, err := g.LookupConversation(context.Background(), "g1")
		if err != nil {
			t.Fatalf("LookupConversation failed: %v", err)
		}

		// 4. Send without expected roster
		var lastDiag SendDiagnostic
		g.SendLog = func(d SendDiagnostic) { lastDiag = d }
		_, sendErr := g.sendPartsWith(context.Background(), "g1", "txn2", "hi", nil, 0, g.conversationGetter(), successSendMessage)

		t.Logf("roster change outcome: err=%v, diag_result=%q", sendErr, lastDiag.Result)
	})

	t.Run("outgoing identity changed", func(t *testing.T) {
		g := NewGoogle(nil, "")
		g.ready.Store(true)
		g.connected = func() bool { return true }

		// 1. Baseline known
		base := individual("c2")
		base.DefaultOutgoingID = "self-sim1"
		base.SimCard = simCard
		g.cacheDetail(base)

		// 2. Live phone record changes outgoing ID
		live := individual("c2")
		live.DefaultOutgoingID = "self-sim2"
		live.SimCard = simCard

		g.getConversation = func(ctx context.Context, id string) (*gmproto.Conversation, error) {
			return live, nil
		}

		// 3. LookupConversation runs
		_, err := g.LookupConversation(context.Background(), "c2")
		if err != nil {
			t.Fatalf("LookupConversation failed: %v", err)
		}

		// 4. Send without expected outgoing ID
		var lastDiag SendDiagnostic
		g.SendLog = func(d SendDiagnostic) { lastDiag = d }
		_, sendErr := g.sendPartsWith(context.Background(), "c2", "txn3", "hi", nil, 0, g.conversationGetter(), successSendMessage)

		t.Logf("outgoing identity change outcome: err=%v, diag_result=%q", sendErr, lastDiag.Result)
	})
}
