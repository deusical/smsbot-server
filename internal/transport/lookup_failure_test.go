package transport

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
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
		name    string
		err     error
		reason  string
		hard    bool
		offline bool
	}{
		{"one unanswered lookup leaves stream grace in charge", errors.New("transient"), "", false, false},
		{"single lookup timeout triggers probe instead of dropping phone directly", context.DeadlineExceeded, "", false, false},
		{"login problem", events.ErrInvalidCredentials, "login_required", true, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			reason, hard, offline := lookupFailure(c.err)
			if reason != c.reason || hard != c.hard || offline != c.offline {
				t.Fatalf("got %q %v %v", reason, hard, offline)
			}
		})
	}
}

func TestLookupConversationDuringStreamRolloverAndSessionFailures(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// Transports that do not implement SessionState fall back to Ready().
	if SessionUp(nil) {
		t.Fatal("nil transport reported SessionUp")
	}
	f := NewFixture()
	if !SessionUp(f) {
		t.Fatal("online fixture did not report SessionUp")
	}
	f.Online = false
	if SessionUp(f) {
		t.Fatal("offline fixture reported SessionUp")
	}

	// Stream rollover gap: session is up (ready && !healthFailure && !overflow),
	// while the long-polling stream is momentarily disconnected (!Ready()).
	t.Run("transient_failure_during_stream_gap_keeps_session_up", func(t *testing.T) {
		t.Parallel()
		g := NewGoogle(nil, "")
		g.ready.Store(true)
		g.lookupPause = time.Millisecond
		g.connected = func() bool { return false }
		calls := 0
		g.getConversation = func(context.Context, string) (*gmproto.Conversation, error) {
			calls++
			return nil, errors.New("synthetic transient lookup error")
		}
		if !g.SessionUp() || !SessionUp(g) || g.Ready() {
			t.Fatalf("unexpected readiness before lookup: SessionUp=%v Ready=%v", g.SessionUp(), g.Ready())
		}
		_, err := g.LookupConversation(ctx, "c1")
		if !errors.Is(err, ErrLookupFailed) || errors.Is(err, ErrUnavailable) {
			t.Fatalf("rollover lookup error = %v, want ErrLookupFailed", err)
		}
		if calls != 2 {
			t.Fatalf("fetchConversation calls = %d, want 2", calls)
		}
		if g.healthFailure.Load() || !g.ready.Load() || !g.SessionUp() {
			t.Fatalf("rollover lookup failure marked phone offline: healthFailure=%v ready=%v sessionUp=%v",
				g.healthFailure.Load(), g.ready.Load(), g.SessionUp())
		}
	})

	t.Run("lookup_succeeds_during_stream_gap", func(t *testing.T) {
		t.Parallel()
		g := NewGoogle(nil, "")
		g.ready.Store(true)
		g.lookupPause = time.Millisecond
		g.connected = func() bool { return false }
		calls := 0
		g.getConversation = func(context.Context, string) (*gmproto.Conversation, error) {
			calls++
			if calls == 1 {
				return nil, errors.New("synthetic transient")
			}
			return individual("c1"), nil
		}
		conv, err := g.LookupConversation(ctx, "c1")
		if err != nil || conv.ID != "c1" || conv.ParticipantID != "peer" || !conv.Writable {
			t.Fatalf("rollover lookup failed: conv=%+v err=%v", conv, err)
		}
		if !g.SessionUp() || g.healthFailure.Load() || !g.ready.Load() {
			t.Fatal("successful rollover lookup altered session health")
		}
	})

	t.Run("credential_error_notes_hard_failure", func(t *testing.T) {
		t.Parallel()
		g := NewGoogle(nil, "")
		g.ready.Store(true)
		g.lookupPause = time.Millisecond
		g.connected = func() bool { return false }
		g.getConversation = func(context.Context, string) (*gmproto.Conversation, error) {
			return nil, events.ErrInvalidCredentials
		}
		_, err := g.LookupConversation(ctx, "c1")
		if !errors.Is(err, ErrUnavailable) {
			t.Fatalf("credential failure returned %v, want ErrUnavailable", err)
		}
		if !g.healthFailure.Load() || g.ready.Load() || g.SessionUp() || g.ConnectionStatus().State != "login_required" {
			t.Fatalf("credential failure did not mark login_required: healthFailure=%v ready=%v status=%+v",
				g.healthFailure.Load(), g.ready.Load(), g.ConnectionStatus())
		}
	})

	t.Run("deadline_exceeded_with_probe_ok_keeps_session_up", func(t *testing.T) {
		t.Parallel()
		g := NewGoogle(nil, "")
		g.ready.Store(true)
		g.lookupPause = time.Millisecond
		g.connected = func() bool { return true }
		g.getConversation = func(context.Context, string) (*gmproto.Conversation, error) {
			return nil, context.DeadlineExceeded
		}
		var probes atomic.Int32
		g.probeBugleDefault = func(context.Context) (*gmproto.IsBugleDefaultResponse, error) {
			probes.Add(1)
			return &gmproto.IsBugleDefaultResponse{Success: true}, nil
		}
		_, err := g.LookupConversation(ctx, "c1")
		if !errors.Is(err, ErrLookupFailed) || errors.Is(err, ErrUnavailable) {
			t.Fatalf("timeout with healthy phone returned %v, want ErrLookupFailed", err)
		}
		g.recoveryWG.Wait()
		if probes.Load() != 1 || g.PhoneRequests()[phoneProbe].Total != 1 {
			t.Fatalf("probe count = %d (metric=%d), want 1", probes.Load(), g.PhoneRequests()[phoneProbe].Total)
		}
		if g.healthFailure.Load() || !g.ready.Load() || !g.SessionUp() {
			t.Fatalf("healthy probe still marked phone offline: healthFailure=%v ready=%v sessionUp=%v",
				g.healthFailure.Load(), g.ready.Load(), g.SessionUp())
		}
	})

	t.Run("deadline_exceeded_with_probe_failure_marks_offline", func(t *testing.T) {
		t.Parallel()
		for _, tc := range []struct {
			name string
			resp *gmproto.IsBugleDefaultResponse
			err  error
		}{
			{"probe_error", nil, errors.New("probe timeout")},
			{"probe_not_success", &gmproto.IsBugleDefaultResponse{Success: false}, nil},
		} {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()
				g := NewGoogle(nil, "")
				g.ready.Store(true)
				g.lookupPause = time.Millisecond
				g.connected = func() bool { return true }
				g.getConversation = func(context.Context, string) (*gmproto.Conversation, error) {
					return nil, context.DeadlineExceeded
				}
				g.probeBugleDefault = func(context.Context) (*gmproto.IsBugleDefaultResponse, error) {
					return tc.resp, tc.err
				}
				_, err := g.LookupConversation(ctx, "c1")
				if !errors.Is(err, ErrLookupFailed) {
					t.Fatalf("timeout returned %v, want ErrLookupFailed", err)
				}
				g.recoveryWG.Wait()
				if !g.healthFailure.Load() || g.ready.Load() || g.SessionUp() || g.ConnectionStatus().State != "reconnecting" {
					t.Fatalf("failed probe did not mark reconnecting: healthFailure=%v ready=%v status=%+v",
						g.healthFailure.Load(), g.ready.Load(), g.ConnectionStatus())
				}
			})
		}
	})

	t.Run("concurrent_timeouts_coalesce_to_one_probe", func(t *testing.T) {
		t.Parallel()
		g := NewGoogle(nil, "")
		g.ready.Store(true)
		g.lookupPause = time.Millisecond
		g.connected = func() bool { return true }
		g.getConversation = func(context.Context, string) (*gmproto.Conversation, error) {
			return nil, context.DeadlineExceeded
		}
		var probes atomic.Int32
		probeStarted := make(chan struct{})
		releaseProbe := make(chan struct{})
		g.probeBugleDefault = func(context.Context) (*gmproto.IsBugleDefaultResponse, error) {
			if probes.Add(1) == 1 {
				close(probeStarted)
			}
			<-releaseProbe
			return &gmproto.IsBugleDefaultResponse{Success: true}, nil
		}
		const n = 5
		var wg sync.WaitGroup
		wg.Add(n)
		for range n {
			go func() {
				defer wg.Done()
				_, err := g.LookupConversation(ctx, "c1")
				if !errors.Is(err, ErrLookupFailed) {
					t.Errorf("concurrent timeout returned %v, want ErrLookupFailed", err)
				}
			}()
		}
		wg.Wait()
		<-probeStarted
		if probes.Load() != 1 || g.PhoneRequests()[phoneProbe].Total != 1 {
			t.Fatalf("concurrent timeouts started %d probes (metric=%d), want 1", probes.Load(), g.PhoneRequests()[phoneProbe].Total)
		}
		close(releaseProbe)
		g.recoveryWG.Wait()
		if probes.Load() != 1 || g.healthFailure.Load() || !g.SessionUp() {
			t.Fatalf("unexpected state after coalesced probe: probes=%d healthFailure=%v sessionUp=%v",
				probes.Load(), g.healthFailure.Load(), g.SessionUp())
		}
	})

	t.Run("session_down_rejects_up_front_with_unavailable", func(t *testing.T) {
		t.Parallel()
		g := NewGoogle(nil, "")
		g.ready.Store(true)
		g.connected = func() bool { return true }
		g.noteFailure("reconnecting", false)
		called := false
		g.getConversation = func(context.Context, string) (*gmproto.Conversation, error) {
			called = true
			return individual("c1"), nil
		}
		_, err := g.LookupConversation(ctx, "c1")
		if !errors.Is(err, ErrUnavailable) || called {
			t.Fatalf("session-down lookup returned %v (called=%v), want ErrUnavailable without phone call", err, called)
		}
	})
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

