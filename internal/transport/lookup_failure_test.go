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
