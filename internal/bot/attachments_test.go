package bot

import (
	"context"
	"errors"
	"smsbot-server/internal/media"
	"smsbot-server/internal/transport"
	"testing"
	"time"
)

type thumbnailTransport struct{ *transport.Fixture }

func (f *thumbnailTransport) DownloadFile(ctx context.Context, file media.File) ([]byte, error) {
	if file.RemoteID == "synthetic-thumbnail" {
		return []byte("GIF89aSynthetic thumbnail"), nil
	}
	return nil, errors.New("attachment_unavailable")
}

func TestImageThumbnailDoesNotMasqueradeAsOriginalDownload(t *testing.T) {
	e, fixture := setup(t)
	enableInbox(t, e)
	e.Transport = &thumbnailTransport{fixture}
	files := []media.File{{Info: media.Info{ID: "part-0", Name: "synthetic.png", MIME: "image/png", Size: 100}, RemoteID: "synthetic-unavailable-original", ThumbnailID: "synthetic-thumbnail"}}
	if err := e.Store.SaveObservedFiles("demo-chat", "synthetic-thumb-message", "", "incoming", "", "phone", "", time.Now(), files); err != nil {
		t.Fatal(err)
	}
	message := ""
	p, _ := e.Store.OwnerMessagePage("demo-chat", "", "", 50)
	message = p.Messages[0].ID
	info, data, err := e.InboxAttachmentContent(context.Background(), "demo-chat", message, "part-0", true)
	if err != nil || info.MIME != "image/gif" || string(data) != "GIF89aSynthetic thumbnail" {
		t.Fatal("available thumbnail not displayed", err)
	}
	if _, _, err = e.InboxAttachment(context.Background(), "demo-chat", message, "part-0"); err == nil {
		t.Fatal("thumbnail returned as original file")
	}
}

func syntheticUpload() []media.Upload {
	return []media.Upload{{Name: "synthetic.txt", MIME: "text/plain", Data: []byte("Synthetic attachment")}}
}

