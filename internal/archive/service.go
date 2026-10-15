// Package archive owns original-file preservation and history backup/recovery.
// It has no command dispatch or message submission capability.
package archive

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"smsbot-server/internal/media"
	"smsbot-server/internal/store"
	"smsbot-server/internal/transport"
	"strings"
	"sync"
	"time"
)

type Policy struct {
	Enabled    bool  `json:"enabled"`
	MaxFileMiB int64 `json:"max_file_mb"`
	QuotaMiB   int64 `json:"quota_mb"`
}

func (p Policy) Validate() error {
	if p.MaxFileMiB < 0 || p.QuotaMiB < 0 || p.MaxFileMiB > math.MaxInt64>>20 || p.QuotaMiB > math.MaxInt64>>20 {
		return errors.New("invalid_archive_policy")
	}
	return nil
}

type FileHost interface {
	Authorize(context.Context, transport.Conversation) (int64, error)
	Open(context.Context, media.File) (io.ReadCloser, error)
	CommitFile(context.Context, transport.Conversation, int64, string, media.File, media.File) error
}
type Service struct {
	closing      bool
	Store        *store.Store
	Root         string
	Host         FileHost
	policyMu     sync.Mutex // policy changes serialize with final file publication
	mu           sync.Mutex // publishes blobs/records, garbage collection and snapshot exports
	tick         sync.Mutex
	cursor       string
	lastGC       time.Time
	nextScan     time.Time
	idleSequence int64
	scanPolicy   Policy
	jobsMu       sync.Mutex
	jobsWait     sync.WaitGroup
	job          *Job
	cancel       context.CancelFunc
}

func New(s *store.Store, root string, h FileHost) *Service {
	return &Service{Store: s, Root: root, Host: h}
}
func (s *Service) Policy() (Policy, error) {
	p := Policy{Enabled: true, QuotaMiB: 10 * 1024}
	b, err := s.Store.GetSecretSetting("archive_files", "archive-files-policy")
	if err != nil {
		return p, err
	}
	if len(b) > 0 {
		err = json.Unmarshal(b, &p)
	}
	return p, err
}
func (s *Service) SetPolicy(p Policy) error {
	s.policyMu.Lock()
	defer s.policyMu.Unlock()
	if err := p.Validate(); err != nil {
		return err
	}
	b, err := json.Marshal(p)
	if err != nil {
		return err
	}
	return s.Store.SetSecretSetting("archive_files", "archive-files-policy", b)
}
func (s *Service) Run(ctx context.Context) {
	stopped := make(chan struct{})
	defer close(stopped)
	go func() {
		select {
		case <-ctx.Done():
			s.stop()
		case <-stopped:
		}
	}()
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			s.Close()
			return
		case <-timer.C:
			s.Tick(ctx)
			timer.Reset(3 * time.Second)
		}
	}
}
func (s *Service) Tick(ctx context.Context) {
	s.tick.Lock()
	defer s.tick.Unlock()
	if ctx.Err() != nil {
		return
	}
	if time.Since(s.lastGC) > time.Minute {
		_ = s.Collect(ctx)
		s.lastGC = time.Now()
	}
	policy, err := s.Policy()
	if err != nil || !policy.Enabled || s.Host == nil {
		return
	}
	// Read the watermark before the batch so an insertion racing the empty
	// batch cannot be absorbed into its idle baseline without being processed.
	var sequence int64
	if err := s.Store.DB.QueryRowContext(ctx, "SELECT coalesce(max(seq),0) FROM message_changes").Scan(&sequence); err != nil {
		return
	}
	if time.Now().Before(s.nextScan) && policy == s.scanPolicy && sequence == s.idleSequence {
		return
	}
	s.scanPolicy = policy
	records, err := s.Store.AttachmentBatch(ctx, s.cursor, 20)
	if err != nil {
		return
	}
	if len(records) == 0 {
		s.cursor = ""
		s.idleSequence = sequence
		s.nextScan = time.Now().Add(time.Minute)
		return
	}
	s.nextScan = time.Time{}
	for _, record := range records {
		if ctx.Err() != nil {
			return
		}
		s.cursor = record.Message
		a, err := s.Store.Archive(record.Conversation)
		if err != nil {
			continue
		}
		for _, file := range record.Files {
			if ctx.Err() != nil {
				return
			}
			if file.BlobID != "" || (file.ArchiveState == "unavailable" && file.RemoteID == "" && len(file.Data) == 0) {
				continue
			}
			if file.ArchiveState == "policy_limited" {
				used, err := s.usage()
				if err != nil || file.RetryPolicy == policyToken(policy, used) {
					continue
				}
			}
			retry, _ := time.Parse(time.RFC3339Nano, file.RetryAt)
			if time.Now().Before(retry) {
				continue
			}
			call, cancel := context.WithTimeout(ctx, 90*time.Second)
			revision, err := s.Host.Authorize(call, a.Conversation)
			if err == nil {
				s.ingest(call, a.Conversation, revision, record.Message, file, policy)
			}
			cancel()
		}
	}
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r contextReader) Read(b []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(b)
}