func TestFetchHistorySkipsPreLookupWhenCachedAndVerifiesPostFetch(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	g := NewGoogle(nil, "")
	g.ready.Store(true)
	g.connected = func() bool { return true }
	g.cacheDetail(individual("c1"))

	getCalls := 0
	fetchCalls := 0
	_, err := g.fetchHistory(ctx, "c1", "peer", "", func(context.Context, string) (*gmproto.Conversation, error) {
		getCalls++
		return individual("c1"), nil
	}, func(context.Context, string, int64, *gmproto.Cursor) (*gmproto.ListMessagesResponse, error) {
		fetchCalls++
		return &gmproto.ListMessagesResponse{}, nil
	})
	if err != nil {
		t.Fatalf("cached fetchHistory failed: %v", err)
	}
	if getCalls != 1 || fetchCalls != 1 {
		t.Fatalf("got getCalls=%d fetchCalls=%d, want 1 and 1 (post-fetch guard only)", getCalls, fetchCalls)
	}

	// Post-fetch identity mismatch rejects the page and marks cache stale so the
	// next call runs the pre-check again.
	getCalls, fetchCalls = 0, 0
	wrongPeer := individual("c1")
	wrongPeer.Participants[0].ID.ParticipantID = "other-peer"
	_, err = g.fetchHistory(ctx, "c1", "peer", "", func(context.Context, string) (*gmproto.Conversation, error) {
		getCalls++
		return wrongPeer, nil
	}, func(context.Context, string, int64, *gmproto.Cursor) (*gmproto.ListMessagesResponse, error) {
		fetchCalls++
		return &gmproto.ListMessagesResponse{}, nil
	})
	if err == nil || err.Error() != "conversation_identity_changed" {
		t.Fatalf("identity mismatch returned %v, want conversation_identity_changed", err)
	}
	if _, ok := g.CachedConversation("c1"); ok {
		t.Fatal("post-fetch identity mismatch left stale conversation cached")
	}

	// Next call runs the pre-check and returns ErrIneligibleConversation without fetching messages.
	getCalls, fetchCalls = 0, 0
	_, err = g.fetchHistory(ctx, "c1", "peer", "", func(context.Context, string) (*gmproto.Conversation, error) {
		getCalls++
		return wrongPeer, nil
	}, func(context.Context, string, int64, *gmproto.Cursor) (*gmproto.ListMessagesResponse, error) {
		fetchCalls++
		return &gmproto.ListMessagesResponse{}, nil
	})
	if !errors.Is(err, ErrIneligibleConversation) || getCalls != 1 || fetchCalls != 0 {
		t.Fatalf("post-stale fetchHistory returned %v (getCalls=%d fetchCalls=%d), want ErrIneligibleConversation without fetch", err, getCalls, fetchCalls)
	}
}

