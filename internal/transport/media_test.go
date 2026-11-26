package transport

import (
	"context"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/deusical/smsbot-server/internal/media"
	"go.mau.fi/mautrix-gmessages/pkg/libgm"
	"go.mau.fi/mautrix-gmessages/pkg/libgm/gmproto"
)

func TestOutboundMediaRetainsUploadReferenceAndFormat(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		mime   string
		format gmproto.MediaFormats
	}{
		{"image/png", gmproto.MediaFormats_IMAGE_PNG},
		{"image/webp", gmproto.MediaFormats_IMAGE_UNSPECIFIED},
		{"application/pdf", gmproto.MediaFormats_APP_PDF},
		{"application/octet-stream", gmproto.MediaFormats_APP_UNSPECIFIED},
	} {
		part := outboundMedia(media.File{Info: media.Info{Name: "synthetic-file", MIME: tc.mime, Size: 42}, RemoteID: "synthetic-upload", Key: make([]byte, 32)})
		if part.Format != tc.format || part.MediaID != "synthetic-upload" || len(part.DecryptionKey) != 32 || part.MediaName != "synthetic-file" || part.Size != 42 || part.MimeType != tc.mime {
			t.Fatal("incorrect outbound media payload", tc.mime)
		}
	}
}

func TestAttachmentsNormalizeLiveAndHistoryWithoutLosingCaption(t *testing.T) {
	t.Parallel()
	now := time.Now()
	m := &gmproto.Message{MessageID: "synthetic-media", ConversationID: "synthetic-chat", ParticipantID: "synthetic-peer", Timestamp: now.UnixMicro(), MessageStatus: &gmproto.MessageStatus{Status: gmproto.MessageStatusType_INCOMING_COMPLETE}, MessageInfo: []*gmproto.MessageInfo{
		{Data: &gmproto.MessageInfo_MessageContent{MessageContent: &gmproto.MessageContent{Content: "!ping"}}},
		{Data: &gmproto.MessageInfo_MediaContent{MediaContent: &gmproto.MediaContent{MediaID: "synthetic-remote", MediaName: "../image.png", Format: gmproto.MediaFormats_IMAGE_PNG, Size: 42, DecryptionKey: make([]byte, 32)}}},
	}}
	for _, outgoing := range []bool{false, true} {
		if outgoing {
			m.MessageStatus.Status = gmproto.MessageStatusType_OUTGOING_DELIVERED
		}
		live, ok := normalize(&libgm.WrappedMessage{Message: m}, now.Add(-time.Second))
		if !ok || live.Text != "!ping" || len(live.Attachments) != 1 || live.Outgoing != outgoing {
			t.Fatal("live media or caption lost")
		}
		file := live.Attachments[0]
		if file.Name != "image.png" || file.MIME != "image/png" || file.RemoteID != "synthetic-remote" || len(file.Key) != 32 {
			t.Fatal("media descriptor lost")
		}
		history, ok := historicalText(m, m.ConversationID)
		if !ok || len(history.Attachments) != 1 || history.Text != "!ping" {
			t.Fatal("historical media lost")
		}
	}
	m.MessageInfo = m.MessageInfo[1:]
	if live, ok := normalize(&libgm.WrappedMessage{Message: m}, now.Add(-time.Second)); !ok || live.Text != "" || len(live.Attachments) != 1 {
		t.Fatal("file-only message discarded")
	}
	if _, ok := historicalText(m, "wrong-chat"); ok {
		t.Fatal("cross-conversation media accepted")
	}
	if _, ok := normalize(&libgm.WrappedMessage{Message: m, IsOld: true}, now.Add(-time.Second)); ok {
		t.Fatal("historical media replayed live")
	}
}

func TestLongTextPreservedForArchive(t *testing.T) {
	t.Parallel()
	text := strings.Repeat("Synthetic long text ", 1000)
	m := &gmproto.Message{MessageInfo: []*gmproto.MessageInfo{{Data: &gmproto.MessageInfo_MessageContent{MessageContent: &gmproto.MessageContent{Content: text}}}}}
	got, _, ok := messageParts(m)
	if !ok || got != text {
		t.Fatal("archive normalization applied command length ceiling")
	}
}