var errPolicy = errors.New("archive_policy_limit")

type limitedReader struct {
	r io.Reader
	n int64
}

func (r *limitedReader) Read(b []byte) (int, error) {
	if r.n <= 0 {
		return 0, errPolicy
	}
	if int64(len(b)) > r.n {
		b = b[:r.n]
	}
	n, err := r.r.Read(b)
	r.n -= int64(n)
	return n, err
}
func validID(id string) bool { b, err := hex.DecodeString(id); return err == nil && len(b) == 16 }
func (s *Service) blobPath(id string) (string, error) {
	if !validID(id) {
		return "", errors.New("invalid_blob")
	}
	return filepath.Join(s.Root, "blobs", id+".enc"), nil
}
func (s *Service) usage() (int64, error) {
	entries, err := os.ReadDir(filepath.Join(s.Root, "blobs"))
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	var n int64
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			return 0, err
		}
		if !entry.IsDir() && len(entry.Name()) == 36 && strings.HasSuffix(entry.Name(), ".enc") && validID(entry.Name()[:32]) {
			n += info.Size()
		}
	}
	return n, nil
}
func policyToken(p Policy, used int64) string {
	if p.QuotaMiB == 0 {
		used = 0
	}
	return fmt.Sprintf("%d:%d:%d", p.MaxFileMiB, p.QuotaMiB, used)
}
func (s *Service) ingest(ctx context.Context, c transport.Conversation, revision int64, message string, file media.File, p Policy) {
	updated := file
	updated.ArchiveState = "failed"
	updated.ArchiveError = "download_failed"
	updated.Failures++
	updated.RetryAt = time.Now().Add(time.Duration(1<<min(updated.Failures, 8)) * 5 * time.Second).UTC().Format(time.RFC3339Nano)
	limit := p.MaxFileMiB << 20
	fail := func() {
		if ctx.Err() == nil {
			_ = s.Host.CommitFile(ctx, c, revision, message, file, updated)
		}
	}
	used, err := s.usage()
	if err != nil {
		updated.ArchiveError = "archive_storage_failed"
		fail()
		return
	}
	limited := func() {
		updated.ArchiveState = "policy_limited"
		updated.ArchiveError = "archive_policy_limit"
		updated.RetryAt = ""
		updated.Failures = 0
		updated.RetryPolicy = policyToken(p, used)
	}
	if p.QuotaMiB > 0 {
		remaining := (p.QuotaMiB << 20) - used - 1024
		if remaining <= 0 {
			limit = -1
		} else if limit == 0 || remaining < limit {
			limit = remaining
		}
	}
	if limit < 0 || (limit > 0 && file.Size > limit) {
		limited()
		_ = s.Host.CommitFile(ctx, c, revision, message, file, updated)
		return
	}
	if file.RemoteID == "" && len(file.Data) == 0 {
		updated.ArchiveState = "unavailable"
		updated.ArchiveError = "original_unavailable"
		updated.RetryAt = ""
		updated.Failures = 0
		_ = s.Host.CommitFile(ctx, c, revision, message, file, updated)
		return
	}
	source, err := s.Host.Open(ctx, file)
	if err != nil {
		_ = s.Host.CommitFile(ctx, c, revision, message, file, updated)
		return
	}
	defer source.Close()
	if err = os.MkdirAll(filepath.Join(s.Root, "blobs"), 0700); err != nil {
		updated.ArchiveError = "archive_storage_failed"
		fail()
		return
	}
	id := store.ID()
	path, _ := s.blobPath(id)
	output, err := os.OpenFile(path+".part", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		updated.ArchiveError = "archive_storage_failed"
		fail()
		return
	}
	defer os.Remove(path + ".part")
	digest := sha256.New()
	var reader io.Reader = contextReader{ctx, source}
	if limit > 0 {
		reader = &limitedReader{r: reader, n: limit + 1}
	}
	size, err := s.Store.Vault.EncryptStream(output, io.TeeReader(reader, digest), "blob:"+id)
	if err == nil {
		err = output.Sync()
	}
	closeErr := output.Close()
	if err == nil {
		err = closeErr
	}
	if ctx.Err() != nil {
		return
	}
	if err != nil || (limit > 0 && size > limit) {
		if errors.Is(err, errPolicy) || (limit > 0 && size > limit) {
			limited()
		}
		_ = s.Host.CommitFile(ctx, c, revision, message, file, updated)
		return
	}
	if file.Size > 0 && size != file.Size {
		updated.ArchiveError = "original_size_mismatch"
		_ = s.Host.CommitFile(ctx, c, revision, message, file, updated)
		return
	}
	updated.BlobID = id
	updated.Digest = hex.EncodeToString(digest.Sum(nil))
	updated.Size = size
	updated.Data = nil
	updated.ArchiveState = "saved"
	updated.ArchiveError = ""
	updated.RetryAt = ""
	updated.Failures = 0
	updated.RetryPolicy = ""
	s.mu.Lock()
	defer s.mu.Unlock()
	s.policyMu.Lock()
	defer s.policyMu.Unlock()
	current, err := s.Policy()
	if err != nil || current != p || !current.Enabled {
		return
	}
	// Include encryption overhead in the final disk quota check.
	if p.QuotaMiB > 0 {
		used, err = s.usage()
		info, statErr := os.Stat(path + ".part")
		if err != nil || statErr != nil {
			return
		}
		if used > p.QuotaMiB<<20 || info.Size() > (p.QuotaMiB<<20)-used {
			updated = file
			limited()
			_ = s.Host.CommitFile(ctx, c, revision, message, file, updated)
			return
		}
	}
	if err = os.Rename(path+".part", path); err != nil {
		return
	}
	if err = s.Host.CommitFile(ctx, c, revision, message, file, updated); err != nil {
		_ = os.Remove(path)
	}
}

