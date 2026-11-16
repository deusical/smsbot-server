package collection

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"github.com/deusical/smsbot-server/internal/media"
	"github.com/deusical/smsbot-server/internal/store"
	"github.com/deusical/smsbot-server/internal/transport"
)

type countingFileTransport struct {
	*transport.Fixture
	lookups  atomic.Int32
	failChat string
	livePeer string
	fileData []byte
}

func (c *countingFileTransport) LookupConversation(ctx context.Context, id string) (transport.Conversation, error) {
	c.lookups.Add(1)
	if id == c.failChat {
		return transport.Conversation{}, errors.New("lookup_failed")
	}
	list, err := c.Fixture.Conversations(ctx)
	if err != nil {
		return transport.Conversation{}, err
	}
	for _, conv := range list {
		if conv.ID == id {
			if c.livePeer != "" && id == "demo-chat" {
				conv.ParticipantID = c.livePeer
			}
			return conv, nil
		}
	}
	return transport.Conversation{}, errors.New("conversation_unavailable")
}

func (c *countingFileTransport) OpenFile(_ context.Context, _ media.File) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(c.fileData)), nil
}

func (c *countingFileTransport) RefreshFile(_ context.Context, _, _ string, _ time.Time, f media.File) (media.File, error) {
	return f, nil
}