func TestSendPartsRetriesGetConversationOnce(t *testing.T) {
	t.Parallel()
	ctx := WithOutgoingID(context.Background(), "self")
	rcsConv := func() *gmproto.Conversation {
		c := individual("c1")
		c.Type = gmproto.ConversationType(2)
		c.SimCard = &gmproto.SIMCard{SIMData: &gmproto.SIMData{SIMPayload: &gmproto.SIMPayload{SIMNumber: 1}}}
		return c
	}

	readyGoogle := func(logFn func(SendDiagnostic)) *Google {
		g := NewGoogle(nil, "")
		g.ready.Store(true)
		g.connected = func() bool { return true }
		g.SendLog = logFn
		return g
	}

	t.Run("transient_lookup_recovers_and_sends_once", func(t *testing.T) {
		var logs []SendDiagnostic
		g := readyGoogle(func(d SendDiagnostic) { logs = append(logs, d) })
		c := rcsConv()
		g.cacheDetail(c)

		getCalls, sendCalls := 0, 0
		got, err := g.sendPartsWith(ctx, "c1", "txn-1", "synthetic", nil, time.Millisecond,
			func(context.Context, string) (*gmproto.Conversation, error) {
				getCalls++
				if getCalls == 1 {
					return nil, errors.New("transient rpc blip")
				}
				return c, nil
			},
			func(context.Context, *gmproto.SendMessageRequest) (*gmproto.SendMessageResponse, error) {
				sendCalls++
				return &gmproto.SendMessageResponse{Status: gmproto.SendMessageResponse_SUCCESS}, nil
			},
		)
		if err != nil || got.State != "submitted" || getCalls != 2 || sendCalls != 1 {
			t.Fatalf("got=%+v err=%v getCalls=%d sendCalls=%d", got, err, getCalls, sendCalls)
		}
		if len(logs) != 1 || logs[0].Result != "submitted" {
			t.Fatalf("unexpected diagnostics: %+v", logs)
		}
	})

	t.Run("two_lookup_failures_refuse_before_send", func(t *testing.T) {
		var logs []SendDiagnostic
		g := readyGoogle(func(d SendDiagnostic) { logs = append(logs, d) })
		g.cacheDetail(rcsConv())

		getCalls, sendCalls := 0, 0
		_, err := g.sendPartsWith(ctx, "c1", "txn-2", "synthetic", nil, time.Millisecond,
			func(context.Context, string) (*gmproto.Conversation, error) {
				getCalls++
				return nil, errors.New("transient rpc blip")
			},
			func(context.Context, *gmproto.SendMessageRequest) (*gmproto.SendMessageResponse, error) {
				sendCalls++
				return &gmproto.SendMessageResponse{Status: gmproto.SendMessageResponse_SUCCESS}, nil
			},
		)
		if !errors.Is(err, ErrNotSubmitted) || RefusalReason(err) != "conversation_lookup_failed" || getCalls != 2 || sendCalls != 0 {
			t.Fatalf("err=%v reason=%q getCalls=%d sendCalls=%d", err, RefusalReason(err), getCalls, sendCalls)
		}
		if len(logs) != 1 || logs[0].Result != "conversation_lookup_failed" {
			t.Fatalf("unexpected diagnostics: %+v", logs)
		}
	})

	t.Run("identity_change_after_retry_still_refuses", func(t *testing.T) {
		g := readyGoogle(nil)
		g.cacheDetail(rcsConv())

		getCalls, sendCalls := 0, 0
		_, err := g.sendPartsWith(ctx, "c1", "txn-3", "synthetic", nil, time.Millisecond,
			func(context.Context, string) (*gmproto.Conversation, error) {
				getCalls++
				if getCalls == 1 {
					return nil, errors.New("transient rpc blip")
				}
				changed := rcsConv()
				changed.Participants[0].ID.ParticipantID = "other-peer"
				return changed, nil
			},
			func(context.Context, *gmproto.SendMessageRequest) (*gmproto.SendMessageResponse, error) {
				sendCalls++
				return &gmproto.SendMessageResponse{Status: gmproto.SendMessageResponse_SUCCESS}, nil
			},
		)
		if !errors.Is(err, ErrNotSubmitted) || RefusalReason(err) != "participant_changed" || getCalls != 2 || sendCalls != 0 {
			t.Fatalf("err=%v reason=%q getCalls=%d sendCalls=%d", err, RefusalReason(err), getCalls, sendCalls)
		}
	})

	t.Run("send_error_is_unknown_and_never_retried", func(t *testing.T) {
		g := readyGoogle(nil)
		c := rcsConv()
		g.cacheDetail(c)

		getCalls, sendCalls := 0, 0
		_, err := g.sendPartsWith(ctx, "c1", "txn-4", "synthetic", nil, time.Millisecond,
			func(context.Context, string) (*gmproto.Conversation, error) {
				getCalls++
				if getCalls == 1 {
					return nil, errors.New("transient rpc blip")
				}
				return c, nil
			},
			func(context.Context, *gmproto.SendMessageRequest) (*gmproto.SendMessageResponse, error) {
				sendCalls++
				return nil, errors.New("send timeout")
			},
		)
		if !errors.Is(err, ErrUnknown) || errors.Is(err, ErrNotSubmitted) || getCalls != 2 || sendCalls != 1 {
			t.Fatalf("err=%v getCalls=%d sendCalls=%d", err, getCalls, sendCalls)
		}
	})
}