type LocalFile struct {
	io.ReadSeeker
	close func() error
}

func (f *LocalFile) Close() error { return f.close() }
func (s *Service) OpenLocal(file media.File) (*LocalFile, error) {
	path, err := s.blobPath(file.BlobID)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, errors.New("archived_file_missing")
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	stream, err := s.Store.Vault.OpenSeekStream(f, info.Size(), file.Size, "blob:"+file.BlobID)
	if err != nil {
		f.Close()
		return nil, errors.New("archived_file_corrupt")
	}
	return &LocalFile{ReadSeeker: stream, close: f.Close}, nil
}
func (s *Service) Collect(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	refs := map[string]bool{}
	after := ""
	for {
		batch, err := s.Store.AttachmentBatch(ctx, after, 100)
		if err != nil {
			return err
		}
		if len(batch) == 0 {
			break
		}
		for _, record := range batch {
			after = record.Message
			for _, file := range record.Files {
				if file.BlobID != "" {
					refs[file.BlobID] = true
				}
			}
		}
	}
	entries, err := os.ReadDir(filepath.Join(s.Root, "blobs"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		name := entry.Name()

		if strings.HasSuffix(name, ".enc.part") && validID(strings.TrimSuffix(name, ".enc.part")) {
			if err = os.Remove(filepath.Join(s.Root, "blobs", name)); err != nil {
				return err
			}
			continue
		}
		if len(name) == 36 && name[32:] == ".enc" && validID(name[:32]) && !refs[name[:32]] {
			if err = os.Remove(filepath.Join(s.Root, "blobs", name)); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Service) stop() {
	s.jobsMu.Lock()
	s.closing = true
	if s.cancel != nil {
		s.cancel()
	}
	s.jobsMu.Unlock()
}
func (s *Service) Close() { s.stop(); s.jobsWait.Wait() }