func TestFileHostAuthorizeChecksLocalSettingsFirstAndCoolsDownFailures(t *testing.T) {
	t.Parallel()
	svc, fixture := setup(t)
	now := time.Now()
	svc.SetClock(func() time.Time { return now })
	svc.Archive.Now = func() time.Time { return now }

	tr := &countingFileTransport{Fixture: fixture, fileData: []byte("original-bytes")}
	svc.Transport = tr

	demo := transport.Conversation{ID: "demo-chat", ParticipantID: "demo-peer", Writable: true}
	sms := transport.Conversation{ID: "demo-sms", ParticipantID: "demo-sms-peer", Writable: true}
	if err := svc.Store.SaveArchive(store.Archive{Conversation: demo, Generation: 1, State: "running"}, 0); err != nil {
		t.Fatal(err)
	}
	if err := svc.Store.SaveArchive(store.Archive{Conversation: sms, Generation: 1, State: "running"}, 0); err != nil {
		t.Fatal(err)
	}
	file := media.File{Info: media.Info{ID: "part-1", Size: int64(len(tr.fileData))}, RemoteID: "remote-1", MessageID: "m1", SizeUnverified: false}
	if err := svc.Store.SaveObservedFiles(demo.ID, "m1", "", "incoming", "", "phone", "", now, []media.File{file}); err != nil {
		t.Fatal(err)
	}

	// 1. With collection disabled locally, Archive.Tick, Repair, and Authorize must
	// refuse before making any live phone lookup.
	for range 3 {
		svc.Archive.Tick(context.Background())
	}
	if _, err := svc.FileHost().Repair(context.Background(), demo, store.Hash(demo.ID+"\x00m1"), file); err == nil {
		t.Fatal("expected Repair refusal while collection is disabled")
	}
	if _, err := svc.FileHost().Authorize(context.Background(), demo); err == nil {
		t.Fatal("expected Authorize refusal while collection is disabled")
	}
	if got := tr.lookups.Load(); got != 0 {
		t.Fatalf("local refusal still queried phone %d times", got)
	}

	// Selected-scope refusal for an unselected conversation also avoids phone lookups.
	cfg, err := svc.Settings()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Enabled = true
	cfg.Scope = "selected"
	cfg.Conversations = []Binding{{Conversation: sms.ID, Participant: sms.ParticipantID}}
	if err := svc.SetSettings(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	tr.lookups.Store(0)
	if _, err := svc.FileHost().Authorize(context.Background(), demo); err == nil {
		t.Fatal("unselected conversation authorized")
	}
	if got := tr.lookups.Load(); got != 0 {
		t.Fatalf("unselected conversation queried phone %d times", got)
	}

	// 2. Enable all conversations and verify failed authorization grows cooldown per conversation.
	cfg, _ = svc.Settings()
	cfg.Scope = "all"
	cfg.Conversations = []Binding{}
	if err := svc.SetSettings(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	tr.lookups.Store(0)
	tr.failChat = demo.ID

	// Attempt 1 fails -> 10s cooldown on demo-chat, while demo-sms remains unaffected.
	if _, err := svc.FileHost().Authorize(context.Background(), demo); err == nil {
		t.Fatal("expected failed lookup refusal")
	}
	if got := tr.lookups.Load(); got != 1 {
		t.Fatalf("lookups after first failure = %d, want 1", got)
	}
	// Note: Repair happened inside Tick or directly; wait 2s (< 10s cooldown) so
	// even if we waited <10s after the first failure, Tick and Repair still hit cooldown.
	// Reset clock relative to the first Authorize failure above:
	for range 4 {
		now = now.Add(2 * time.Second) // +2s..+8s (< 10s)
		svc.Archive.Tick(context.Background())
		if _, err := svc.FileHost().Repair(context.Background(), demo, store.Hash(demo.ID+"\x00m1"), file); err == nil {
			t.Fatal("Repair succeeded during cooldown")
		}
	}
	if got := tr.lookups.Load(); got != 1 {
		t.Fatalf("cooldown bypassed during first 10s window: lookups = %d", got)
	}
	// Other conversations are not blocked by demo-chat's cooldown.
	if _, err := svc.FileHost().Authorize(context.Background(), sms); err != nil {
		t.Fatalf("other conversation blocked by cooldown: %v", err)
	}
	if got := tr.lookups.Load(); got != 2 {
		t.Fatalf("other conversation skipped live lookup: lookups = %d", got)
	}

	// Past 10s, retry 2 fails -> 20s cooldown.
	now = now.Add(3 * time.Second) // +11s from first failure
	if _, err := svc.FileHost().Authorize(context.Background(), demo); err == nil {
		t.Fatal("expected second failure")
	}
	if got := tr.lookups.Load(); got != 3 {
		t.Fatalf("lookups after second failure = %d, want 3", got)
	}
	now = now.Add(15 * time.Second) // < 20s cooldown
	if _, err := svc.FileHost().Authorize(context.Background(), demo); err == nil {
		t.Fatal("expected cooldown refusal at 15s")
	}
	if got := tr.lookups.Load(); got != 3 {
		t.Fatalf("grown 20s cooldown bypassed at 15s: lookups = %d", got)
	}

	// Verify delay cap.
	if got := fileAuthDelay(100); got != fileAuthMaxCooldown {
		t.Fatalf("uncapped delay = %v, want %v", got, fileAuthMaxCooldown)
	}

	// 3. Reconnected() clears cooldown immediately.
	svc.Reconnected()
	if _, err := svc.FileHost().Authorize(context.Background(), demo); err == nil {
		t.Fatal("expected lookup failure after Reconnected")
	}
	if got := tr.lookups.Load(); got != 4 {
		t.Fatalf("Reconnected did not clear cooldown: lookups = %d, want 4", got)
	}

	// 4. Saving settings also clears cooldown immediately, and live lookup still
	// guards against participant changes before authorizing any download.
	tr.failChat = ""
	tr.livePeer = "impostor-peer"
	cfg, _ = svc.Settings()
	if err := svc.SetSettings(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	beforeSaveRetry := tr.lookups.Load()
	if _, err := svc.FileHost().Authorize(context.Background(), demo); err == nil {
		t.Fatal("changed live participant was authorized")
	}
	if tr.lookups.Load() != beforeSaveRetry+1 {
		t.Fatalf("settings save did not clear cooldown or skipped live lookup: lookups=%d", tr.lookups.Load())
	}

	// Once reconnected with matching live participant, live lookup runs and download succeeds.
	tr.livePeer = ""
	svc.Reconnected()
	now = now.Add(time.Minute)
	beforeSuccess := tr.lookups.Load()
	svc.Archive.Tick(context.Background()) // upgrades SizeUnverified via Repair
	svc.Archive.Tick(context.Background()) // wraps cursor at end of batch
	now = now.Add(time.Minute)
	svc.Archive.Tick(context.Background()) // authorizes and downloads original
	if tr.lookups.Load() <= beforeSuccess {
		t.Fatal("download authorized without live phone lookup")
	}
	saved, err := svc.Store.Attachment(demo.ID, store.Hash(demo.ID+"\x00m1"), file.ID)
	if err != nil || saved.BlobID == "" {
		t.Fatalf("file not archived after recovery: %+v err=%v", saved, err)
	}
}