func TestOutboundPartsPutFilesBeforeCaption(t *testing.T) {
	t.Parallel()
	file := media.File{RemoteID: "upload", Key: make([]byte, 32), Name: "a.png", MIME: "image/png"}
	parts, ok := outboundParts("caption", []media.File{file, file})
	if !ok || len(parts) != 3 || parts[0].GetMediaContent() == nil || parts[1].GetMediaContent() == nil || parts[2].GetMessageContent().GetContent() != "caption" {
		t.Fatalf("parts=%v ok=%v", parts, ok)
	}
	if parts, ok = outboundParts("text only", nil); !ok || len(parts) != 1 || parts[0].GetMessageContent().GetContent() != "text only" {
		t.Fatalf("text parts=%v ok=%v", parts, ok)
	}
	if _, ok = outboundParts("caption", []media.File{{Name: "not uploaded"}}); ok {
		t.Fatal("a file without an upload reference must not be sent")
	}
	if _, ok = outboundParts("", nil); ok {
		t.Fatal("an empty message must not be sent")
	}
}

func TestDownloadFileBoundsConcurrentPhoneDownloadsAndBypassesMemoryData(t *testing.T) {
	t.Parallel()
	g := NewGoogle(nil, "")
	g.ready.Store(true)
	g.connected = func() bool { return true }

	var active atomic.Int32
	var maxActive atomic.Int32
	var startedCalls atomic.Int32
	slotEntered := make(chan string, 4)
	releaseSlots := make(chan struct{})
	g.downloadMedia = func(remoteID string, _ []byte) (io.ReadCloser, error) {
		cur := active.Add(1)
		for {
			prev := maxActive.Load()
			if cur <= prev || maxActive.CompareAndSwap(prev, cur) {
				break
			}
		}
		startedCalls.Add(1)
		slotEntered <- remoteID
		<-releaseSlots
		active.Add(-1)
		return io.NopCloser(strings.NewReader("payload-" + remoteID)), nil
	}

	remoteFile := func(id string) media.File {
		return media.File{RemoteID: id, Key: make([]byte, 32)}
	}

	// Start 2 concurrent downloads that occupy both semaphore slots.
	type result struct {
		b   []byte
		err error
	}
	res1 := make(chan result, 1)
	res2 := make(chan result, 1)
	go func() {
		b, err := g.DownloadFile(context.Background(), remoteFile("1"))
		res1 <- result{b, err}
	}()
	go func() {
		b, err := g.DownloadFile(context.Background(), remoteFile("2"))
		res2 <- result{b, err}
	}()
	<-slotEntered
	<-slotEntered

	// In-memory data must bypass the semaphore completely.
	memData, err := g.DownloadFile(context.Background(), media.File{Data: []byte("inline-bytes")})
	if err != nil || string(memData) != "inline-bytes" {
		t.Fatalf("in-memory download failed while slots full: %q %v", string(memData), err)
	}

	// Start a 3rd concurrent download and verify it waits.
	res3 := make(chan result, 1)
	go func() {
		b, err := g.DownloadFile(context.Background(), remoteFile("3"))
		res3 <- result{b, err}
	}()

	// A cancelled caller waiting for a slot must return promptly without acquiring a slot.
	cancelCtx, cancel := context.WithCancel(context.Background())
	res4 := make(chan result, 1)
	go func() {
		b, err := g.DownloadFile(cancelCtx, remoteFile("4"))
		res4 <- result{b, err}
	}()
	cancel()
	if r4 := <-res4; r4.err == nil {
		t.Fatal("cancelled waiting download succeeded unexpectedly")
	}

	select {
	case id := <-slotEntered:
		t.Fatalf("3rd download %s bypassed semaphore while 2 were active", id)
	case <-time.After(30 * time.Millisecond):
	}
	if startedCalls.Load() != 2 {
		t.Fatalf("startedCalls = %d while 2 slots occupied, want 2", startedCalls.Load())
	}

	close(releaseSlots)
	for i, ch := range []chan result{res1, res2, res3} {
		r := <-ch
		if r.err != nil {
			t.Fatalf("download %d failed: %v", i+1, r.err)
		}
	}
	if maxActive.Load() > 2 || startedCalls.Load() != 3 {
		t.Fatalf("maxActive=%d startedCalls=%d, want <=2 and 3", maxActive.Load(), startedCalls.Load())
	}
}
