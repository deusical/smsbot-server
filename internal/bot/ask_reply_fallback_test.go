package bot

import (
	"context"
	"errors"
	"testing"

	"github.com/deusical/smsbot-server/internal/media"
	"github.com/deusical/smsbot-server/internal/store"
	"github.com/deusical/smsbot-server/internal/transport"
)

type askReplyTransport struct {
	*transport.Fixture
	replyErr               error
	replyCalls, plainCalls int
	replyTxn, plainTxn     string
}

func (f *askReplyTransport) SendReply(ctx context.Context, conv, txn, text, target string, files []media.File) (transport.Receipt, error) {
	f.replyCalls++
	f.replyTxn = txn
	if f.replyErr != nil {
		return transport.Receipt{}, f.replyErr
	}
	return f.Fixture.SendReply(ctx, conv, txn, text, target, files)
}

func (f *askReplyTransport) Send(ctx context.Context, conv, txn, text string) (transport.Receipt, error) {
	f.plainCalls++
	f.plainTxn = txn
	return f.Fixture.Send(ctx, conv, txn, text)
}

func TestAskReplyFallback(t *testing.T) {
	for _, tc := range []struct {
		name                     string
		replyErr                 error
		plainFail                string
		noReplySender            bool
		state                    string
		plainCalls, sent, quotes int
	}{
		{name: "native", state: "submitted", sent: 1, quotes: 1},
		{name: "definite refusal", replyErr: transport.ErrNotSubmitted, state: "submitted", plainCalls: 1, sent: 1},
		{name: "no native reply support", noReplySender: true, state: "submitted", plainCalls: 1, sent: 1},
		{name: "uncertain reply", replyErr: transport.ErrUnknown, state: "unknown_submission"},
		{name: "unclassified reply error", replyErr: errors.New("synthetic"), state: "unknown_submission"},
		{name: "plain refusal", replyErr: transport.ErrNotSubmitted, plainFail: "refused", state: "failed", plainCalls: 1},
		{name: "uncertain plain", replyErr: transport.ErrNotSubmitted, plainFail: "unknown", state: "unknown_submission", plainCalls: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, fixture := setup(t)
			arm(t, e)
			configureAI(t, e, true, false)
			e.Provider = &transcriptProvider{}
			tr := &askReplyTransport{Fixture: fixture, replyErr: tc.replyErr}
			e.Transport = tr
			wantReplyCalls := 1
			if tc.noReplySender {
				e.Transport = struct{ transport.Transport }{tr}
				wantReplyCalls = 0
			}
			m := threadPrompt("synthetic-root", "!ask Synthetic question", "")
			threadAccept(t, e, m)
			fixture.Fail = tc.plainFail
			if err := e.Dispatch(context.Background()); err != nil {
				t.Fatal(err)
			}
			id := store.Hash(m.ConversationID + "\x00" + m.ID)
			if got := stateOf(t, e, id); got != tc.state {
				t.Fatalf("state=%s, want %s", got, tc.state)
			}
			if tr.replyCalls != wantReplyCalls || tr.plainCalls != tc.plainCalls || len(fixture.Sent) != tc.sent {
				t.Fatalf("reply=%d plain=%d sent=%d", tr.replyCalls, tr.plainCalls, len(fixture.Sent))
			}
			if tc.plainCalls > 0 && !tc.noReplySender && tr.plainTxn != tr.replyTxn {
				t.Fatal("fallback changed the recorded transaction")
			}
			var quotes int
			if err := e.Store.DB.QueryRow("SELECT count(*) FROM message_interactions").Scan(&quotes); err != nil || quotes != tc.quotes {
				t.Fatalf("quotes=%d, want %d: %v", quotes, tc.quotes, err)
			}
			for _, text := range fixture.Sent {
				if text != "AI: Synthetic question" {
					t.Fatal("answer changed or quote prefix was added")
				}
			}
			if tc.state == "submitted" {
				threadEcho(t, e, m, "synthetic-answer")
				p := threadAccept(t, e, threadPrompt("synthetic-followup", "Continue", "synthetic-answer"))
				if len(p.Context) != 2 || p.Thread.Parent != id {
					t.Fatal("answer lost its AI thread")
				}
			}
			// Dispatching again must not retry any terminal send outcome.
			if tc.state != "submitted" {
				if err := e.Dispatch(context.Background()); err != nil || tr.replyCalls != 1 || tr.plainCalls != tc.plainCalls {
					t.Fatal("terminal send retried", err)
				}
			}
		})
	}
}
