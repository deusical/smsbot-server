package transport

import (
	"context"
	"errors"
	"github.com/deusical/smsbot-server/internal/media"
	"go.mau.fi/mautrix-gmessages/pkg/libgm"
	"go.mau.fi/mautrix-gmessages/pkg/libgm/gmproto"
	"sync/atomic"
	"testing"
	"time"
)

func mediaUpdate(message, action, remote string) *gmproto.Message {
	return &gmproto.Message{MessageID: message, MessageInfo: []*gmproto.MessageInfo{{ActionMessageID: &action, Data: &gmproto.MessageInfo_MediaContent{MediaContent: &gmproto.MediaContent{MimeType: "image/jpeg", MediaID: remote, Size: 250000, DecryptionKey: make([]byte, 32), MediaData: []byte("tiny preview")}}}}}
}
func TestGoogleInlineImageIsPreviewOnly(t *testing.T) {
	m := mediaUpdate("synthetic-message", "synthetic-action", "")
	_, files, ok := messageParts(m)
	if !ok || len(files) != 1 {
		t.Fatal("missing media")
	}
	f := files[0]
	if len(f.Data) != 0 || string(f.PreviewData) != "tiny preview" || !f.PreviewOnly || f.Size != 0 || f.MessageID != "synthetic-message" || f.ActionID != "synthetic-action" {
		t.Fatal("inline preview advertised as original")
	}
	// An original reference must take precedence over any inline preview.
	_, files, _ = messageParts(mediaUpdate("synthetic-message", "synthetic-action", "synthetic-original"))
	if files[0].RemoteID != "synthetic-original" || len(files[0].Data) != 0 || files[0].PreviewOnly || files[0].Size != 250000 {
		t.Fatal("original descriptor lost")
	}
}
func TestFullMediaAcceptsOnlyRequestedHistoricalPart(t *testing.T) {
	g := NewGoogle(nil, "")
	f := media.File{Info: media.Info{ID: "part-0"}, MessageID: "synthetic-message", ActionID: "synthetic-action"}
	result, err := g.fullMedia.get(context.Background(), f, func(ctx context.Context, message, action string) error {
		if message != f.MessageID || action != f.ActionID {
			t.Fatal("incorrect full-size request")
		}
		g.enqueue(&libgm.WrappedMessage{Message: mediaUpdate("other-message", action, "wrong-original"), IsOld: true})
		g.enqueue(&libgm.WrappedMessage{Message: mediaUpdate(message, "other-part", "wrong-original"), IsOld: true})
		g.enqueue(&libgm.WrappedMessage{Message: mediaUpdate(message, action, "synthetic-original"), IsOld: true})
		return nil
	})
	if err != nil || result.RemoteID != "synthetic-original" || result.ID != "part-0" {
		t.Fatal("full-size response lost", err)
	}
	if len(g.updates) != 0 {
		t.Fatal("historical media entered command event queue")
	}
	if len(g.fullMedia.pending) != 0 {
		t.Fatal("request not cleaned up")
	}
}
func TestFullMediaCancellationAndFailureNeverReturnPreview(t *testing.T) {
	for _, fail := range []bool{false, true} {
		var r fullMediaRequests
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
		f := media.File{MessageID: "synthetic-message", ActionID: "synthetic-action", PreviewData: []byte("tiny preview")}
		got, err := r.get(ctx, f, func(context.Context, string, string) error {
			if fail {
				return errors.New("synthetic upstream failure")
			}
			return nil
		})
		cancel()
		if err == nil || got.RemoteID != "" || len(got.Data) > 0 || len(got.PreviewData) > 0 {
			t.Fatal("preview returned as original")
		}
		if len(r.pending) != 0 {
			t.Fatal("cancelled request leaked")
		}
	}
}

func TestOriginalReadBackIsSparseAndStopsWithRequest(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var reads atomic.Int32
	delivered := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		readBackOriginal(ctx, []time.Duration{0, 20 * time.Millisecond, time.Hour}, func(context.Context) ([]*gmproto.Message, error) {
			if reads.Add(1) == 2 {
				return []*gmproto.Message{{MessageID: "synthetic-message"}}, nil
			}
			return nil, nil
		}, func(*gmproto.Message) { close(delivered) })
	}()
	<-delivered
	cancel()
	<-done
	if reads.Load() != 2 {
		t.Fatalf("read back %d times", reads.Load())
	}
	if len(fullMediaReadBack) > 3 || fullMediaReadBack[0] != 0 || fullMediaReadBack[len(fullMediaReadBack)-1] >= 20*time.Second || fullMediaReadCount > 10 {
		t.Fatal("full-size read-back is no longer sparse and bounded by the request deadline")
	}
}
func TestOriginalReadBackStopsOnPhoneError(t *testing.T) {
	reads := 0
	readBackOriginal(context.Background(), []time.Duration{0, 0, 0}, func(context.Context) ([]*gmproto.Message, error) {
		reads++
		return nil, errors.New("synthetic failure")
	}, func(*gmproto.Message) {})
	if reads != 1 {
		t.Fatal("read back continued after a phone error")
	}
}
