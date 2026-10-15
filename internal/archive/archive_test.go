package archive

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"smsbot-server/internal/media"
	"smsbot-server/internal/store"
	"smsbot-server/internal/transport"
	"smsbot-server/internal/vault"
	"strings"
	"sync"
	"testing"
	"time"
)

func setup(t *testing.T) (*store.Store, *Service) {
	t.Helper()
	v, _ := vault.New(make([]byte, 32))
	root := t.TempDir()
	s, err := store.Open(filepath.Join(root, "state.db"), v)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.DB.Close() })
	return s, New(s, filepath.Join(root, "archive"), nil)
}
func seed(t *testing.T, s *store.Store, files []media.File) {
	t.Helper()
	c := transport.Conversation{ID: "chat", ParticipantID: "peer", Label: "Synthetic conversation", Writable: true}
	if err := s.SaveArchive(store.Archive{Conversation: c, State: "running", Generation: 1}, 0); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveObservedFiles("chat", "message", "", "incoming", "Synthetic original", "phone", "read", time.Now().Add(-time.Hour), files); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveObservedFiles("chat", "message", "", "incoming", "Synthetic edited", "phone", "read", time.Now().Add(-time.Hour), files); err != nil {
		t.Fatal(err)
	}
}
func TestEncryptedAndReadableArchiveRoundTrip(t *testing.T) {
	s, manager := setup(t)
	files, _ := media.Prepare([]media.Upload{{Name: "synthetic.txt", MIME: "text/plain", Data: []byte("Synthetic original bytes")}})
	files = append(files, media.File{Info: media.Info{ID: "expired", Name: "expired.txt"}, RemoteID: "remote-reference", Key: bytes.Repeat([]byte{7}, 32)})
	seed(t, s, files)
	var encrypted bytes.Buffer
	counts, err := manager.Export(context.Background(), &encrypted, Scope{}, nil)
	if err != nil || counts.Messages != 1 || counts.SavedFiles != 1 || counts.MissingFiles != 1 {
		t.Fatal("export", counts, err)
	}
	if bytes.Contains(encrypted.Bytes(), []byte("Synthetic edited")) {
		t.Fatal("encrypted text leaked")
	}
	dst, recovery := setup(t)
	restored, err := recovery.Restore(context.Background(), bytes.NewReader(encrypted.Bytes()), nil)
	if err != nil || counts != restored {
		t.Fatal("restore", restored, err)
	}
	page, err := dst.OwnerMessagePage("chat", "", "", 50)
	if err != nil || len(page.Messages) != 1 || page.Messages[0].Text != "Synthetic edited" || page.Messages[0].Revision != 1 {
		t.Fatal("restored message/revision", err)
	}
	file, err := dst.Attachment("chat", store.Hash("chat\x00message"), files[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	local, err := recovery.OpenLocal(file)
	if err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(local)
	local.Close()
	if err != nil || string(b) != "Synthetic original bytes" {
		t.Fatal("restored original", err)
	}
	var jobs, enrolled, consent int
	dst.DB.QueryRow("SELECT count(*) FROM jobs").Scan(&jobs)
	dst.DB.QueryRow("SELECT count(*) FROM enrollment").Scan(&enrolled)
	dst.DB.QueryRow("SELECT count(*) FROM settings WHERE key='inbox_settings'").Scan(&consent)
	if jobs+enrolled+consent != 0 {
		t.Fatal("restore enabled collection or work")
	}
	if _, err = recovery.Restore(context.Background(), bytes.NewReader(encrypted.Bytes()), nil); err == nil {
		t.Fatal("nonfresh restore accepted")
	}
	var readable bytes.Buffer
	plainCounts, err := manager.Export(context.Background(), &readable, Scope{Plaintext: true}, nil)
	if err != nil || plainCounts != counts {
		t.Fatal("readable export", err)
	}
	tr := tar.NewReader(bytes.NewReader(readable.Bytes()))
	foundText, foundRevision, foundFile := false, false, false
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(tr)
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(h.Name, "files/") {
			foundFile = string(b) == "Synthetic original bytes"
			continue
		}
		if bytes.Contains(b, []byte("Synthetic edited")) {
			foundText = true
		}
		if bytes.Contains(b, []byte("Synthetic original")) {
			foundRevision = true
		}
		if bytes.Contains(b, []byte("remote-reference")) || bytes.Contains(b, []byte("remote_id")) || bytes.Contains(b, []byte("thumbnail_key")) {
			t.Fatal("readable export leaked media credentials")
		}
	}
	if !foundText || !foundRevision || !foundFile {
		t.Fatal("readable export incomplete")
	}
	// Operational backups remain self-contained after file normalization.
	legacy, err := dst.ExportBackup()
	if err != nil {
		t.Fatal("legacy export", err)
	}
	other, _ := setup(t)
	if err = other.RestoreBackup(legacy); err != nil {
		t.Fatal("legacy restore", err)
	}
	legacyFile, err := other.Attachment("chat", store.Hash("chat\x00message"), files[0].ID)
	if err != nil || !bytes.Equal(legacyFile.Data, files[0].Data) || legacyFile.BlobID != "" {
		t.Fatal("legacy lost original", err)
	}
}
func TestTamperCancellationAndFailedWritesDoNotPublish(t *testing.T) {
	s, manager := setup(t)
	seed(t, s, nil)
	var packageBytes bytes.Buffer
	if _, err := manager.Export(context.Background(), &packageBytes, Scope{}, nil); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"tamper", "truncated", "cancelled"} {
		t.Run(kind, func(t *testing.T) {
			dst, recovery := setup(t)
			b := append([]byte(nil), packageBytes.Bytes()...)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch kind {
			case "tamper":
				b[len(b)/2] ^= 1
			case "truncated":
				b = b[:len(b)-20]
			case "cancelled":
				cancel()
			}
			if _, err := recovery.Restore(ctx, bytes.NewReader(b), nil); err == nil {
				t.Fatal("invalid restore succeeded")
			}
			var n int
			dst.DB.QueryRow("SELECT count(*) FROM messages").Scan(&n)
			if n != 0 {
				t.Fatal("partial restored messages published")
			}
		})
	}
	if _, err := manager.Export(context.Background(), failingWriter{}, Scope{}, nil); err == nil {
		t.Fatal("disk failure reported complete")
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("synthetic disk exhausted") }

type repeatedReader struct{ remaining int64 }

func (r *repeatedReader) Read(b []byte) (int, error) {
	if r.remaining == 0 {
		return 0, io.EOF
	}
	n := int(min(int64(len(b)), r.remaining))
	for i := range b[:n] {
		b[i] = byte(i % 251)
	}
	r.remaining -= int64(n)
	return n, nil
}
func TestStreamingArchiveBeyondLogicalJSONLimit(t *testing.T) {
	if testing.Short() {
		t.Skip("large streaming acceptance")
	}
	s, manager := setup(t)
	const size = 140 << 20
	id := store.ID()
	path, _ := manager.blobPath(id)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	output, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.New()
	if _, err = s.Vault.EncryptStream(output, io.TeeReader(&repeatedReader{remaining: size}, digest), "blob:"+id); err != nil {
		t.Fatal(err)
	}
	output.Close()
	file := media.File{Info: media.Info{ID: "large", Name: "large.bin", MIME: "application/octet-stream", Size: size, ArchiveState: "saved"}, BlobID: id, Digest: hex.EncodeToString(digest.Sum(nil))}
	seed(t, s, []media.File{file})
	packagePath := filepath.Join(t.TempDir(), "archive.sma")
	f, err := os.Create(packagePath)
	if err != nil {
		t.Fatal(err)
	}
	runtime.GC()
	var before, peak runtime.MemStats
	runtime.ReadMemStats(&before)
	counts, err := manager.Export(context.Background(), f, Scope{}, func(Counts) {
		var now runtime.MemStats
		runtime.ReadMemStats(&now)
		if now.HeapAlloc > peak.HeapAlloc {
			peak = now
		}
	})
	f.Close()
	if err != nil || counts.Bytes != size {
		t.Fatal("large export", err)
	}
	info, _ := os.Stat(packagePath)
	if info.Size() <= 128<<20 {
		t.Fatal("package did not exceed old logical limit")
	}
	_, recovery := setup(t)
	f, err = os.Open(packagePath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err = recovery.Restore(context.Background(), f, nil); err != nil {
		t.Fatal("large restore", err)
	}
	if peak.HeapAlloc > before.HeapAlloc+32<<20 {
		t.Fatalf("export heap grew with file size: %d", peak.HeapAlloc-before.HeapAlloc)
	}
	t.Logf("streaming package bytes=%d observed export heap delta=%d", info.Size(), int64(peak.HeapAlloc)-int64(before.HeapAlloc))
}
func TestScopeFilterAndConcurrentCaptureSnapshot(t *testing.T) {
	s, manager := setup(t)
	seed(t, s, nil)
	var packageBytes bytes.Buffer
	captured := false
	_, err := manager.Export(context.Background(), &packageBytes, Scope{}, func(Counts) {
		if captured {
			return
		}
		captured = true
		if err := s.SaveObserved("chat", "after-snapshot", "", "incoming", "Synthetic next capture", "phone", "", time.Now()); err != nil {
			t.Error(err)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	dst, recovery := setup(t)
	counts, err := recovery.Restore(context.Background(), bytes.NewReader(packageBytes.Bytes()), nil)
	if err != nil || counts.Messages != 1 {
		t.Fatal("snapshot boundary changed", err)
	}
	var n int
	dst.DB.QueryRow("SELECT count(*) FROM messages").Scan(&n)
	if n != 1 {
		t.Fatal("inconsistent snapshot")
	}
	var filtered bytes.Buffer
	counts, err = manager.Export(context.Background(), &filtered, Scope{Since: time.Now().Add(time.Hour).Format(time.RFC3339)}, nil)
	if err != nil || counts.Messages != 0 {
		t.Fatal("date filter failed", err)
	}
	var unrelated bytes.Buffer
	counts, err = manager.Export(context.Background(), &unrelated, Scope{Conversations: []string{"absent"}}, nil)
	if err != nil || counts.Messages != 0 {
		t.Fatal("scope filter failed", err)
	}
}

type fileHost struct {
	s              *store.Store
	data           []byte
	opened         chan struct{}
	release        chan struct{}
	revoked        bool
	authorizations int
	mu             sync.Mutex
}

func (h *fileHost) Authorize(ctx context.Context, c transport.Conversation) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.authorizations++
	if h.revoked {
		return 0, errors.New("revoked")
	}
	return 1, nil
}
func (h *fileHost) Open(ctx context.Context, f media.File) (io.ReadCloser, error) {
	if h.opened != nil {
		close(h.opened)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-h.release:
		}
	}
	return io.NopCloser(bytes.NewReader(h.data)), nil
}
func (h *fileHost) CommitFile(ctx context.Context, c transport.Conversation, revision int64, message string, expected, updated media.File) error {
	if _, err := h.Authorize(ctx, c); err != nil {
		return err
	}
	return h.s.UpdateArchivedFile(ctx, message, expected, updated)
}
func TestFileArchiveRestartQuotasAndRevocation(t *testing.T) {
	for _, mode := range []string{"saved", "near-quota", "quota", "unknown-size-limit", "revoked", "cancelled", "storage-failed"} {
		t.Run(mode, func(t *testing.T) {
			s, manager := setup(t)
			data := bytes.Repeat([]byte("synthetic"), 100)
			file := media.File{Info: media.Info{ID: "original", Name: "original.bin", Size: int64(len(data))}, RemoteID: "synthetic-remote", Key: make([]byte, 32)}
			policy := Policy{Enabled: true, QuotaMiB: 1}
			switch mode {
			case "near-quota":
				data = make([]byte, 700<<10)
				file.Size = int64(len(data))
			case "quota":
				file.Size = 2 << 20
			case "unknown-size-limit":
				file.Size = 0
				policy.MaxFileMiB = 1
				policy.QuotaMiB = 0
				data = make([]byte, (1<<20)+99)
			}
			if err := manager.SetPolicy(policy); err != nil {
				t.Fatal(err)
			}
			seed(t, s, []media.File{file})
			host := &fileHost{s: s, data: data}
			manager.Host = host
			if mode == "storage-failed" {
				if err := os.MkdirAll(manager.Root, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(manager.Root, "blobs"), []byte("synthetic blocker"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "revoked" || mode == "cancelled" {
				host.opened = make(chan struct{})
				host.release = make(chan struct{})
				done := make(chan struct{})
				go func() { manager.Tick(ctx); close(done) }()
				<-host.opened
				if mode == "revoked" {
					host.mu.Lock()
					host.revoked = true
					host.mu.Unlock()
				} else {
					cancel()
				}
				close(host.release)
				<-done
			} else {
				manager.Tick(ctx)
			}
			current, err := s.Attachment("chat", store.Hash("chat\x00message"), "original")
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "saved", "near-quota":
				if current.BlobID == "" || current.ArchiveState != "saved" || len(current.Data) > 0 {
					t.Fatal("original not normalized")
				}
				host.mu.Lock()
				host.revoked = true
				host.mu.Unlock()
				restarted := New(s, manager.Root, host)
				local, err := restarted.OpenLocal(current)
				if err != nil {
					t.Fatal(err)
				}
				saved, err := io.ReadAll(local)
				local.Close()
				if err != nil || !bytes.Equal(saved, data) {
					t.Fatal("offline original", err)
				}
				if err = s.SaveObservedFiles("chat", "message", "", "incoming", "Synthetic echo", "phone", "read", time.Now(), []media.File{file}); err != nil {
					t.Fatal(err)
				}
				echo, _ := s.Attachment("chat", store.Hash("chat\x00message"), "original")
				if echo.BlobID != current.BlobID {
					t.Fatal("echo lost original")
				}
				s.DB.Exec("DELETE FROM messages")
				if err = restarted.Collect(context.Background()); err != nil {
					t.Fatal(err)
				}
				path, _ := manager.blobPath(current.BlobID)
				if _, err = os.Stat(path); !os.IsNotExist(err) {
					t.Fatal("retention left orphaned bytes")
				}
			case "quota", "unknown-size-limit":
				if current.BlobID != "" || current.ArchiveState != "policy_limited" {
					t.Fatal("quota status", current.ArchiveState)
				}
			case "storage-failed":
				if current.ArchiveState != "failed" || current.ArchiveError != "archive_storage_failed" {
					t.Fatal("disk error not visible")
				}
			default:
				if current.BlobID != "" {
					t.Fatal("revoked or cancelled read published bytes")
				}
			}
		})
	}
}
func TestExportJobIdempotencyCancellationAndInterruptedStatus(t *testing.T) {
	s, manager := setup(t)
	seed(t, s, nil)
	manager.mu.Lock() // hold publication/export to make the concurrent start deterministic
	first, err := manager.StartExport(Scope{})
	if err != nil {
		t.Fatal(err)
	}
	second, err := manager.StartExport(Scope{})
	if err != nil || second.ID != first.ID {
		t.Fatal("double click started another job")
	}
	manager.Cancel()
	manager.mu.Unlock()
	manager.jobsWait.Wait()
	job, err := manager.Job()
	if err != nil || job.State != "cancelled" {
		t.Fatal("job cancellation", err)
	}
	if _, err = manager.ExportFile(job.ID); err == nil {
		t.Fatal("cancelled package downloadable")
	}
	b, _ := jsonMarshalJob(Job{ID: store.ID(), State: "running"})
	if err = s.SetSecretSetting("archive_backup_job", "archive-backup-job", b); err != nil {
		t.Fatal(err)
	}
	restarted := New(s, manager.Root, nil)
	job, err = restarted.Job()
	if err != nil || job.State != "interrupted" {
		t.Fatal("restart silently completed partial job")
	}
	manager.Close()
	if _, err = manager.StartExport(Scope{}); err == nil {
		t.Fatal("job started after shutdown")
	}
}
func jsonMarshalJob(j Job) ([]byte, error) {
	return []byte(fmt.Sprintf(`{"id":%q,"state":%q}`, j.ID, j.State)), nil
}

func TestArchivedSentOriginalSurvivesEchoAndCollection(t *testing.T) {
	s, manager := setup(t)
	seed(t, s, nil)
	files, err := media.Prepare([]media.Upload{{Name: "synthetic.bin", Data: []byte("Synthetic sent original")}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Enqueue("send-job", "chat", "manual", store.Plan{}); err != nil {
		t.Fatal(err)
	}
	if err = s.Submitting("send-job", "send-txn"); err != nil {
		t.Fatal(err)
	}
	if err = s.SaveSentFiles("send-txn", "chat", "", files); err != nil {
		t.Fatal(err)
	}
	manager.Host = &fileHost{s: s, data: files[0].Data}
	manager.Tick(context.Background())
	before, err := s.Attachment("chat", "send-txn", files[0].ID)
	if err != nil || before.BlobID == "" {
		t.Fatal("sent original not archived", err)
	}
	echo := []media.File{{Info: media.Info{ID: "part-1", Name: "phone.bin", Size: int64(len(files[0].Data))}, RemoteID: "phone-reference", Key: make([]byte, 32)}}
	for _, txn := range []string{"send-txn", ""} {
		if err = s.SaveObservedFiles("chat", "phone-echo", txn, "outgoing", "", "phone", "read", time.Now(), echo); err != nil {
			t.Fatal(err)
		}
	}
	if err = manager.Collect(context.Background()); err != nil {
		t.Fatal(err)
	}
	after, err := s.Attachment("chat", "send-txn", files[0].ID)
	if err != nil || after.BlobID != before.BlobID {
		t.Fatal("echo discarded local original", err)
	}
	f, err := manager.OpenLocal(after)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(f)
	f.Close()
	if err != nil || !bytes.Equal(got, files[0].Data) {
		t.Fatal("GC lost sent original", err)
	}
}

func TestIncomingChangedSourceReplacesArchivedReference(t *testing.T) {
	s, manager := setup(t)
	file := media.File{Info: media.Info{ID: "part-1", Name: "synthetic.bin", Size: 7}, RemoteID: "source-one", Key: make([]byte, 32)}
	seed(t, s, []media.File{file})
	manager.Host = &fileHost{s: s, data: []byte("example")}
	manager.Tick(context.Background())
	before, err := s.Attachment("chat", store.Hash("chat\x00message"), file.ID)
	if err != nil || before.BlobID == "" {
		t.Fatal(err)
	}
	file.RemoteID = "source-two"
	if err = s.SaveObservedFiles("chat", "message", "", "incoming", "Synthetic changed file", "phone", "", time.Now(), []media.File{file}); err != nil {
		t.Fatal(err)
	}
	after, err := s.Attachment("chat", store.Hash("chat\x00message"), file.ID)
	if err != nil || after.BlobID != "" || after.RemoteID != "source-two" {
		t.Fatal("incoming replacement retained stale original", err)
	}
	if err = manager.Collect(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err = manager.OpenLocal(before); err == nil {
		t.Fatal("unreferenced changed source survived GC")
	}
}

func TestRestoreIgnoresBackgroundAcquisitionQuota(t *testing.T) {
	s, source := setup(t)
	files, err := media.Prepare([]media.Upload{{Name: "synthetic-large.bin", Data: make([]byte, 2<<20)}})
	if err != nil {
		t.Fatal(err)
	}
	seed(t, s, files)
	var pkg bytes.Buffer
	if _, err = source.Export(context.Background(), &pkg, Scope{}, nil); err != nil {
		t.Fatal(err)
	}
	dst, target := setup(t)
	if err = target.SetPolicy(Policy{Enabled: true, QuotaMiB: 1}); err != nil {
		t.Fatal(err)
	}
	counts, err := target.Restore(context.Background(), bytes.NewReader(pkg.Bytes()), nil)
	if err != nil || counts.Bytes != 2<<20 {
		t.Fatal("valid restore limited by background quota", err)
	}
	f, err := dst.Attachment("chat", store.Hash("chat\x00message"), files[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	local, err := target.OpenLocal(f)
	if err != nil {
		t.Fatal(err)
	}
	n, err := io.Copy(io.Discard, local)
	local.Close()
	if err != nil || n != 2<<20 {
		t.Fatal("restored original missing", err)
	}
}

func TestTerminalFilesWaitForChangedConditions(t *testing.T) {
	for _, mode := range []string{"unavailable", "policy", "quota-space"} {
		t.Run(mode, func(t *testing.T) {
			s, manager := setup(t)
			file := media.File{Info: media.Info{ID: "part-1", Name: "synthetic.bin", Size: 32}, RemoteID: "source", Key: make([]byte, 32)}
			policy := Policy{Enabled: true, QuotaMiB: 0}
			if mode == "unavailable" {
				file.RemoteID = ""
			} else if mode == "policy" {
				file.Size = 2 << 20
				policy.MaxFileMiB = 1
			} else {
				policy.QuotaMiB = 1
			}
			if err := manager.SetPolicy(policy); err != nil {
				t.Fatal(err)
			}
			seed(t, s, []media.File{file})
			host := &fileHost{s: s, data: make([]byte, file.Size)}
			manager.Host = host
			var blocker string
			if mode == "quota-space" {
				blocker, _ = manager.blobPath(store.ID())
				if err := os.MkdirAll(filepath.Dir(blocker), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(blocker, make([]byte, 1<<20), 0600); err != nil {
					t.Fatal(err)
				}
				manager.lastGC = time.Now()
			}
			manager.Tick(context.Background())
			attempts := host.authorizations
			for i := 0; i < 3; i++ {
				manager.cursor = ""
				manager.nextScan = time.Time{}
				manager.Tick(context.Background())
			}
			if host.authorizations != attempts {
				t.Fatal("terminal file was retried without changed conditions")
			}
			current, err := s.Attachment("chat", store.Hash("chat\x00message"), file.ID)
			if err != nil || current.RetryAt != "" {
				t.Fatal("terminal file kept a retry timer", err)
			}
			if mode == "unavailable" {
				file.RemoteID = "new-source"
				if err = s.SaveObservedFiles("chat", "message", "", "incoming", "New source", "phone", "", time.Now(), []media.File{file}); err != nil {
					t.Fatal(err)
				}
			} else if mode == "policy" {
				policy.MaxFileMiB = 3
				if err = manager.SetPolicy(policy); err != nil {
					t.Fatal(err)
				}
			} else {
				if err = os.Remove(blocker); err != nil {
					t.Fatal(err)
				}
			}
			manager.cursor = ""
			manager.nextScan = time.Time{}
			manager.Tick(context.Background())
			current, err = s.Attachment("chat", store.Hash("chat\x00message"), file.ID)
			if err != nil || current.BlobID == "" {
				t.Fatal("changed conditions did not resume preservation", err)
			}
		})
	}
}

func TestIdleArchiveScanSleepsAndWakesForNewFiles(t *testing.T) {
	s, manager := setup(t)
	seed(t, s, nil)
	host := &fileHost{s: s, data: []byte("new file")}
	manager.Host = host
	manager.Tick(context.Background())
	deadline := manager.nextScan
	if !deadline.After(time.Now()) {
		t.Fatal("idle worker did not enter cooldown")
	}
	manager.Tick(context.Background())
	if manager.nextScan != deadline {
		t.Fatal("idle worker rescanned unchanged archive")
	}
	file := media.File{Info: media.Info{ID: "part-1", Name: "new.bin", Size: 8}, RemoteID: "new-source", Key: make([]byte, 32)}
	if err := s.SaveObservedFiles("chat", "new-message", "", "incoming", "", "phone", "", time.Now(), []media.File{file}); err != nil {
		t.Fatal(err)
	}
	manager.Tick(context.Background())
	current, err := s.Attachment("chat", store.Hash("chat\x00new-message"), file.ID)
	if err != nil || current.BlobID == "" {
		t.Fatal("new attachment did not wake idle worker", err)
	}
}
