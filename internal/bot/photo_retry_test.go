package bot

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/deusical/smsbot-server/internal/media"
	"github.com/deusical/smsbot-server/internal/store"
	"github.com/deusical/smsbot-server/internal/transport"
)

// flakyPhone fails its first downloads, as a slow phone or a file being saved
// by another worker does.
type flakyPhone struct {
	*transport.Fixture
	failures, downloads int
	err                 error
}

func (f *flakyPhone) DownloadFile(ctx context.Context, file media.File) ([]byte, error) {
	f.downloads++
	if f.downloads <= f.failures {
		return nil, f.err
	}
	return syntheticPNG, nil
}

func TestAPhotoIsReadAgainAfterATemporaryError(t *testing.T) {
	old := fileRetryDelay
	fileRetryDelay = time.Millisecond
	t.Cleanup(func() { fileRetryDelay = old })
	for _, tc := range []struct {
		name     string
		failures int
		err      error
		ok       bool
		code     string
	}{
		{"recovers", 2, errors.New("attachment_unavailable"), true, ""},
		{"gives_up", 10, errors.New("original_pending"), false, "(original_pending)"},
		{"not_retried", 10, errors.New("inbox_access_disabled"), false, "(inbox_access_disabled)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, fixture := setup(t)
			enableInbox(t, e)
			phone := &flakyPhone{Fixture: fixture, failures: tc.failures, err: tc.err}
			useTransport(e, phone)
			arm(t, e)
			configureAI(t, e, false, false)
			provider := &transcriptProvider{}
			e.Provider = provider
			files := []media.File{{Info: media.Info{ID: "part-0", Name: "synthetic", MIME: "image/png", Size: int64(len(syntheticPNG))}, RemoteID: "remote", MessageID: "photo-wire"}}
			if err := e.Store.SaveObservedFiles("demo-chat", "photo-wire", "", "incoming", "", "phone", "", time.Now(), files); err != nil {
				t.Fatal(err)
			}
			threadAccept(t, e, threadPrompt("ask-photo", "!ask what's this?", "photo-wire"))
			if err := e.Dispatch(context.Background()); err != nil {
				t.Fatal(err)
			}
			state := stateOf(t, e, store.Hash("demo-chat\x00ask-photo"))
			if tc.ok {
				if state != "submitted" || len(provider.images) != 1 || phone.downloads != 3 {
					t.Fatalf("state=%s downloads=%d", state, phone.downloads)
				}
				return
			}
			if state != "submitted" || len(provider.history) != 0 || !sentContains(fixture, tc.code) {
				t.Fatalf("state=%s sent=%v", state, fixture.Sent)
			}
			if tc.name == "not_retried" && phone.downloads != 1 {
				t.Fatalf("a permanent error was retried %d times", phone.downloads)
			}
		})
	}
}