func TestReactRetriesGetConversationOnce(t *testing.T) {
	t.Parallel()
	ctx := WithOutgoingID(context.Background(), "self")
	rcsConv := func() *gmproto.Conversation {
		c := individual("c1")
		c.Type = gmproto.ConversationType(2)
		c.SimCard = &gmproto.SIMCard{SIMData: &gmproto.SIMData{SIMPayload: &gmproto.SIMPayload{SIMNumber: 1}}}
		return c
	}

	g := NewGoogle(nil, "")
	g.ready.Store(true)
	g.connected = func() bool { return true }
	c := rcsConv()
	g.cacheDetail(c)

	getCalls, reactCalls := 0, 0
	err := g.reactWith(ctx, "c1", "peer", "wire-1", "👍", "add", time.Millisecond,
		func(context.Context, string) (*gmproto.Conversation, error) {
			getCalls++
			if getCalls == 1 {
				return nil, errors.New("transient rpc blip")
			}
			return c, nil
		},
		func(context.Context, *gmproto.SendReactionRequest) (*gmproto.SendReactionResponse, error) {
			reactCalls++
			return &gmproto.SendReactionResponse{Success: true}, nil
		},
	)
	if err != nil || getCalls != 2 || reactCalls != 1 {
		t.Fatalf("err=%v getCalls=%d reactCalls=%d", err, getCalls, reactCalls)
	}

	getCalls, reactCalls = 0, 0
	err = g.reactWith(ctx, "c1", "peer", "wire-1", "👍", "add", time.Millisecond,
		func(context.Context, string) (*gmproto.Conversation, error) {
			getCalls++
			return nil, errors.New("transient rpc blip")
		},
		func(context.Context, *gmproto.SendReactionRequest) (*gmproto.SendReactionResponse, error) {
			reactCalls++
			return &gmproto.SendReactionResponse{Success: true}, nil
		},
	)
	if !errors.Is(err, ErrNotSubmitted) || RefusalReason(err) != "conversation_lookup_failed" || getCalls != 2 || reactCalls != 0 {
		t.Fatalf("err=%v reason=%q getCalls=%d reactCalls=%d", err, RefusalReason(err), getCalls, reactCalls)
	}
}
