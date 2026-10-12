package transport

import (
	"context"
	"errors"
	"fmt"
	"go.mau.fi/mautrix-gmessages/pkg/libgm/gmproto"
	"sync/atomic"
	"testing"
	"time"
)

func TestConversationDetailsBoundedParallelismAndOrder(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ids := make([]string, 25)
	for i := range ids {
		ids[i] = fmt.Sprintf("fixture-%d", i)
	}
	var active, maximum atomic.Int32
	started := make(chan struct{}, 4)
	release := make(chan struct{})
	done := make(chan struct{})
	var result []*gmproto.Conversation
	var resultErr error
	go func() {
		defer close(done)
		result, resultErr = conversationDetails(ctx, ids, func(ctx context.Context, id string) (*gmproto.Conversation, error) {
			current := active.Add(1)
			defer active.Add(-1)
			for old := maximum.Load(); current > old && !maximum.CompareAndSwap(old, current); old = maximum.Load() {
			}
			select {
			case started <- struct{}{}:
			default:
			}
			select {
			case <-release:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			return &gmproto.Conversation{ConversationID: id}, nil
		})
	}()
	for i := 0; i < 4; i++ {
		select {
		case <-started:
		case <-ctx.Done():
			t.Fatal("four independent requests did not overlap")
		}
	}
	close(release)
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("detail fetch did not finish")
	}
	if resultErr != nil {
		t.Fatal(resultErr)
	}
	if maximum.Load() != 4 {
		t.Fatalf("maximum concurrency=%d", maximum.Load())
	}
	for i, c := range result {
		if c.GetConversationID() != ids[i] {
			t.Fatal("inbox order changed")
		}
	}
}

func TestConversationDetailsRejectsIncompleteIdentityAndFailure(t *testing.T) {
	for _, mode := range []string{"mismatch", "nil", "failure"} {
		t.Run(mode, func(t *testing.T) {
			result, err := conversationDetails(context.Background(), []string{"fixture"}, func(context.Context, string) (*gmproto.Conversation, error) {
				switch mode {
				case "mismatch":
					return &gmproto.Conversation{ConversationID: "other-fixture"}, nil
				case "nil":
					return nil, nil
				default:
					return nil, errors.New("synthetic failure")
				}
			})
			if err == nil || result != nil {
				t.Fatal("partial or unverified identities returned")
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := conversationDetails(ctx, []string{"fixture"}, func(context.Context, string) (*gmproto.Conversation, error) {
		t.Fatal("cancelled request started")
		return nil, nil
	}); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation lost")
	}
}
