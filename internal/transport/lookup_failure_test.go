package transport

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.mau.fi/mautrix-gmessages/pkg/libgm/events"
	"go.mau.fi/mautrix-gmessages/pkg/libgm/gmproto"
)

func TestFetchConversationRetriesOnceThenGivesUp(t *testing.T) {
	boom := errors.New("transient")
	cases := []struct {
		name  string
		fails int
		err   error
		want  int
		ok    bool
	}{
		{"first try works", 0, boom, 1, true},
		{"second try works", 1, boom, 2, true},
		{"fails twice", 2, boom, 2, false},
		{"cancelled is not retried", 5, context.Canceled, 1, false},
		{"login problem is not retried", 5, events.ErrInvalidCredentials, 1, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			calls := 0
			got, err := fetchConversation(context.Background(), "c1", time.Millisecond, func(context.Context, string) (*gmproto.Conversation, error) {
				calls++
				if calls <= c.fails {
					return nil, c.err
				}
				return &gmproto.Conversation{ConversationID: "c1"}, nil
			})
			if calls != c.want || (err == nil) != c.ok || (c.ok && got.GetConversationID() != "c1") {
				t.Fatalf("calls=%d err=%v got=%v", calls, err, got)
			}
		})
	}
}

func TestFetchConversationStopsWaitingWhenCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	_, err := fetchConversation(ctx, "c1", time.Hour, func(context.Context, string) (*gmproto.Conversation, error) {
		calls++
		cancel()
		return nil, errors.New("transient")
	})
	if err == nil || calls != 1 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}

func TestLookupFailureOnlyDropsThePhoneWhenTheConnectionIsAtFault(t *testing.T) {
	cases := []struct {
		name      string
		err       error
		connected bool
		reason    string
		hard      bool
		offline   bool
	}{
		{"one unanswered lookup while connected", errors.New("transient"), true, "", false, false},
		{"connection is down", errors.New("transient"), false, "reconnecting", false, true},
		{"phone did not answer in time", context.DeadlineExceeded, true, "reconnecting", false, true},
		{"login problem", events.ErrInvalidCredentials, true, "login_required", true, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			reason, hard, offline := lookupFailure(c.err, c.connected)
			if reason != c.reason || hard != c.hard || offline != c.offline {
				t.Fatalf("got %q %v %v", reason, hard, offline)
			}
		})
	}
}

func TestLookupErrorsDistinguishUnreadyTransportFromFailedRead(t *testing.T) {
	t.Parallel()
	if errors.Is(ErrLookupFailed, ErrUnavailable) || errors.Is(ErrUnavailable, ErrLookupFailed) {
		t.Fatal("lookup failure while ready must not match ErrUnavailable")
	}
	g := NewGoogle(nil, "")
	if _, err := g.LookupConversation(context.Background(), "c1"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("unready lookup returned %v, want ErrUnavailable", err)
	}
}

func TestFetchHistoryDistinguishesIneligibleConversationFromLookupFailure(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	unready := NewGoogle(nil, "")
	if _, err := unready.FetchHistory(ctx, "c1", "peer", ""); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("unready FetchHistory returned %v, want ErrUnavailable", err)
	}

	g := NewGoogle(nil, "")
	g.ready.Store(true)
	g.connected = func() bool { return true }
	noMessages := func(context.Context, string, int64, *gmproto.Cursor) (*gmproto.ListMessagesResponse, error) {
		return &gmproto.ListMessagesResponse{}, nil
	}

	// Lookup error (network/phone timeout while transport is otherwise up) must
	// return ErrLookupFailed and never ErrIneligibleConversation.
	_, err := g.fetchHistory(ctx, "c1", "peer", "", func(context.Context, string) (*gmproto.Conversation, error) {
		return nil, errors.New("phone rpc timeout")
	}, noMessages)
	if !errors.Is(err, ErrLookupFailed) || errors.Is(err, ErrIneligibleConversation) || errors.Is(err, ErrUnavailable) {
		t.Fatalf("lookup error returned %v, want ErrLookupFailed", err)
	}

	// Phone answered, but conversation is deleted/empty, read-only, has no sending SIM,
	// or belongs to a different participant: all return ErrIneligibleConversation.
	readOnly := individual("c1")
	readOnly.ReadOnly = true
	noSIM := individual("c1")
	noSIM.DefaultOutgoingID = ""
	wrongPeer := individual("c1")
	wrongPeer.Participants[0].ID.ParticipantID = "other-peer"
	for _, tc := range []struct {
		name string
		conv *gmproto.Conversation
	}{
		{"deleted", nil},
		{"read_only", readOnly},
		{"no_sim", noSIM},
		{"peer_mismatch", wrongPeer},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := g.fetchHistory(ctx, "c1", "peer", "", func(context.Context, string) (*gmproto.Conversation, error) {
				return tc.conv, nil
			}, noMessages)
			if !errors.Is(err, ErrIneligibleConversation) {
				t.Fatalf("%s returned %v, want ErrIneligibleConversation", tc.name, err)
			}
		})
	}

	// Eligible conversation succeeds.
	if _, err := g.fetchHistory(ctx, "c1", "peer", "", func(context.Context, string) (*gmproto.Conversation, error) {
		return individual("c1"), nil
	}, noMessages); err != nil {
		t.Fatalf("eligible conversation failed: %v", err)
	}
}