func TestOwnerLimitsPersistAndCanBeUnlimited(t *testing.T) {
	e, _ := setup(t)
	s := enableInbox(t, e)
	limits := media.Limits{}
	s.AttachmentLimits = &limits
	if err := e.SetInboxSettings(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	saved, err := e.InboxSettings()
	if err != nil || saved.FileLimits() != limits {
		t.Fatal("unlimited limits did not persist", err)
	}
	if err := e.Pause(false); err != nil {
		t.Fatal(err)
	}
	uploads := []media.Upload{}
	for i := 0; i < 5; i++ {
		uploads = append(uploads, syntheticUpload()...)
	}
	id, err := e.ComposeInboxFiles(context.Background(), "demo-chat", "", "synthetic-unlimited-count", uploads)
	if err != nil {
		t.Fatal("unlimited count rejected", err)
	}
	// Changing limits after queueing must be checked before submission.
	saved.AttachmentLimits.MaxFiles = 1
	if err := e.SetInboxSettings(context.Background(), saved); err != nil {
		t.Fatal(err)
	}
	if err := e.Dispatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	jobs, _ := e.Store.Jobs()
	if len(jobs) != 1 || jobs[0].ID != id || jobs[0].State != "cancelled" || jobs[0].Code != "attachment_limits_changed" {
		t.Fatal("new limits ignored for queued send")
	}
}

func TestFileOnlyCaptureAndCaptionNeverExecute(t *testing.T) {
	e, _ := setup(t)
	enableInbox(t, e)
	arm(t, e)
	files, _ := media.Prepare(syntheticUpload())
	for _, caption := range []string{"", "!ping"} {
		m := incoming()
		m.ID = "synthetic-attachment-" + caption
		m.Text = caption
		m.Attachments = files
		accepted, err := e.Receive(context.Background(), m)
		if err != nil || accepted {
			t.Fatal("media caption executed", err)
		}
	}
	p, err := e.InboxPage(context.Background(), "demo-chat", "", "", 50)
	if err != nil || len(p.Messages) != 2 || len(p.Messages[0].Attachments) != 1 {
		t.Fatal("file-only capture missing", err)
	}
	_, data, err := e.InboxAttachment(context.Background(), "demo-chat", p.Messages[0].ID, files[0].ID)
	if err != nil || string(data) != "Synthetic attachment" {
		t.Fatal("attachment download failed", err)
	}
	if _, _, err = e.InboxAttachment(context.Background(), "demo-sms", p.Messages[0].ID, files[0].ID); err == nil {
		t.Fatal("cross-conversation download permitted")
	}
	s, _ := e.InboxSettings()
	s.Enabled = false
	if err = e.SetInboxSettings(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	if _, _, err = e.InboxAttachment(context.Background(), "demo-chat", p.Messages[0].ID, files[0].ID); err == nil {
		t.Fatal("download after consent revocation")
	}
}

func TestFileComposeDurableIntentAndUncertainSubmission(t *testing.T) {
	e, f := setup(t)
	enableInbox(t, e)
	e.Pause(false)
	ctx := context.Background()
	upload := syntheticUpload()
	id, err := e.ComposeInboxFiles(ctx, "demo-chat", "", "synthetic-file-request", upload)
	if err != nil {
		t.Fatal(err)
	}
	_, p, err := e.Store.JobPlan(id)
	if err != nil || len(p.Attachments) != 1 || string(p.Attachments[0].Data) != "Synthetic attachment" {
		t.Fatal("file intent not persisted", err)
	}
	changed := syntheticUpload()
	changed[0].Data = []byte("Different synthetic attachment")
	if _, err = e.ComposeInboxFiles(ctx, "demo-chat", "", "synthetic-file-request", changed); err == nil {
		t.Fatal("request key accepted changed file")
	}
	f.Fail = "unknown"
	if err = e.Dispatch(ctx); err != nil {
		t.Fatal(err)
	}
	j, _, _ := e.Store.JobPlan(id)
	if j.State != "unknown_submission" {
		t.Fatal("uncertain send not preserved", j.State)
	}
	f.Fail = ""
	if err = e.Dispatch(ctx); err != nil || len(f.Sent) != 0 {
		t.Fatal("uncertain file send retried")
	}
	e.Pause(true)
	if repeated, err := e.ComposeInboxFiles(ctx, "demo-chat", "", "synthetic-file-request", upload); err != nil || repeated != id {
		t.Fatal("lost acknowledgement retry lost original", err)
	}
}

type blockedFileTransport struct {
	*transport.Fixture
	started chan struct{}
	release chan struct{}
}

func (f *blockedFileTransport) PrepareFiles(ctx context.Context, files []media.File) ([]media.File, error) {
	close(f.started)
	<-f.release // Deliberately emulate an upstream upload that ignores cancellation.
	return files, nil
}

func TestPauseAndConsentRevocationDuringUploadPreventSend(t *testing.T) {
	for _, revoke := range []bool{false, true} {
		t.Run(map[bool]string{false: "pause", true: "revoke"}[revoke], func(t *testing.T) {
			e, fixture := setup(t)
			enableInbox(t, e)
			e.Pause(false)
			f := &blockedFileTransport{Fixture: fixture, started: make(chan struct{}), release: make(chan struct{})}
			e.Transport = f
			id, err := e.ComposeInboxFiles(context.Background(), "demo-chat", "", "synthetic-upload-cancel", syntheticUpload())
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- e.Dispatch(context.Background()) }()
			select {
			case <-f.started:
			case <-time.After(2 * time.Second):
				t.Fatal("upload did not start")
			}
			if revoke {
				s, _ := e.InboxSettings()
				s.Enabled = false
				err = e.SetInboxSettings(context.Background(), s)
			} else {
				err = e.Pause(true)
			}
			close(f.release)
			if err != nil {
				t.Fatal(err)
			}
			select {
			case err = <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("upload did not finish")
			}
			if err != nil {
				t.Fatal(err)
			}
			jobs, _ := e.Store.Jobs()
			if len(jobs) != 1 || jobs[0].ID != id || jobs[0].State != "cancelled" || len(f.Sent) != 0 {
				t.Fatal("file sent after pause/revocation")
			}
		})
	}
}

func TestSentFilesRetainOriginalThroughPhoneEcho(t *testing.T) {
	e, _ := setup(t)
	enableInbox(t, e)
	e.Pause(false)
	id, err := e.ComposeInboxFiles(context.Background(), "demo-chat", "Synthetic caption", "synthetic-success-file", syntheticUpload())
	if err != nil {
		t.Fatal(err)
	}
	if err = e.Dispatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	j, _, _ := e.Store.JobPlan(id)
	p, _ := e.Store.OwnerMessagePage("demo-chat", "", "", 50)
	if len(p.Messages) != 1 || len(p.Messages[0].Attachments) != 1 {
		t.Fatal("sent attachment missing")
	}
	if err = e.Store.SaveObservedFiles("demo-chat", "synthetic-echo-id", j.TransactionID, "outgoing", "Synthetic caption", "phone", "delivered", time.Now(), []media.File{{Info: media.Info{ID: "part-1", Name: "synthetic.txt"}, RemoteID: "synthetic-remote"}}); err != nil {
		t.Fatal(err)
	}
	p, _ = e.Store.OwnerMessagePage("demo-chat", "", "", 50)
	if len(p.Messages) != 1 || p.Messages[0].Status != "delivered" {
		t.Fatal("phone echo duplicated send")
	}
	file, err := e.Store.Attachment("demo-chat", j.TransactionID, p.Messages[0].Attachments[0].ID)
	if err != nil || string(file.Data) != "Synthetic attachment" {
		t.Fatal("local original lost", err)
	}
	legacy, _ := e.Store.MessagePage("demo-chat", "", "", 50)
	if len(legacy.Messages) != 0 {
		t.Fatal("private file leaked")
	}
}

type failedReloadTransport struct {
	*transport.Fixture
	fail func()
}

func (f *failedReloadTransport) PrepareFiles(ctx context.Context, files []media.File) ([]media.File, error) {
	f.fail()
	return files, nil
}
func TestUploadJobReloadFailurePropagates(t *testing.T) {
	e, fixture := setup(t)
	enableInbox(t, e)
	e.Pause(false)
	_, err := e.ComposeInboxFiles(context.Background(), "demo-chat", "", "synthetic-db-failure-upload", syntheticUpload())
	if err != nil {
		t.Fatal(err)
	}
	e.Transport = &failedReloadTransport{Fixture: fixture, fail: func() { e.Store.DB.Close() }}
	if err = e.Dispatch(context.Background()); err == nil {
		t.Fatal("job reload database error swallowed")
	}
	if len(fixture.Sent) != 0 {
		t.Fatal("send after failed job reload")
	}
}
func TestAttachmentReadsEnforceExpiryWithoutPruning(t *testing.T) {
	e, _ := setup(t)
	enableInbox(t, e)
	files, _ := media.Prepare(syntheticUpload())
	if err := e.Store.SaveObservedFiles("demo-chat", "synthetic-read", "", "incoming", "", "phone", "", time.Now(), files); err != nil {
		t.Fatal(err)
	}
	page, _ := e.Store.OwnerMessagePage("demo-chat", "", "", 50)
	message := page.Messages[0].ID
	// A read must not try to delete unrelated rows or update job plans.
	if _, err := e.Store.DB.Exec(`CREATE TRIGGER reject_attachment_prune BEFORE DELETE ON messages BEGIN SELECT RAISE(ABORT,'unexpected prune'); END; CREATE TRIGGER reject_attachment_job_update BEFORE UPDATE ON jobs BEGIN SELECT RAISE(ABORT,'unexpected update'); END;`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.InboxAttachment(context.Background(), "demo-chat", message, files[0].ID); err != nil {
		t.Fatal("file read performed retention writes", err)
	}
	if _, err := e.Store.DB.Exec("UPDATE messages SET at='2020-01-01T00:00:00Z' WHERE id=?", message); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.InboxAttachment(context.Background(), "demo-chat", message, files[0].ID); err == nil {
		t.Fatal("expired file returned before scheduled pruning")
	}
	var count int
	e.Store.DB.QueryRow("SELECT count(*) FROM messages WHERE id=?", message).Scan(&count)
	if count != 1 {
		t.Fatal("read pruned expired message")
	}
}
