package archive

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/deusical/smsbot-server/internal/media"
	"github.com/deusical/smsbot-server/internal/store"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"
)

type Scope struct {
	Plaintext     bool     `json:"plaintext,omitempty"`
	Conversations []string `json:"conversations,omitempty"`
	Since         string   `json:"since,omitempty"`
}

func (s Scope) Validate() error {
	if len(s.Conversations) > 500 {
		return errors.New("invalid_backup_scope")
	}
	if s.Since != "" {
		_, err := time.Parse(time.RFC3339, s.Since)
		return err
	}
	return nil
}

type Counts struct {
	Messages     int64 `json:"messages"`
	SavedFiles   int64 `json:"saved_files"`
	MissingFiles int64 `json:"missing_files"`
	Bytes        int64 `json:"bytes"`
}
type Job struct {
	ID     string `json:"id"`
	State  string `json:"state"`
	Error  string `json:"error,omitempty"`
	Scope  Scope  `json:"scope"`
	Counts Counts `json:"counts"`
}
type manifest struct {
	Version int    `json:"version"`
	Scope   Scope  `json:"scope"`
	Counts  Counts `json:"counts"`
	Digest  string `json:"digest"`
}
type archiveRow struct {
	Interaction  string         `json:"interaction,omitempty"`
	Kind         string         `json:"kind"`
	ID           string         `json:"id,omitempty"`
	Conversation string         `json:"conversation,omitempty"`
	Direction    string         `json:"direction,omitempty"`
	At           string         `json:"at,omitempty"`
	Payload      string         `json:"payload,omitempty"`
	Attachments  string         `json:"attachments,omitempty"`
	Origin       string         `json:"origin,omitempty"`
	Status       string         `json:"status,omitempty"`
	Private      bool           `json:"private"`
	Message      string         `json:"message,omitempty"`
	Alias        string         `json:"alias,omitempty"`
	Revision     int            `json:"revision,omitempty"`
	Observed     string         `json:"observed,omitempty"`
	Archive      *store.Archive `json:"archive,omitempty"`
	Retention    int            `json:"retention,omitempty"`
}
type blobEntry struct {
	file media.File
	path string
}

func (s *Service) Job() (*Job, error) {
	s.jobsMu.Lock()
	defer s.jobsMu.Unlock()
	if s.job != nil {
		copy := *s.job
		return &copy, nil
	}
	b, err := s.Store.GetSecretSetting("archive_backup_job", "archive-backup-job")
	if err != nil || len(b) == 0 {
		return nil, err
	}
	var job Job
	if err = json.Unmarshal(b, &job); err != nil {
		return nil, err
	}
	if job.State == "running" {
		if validID(job.ID) {
			_ = os.Remove(filepath.Join(s.Root, "exports", job.ID+exportSuffix(job.Scope)+".part"))
		}
		job.State = "interrupted"
		job.Error = "restart_interrupted_export"
	}
	s.job = &job
	return &job, nil
}
func (s *Service) saveJobLocked() error {
	b, err := json.Marshal(s.job)
	if err != nil {
		return err
	}
	return s.Store.SetSecretSetting("archive_backup_job", "archive-backup-job", b)
}
func (s *Service) Cancel() {
	s.jobsMu.Lock()
	defer s.jobsMu.Unlock()
	if s.cancel != nil {
		s.cancel()
	}
}
func (s *Service) StartExport(scope Scope) (*Job, error) {
	if err := scope.Validate(); err != nil {
		return nil, errors.New("invalid_backup_scope")
	}
	if _, err := s.Job(); err != nil {
		return nil, errors.New("backup_status_unavailable")
	}
	if len(scope.Conversations) == 0 {
		scope.Conversations = nil
	} else {
		scope.Conversations = append([]string(nil), scope.Conversations...)
	}
	s.jobsMu.Lock()
	defer s.jobsMu.Unlock()
	if s.closing {
		return nil, errors.New("archive_stopping")
	}
	if s.cancel != nil {
		if s.job != nil && reflect.DeepEqual(s.job.Scope, scope) {
			copy := *s.job
			return &copy, nil
		}
		return nil, errors.New("backup_busy")
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	job := &Job{ID: store.ID(), State: "running", Scope: scope}
	s.job = job
	if err := s.saveJobLocked(); err != nil {
		s.cancel = nil
		cancel()
		s.job = nil
		return nil, errors.New("cannot_persist_archive_job")
	}

	if err := s.cleanOldExports(job.ID); err != nil {
		s.cancel = nil
		cancel()
		s.job.State = "failed"
		s.job.Error = "cannot_remove_previous_export"
		_ = s.saveJobLocked()
		return nil, errors.New("cannot_remove_previous_export")
	}
	s.jobsWait.Add(1)
	go func() {
		defer s.jobsWait.Done()
		err := os.MkdirAll(filepath.Join(s.Root, "exports"), 0700)
		output := filepath.Join(s.Root, "exports", job.ID+exportSuffix(scope))
		var f *os.File
		if err == nil {
			f, err = os.OpenFile(output+".part", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		}
		var counts Counts
		if err == nil {
			counts, err = s.Export(ctx, f, scope, func(c Counts) { s.jobsMu.Lock(); s.job.Counts = c; s.jobsMu.Unlock() })
		}
		if f != nil {
			if err == nil {
				err = f.Sync()
			}
			ce := f.Close()
			if err == nil {
				err = ce
			}
		}
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		if err == nil {
			err = os.Rename(output+".part", output)
		}
		if err != nil {
			_ = os.Remove(output + ".part")
		}
		s.jobsMu.Lock()
		defer s.jobsMu.Unlock()
		s.job.Counts = counts
		s.job.State = "complete"
		s.job.Error = ""
		if err != nil {
			s.job.State = "failed"
			s.job.Error = "archive_export_failed"
			if ctx.Err() != nil {
				s.job.State = "cancelled"
				s.job.Error = ""
			}
		}
		s.cancel = nil
		cancel()
		if err := s.saveJobLocked(); err != nil {
			s.job.State = "failed"
			s.job.Error = "cannot_persist_archive_job"
		}
	}()
	copy := *job
	return &copy, nil
}
func (s *Service) ExportFile(id string) (*os.File, error) {
	job, err := s.Job()
	if err != nil || job == nil || job.ID != id || job.State != "complete" || !validID(id) {
		return nil, errors.New("backup_not_complete")
	}
	return os.Open(filepath.Join(s.Root, "exports", id+exportSuffix(job.Scope)))
}

// snapshot uses a separate read connection. WAL writers can continue collecting
// text while VACUUM INTO copies a consistent, already encrypted database image.
func (s *Service) snapshot(ctx context.Context, output string) (*store.Store, error) {
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(s.Store.Path)+"?mode=ro")
	if err != nil {
		return nil, err
	}
	defer db.Close()
	if _, err = db.ExecContext(ctx, "VACUUM INTO ?", output); err != nil {
		return nil, err
	}
	copy, err := sql.Open("sqlite", "file:"+filepath.ToSlash(output)+"?mode=ro")
	if err != nil {
		return nil, err
	}
	copy.SetMaxOpenConns(1)
	return &store.Store{DB: copy, Vault: s.Store.Vault, Path: output}, nil
}
func scopeWhere(scope Scope) (string, []any) {
	where := "1=1"
	var args []any
	if len(scope.Conversations) > 0 {
		where += " AND m.conversation IN (" + strings.TrimSuffix(strings.Repeat("?,", len(scope.Conversations)), ",") + ")"
		for _, id := range scope.Conversations {
			args = append(args, id)
		}
	}
	if scope.Since != "" {
		since, _ := time.Parse(time.RFC3339, scope.Since)
		where += " AND m.at>=?"
		args = append(args, since.UTC().Format("2006-01-02T15:04:05.000000000Z"))
	}
	where += " AND m.conversation IN (SELECT conversation FROM archive_conversations)"
	return where, args
}
func (s *Service) Export(ctx context.Context, w io.Writer, scope Scope, progress func(Counts)) (Counts, error) {
	if err := scope.Validate(); err != nil {
		return Counts{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(filepath.Join(s.Root, "exports"), 0700); err != nil {
		return Counts{}, err
	}
	temp, err := os.MkdirTemp(filepath.Join(s.Root, "exports"), "snapshot-")
	if err != nil {
		return Counts{}, err
	}
	defer os.RemoveAll(temp)
	snapshot, err := s.snapshot(ctx, filepath.Join(temp, "state.db"))
	if err != nil {
		return Counts{}, err
	}
	defer snapshot.DB.Close()
	if scope.Plaintext {
		return s.writePackage(ctx, w, snapshot, temp, scope, progress)
	}
	reader, writer := io.Pipe()
	done := make(chan struct {
		counts Counts
		err    error
	}, 1)
	go func() {
		counts, err := s.writePackage(ctx, writer, snapshot, temp, scope, progress)
		writer.CloseWithError(err)
		done <- struct {
			counts Counts
			err    error
		}{counts, err}
	}()
	_, err = s.Store.Vault.EncryptStream(w, contextReader{ctx, reader}, "archive-backup-v1")
	reader.CloseWithError(err)
	result := <-done
	if err != nil {
		return result.counts, err
	}
	return result.counts, result.err
}
func (s *Service) writePackage(ctx context.Context, w io.Writer, snapshot *store.Store, temp string, scope Scope, progress func(Counts)) (Counts, error) {
	tw := tar.NewWriter(w)
	hash := sha256.New()
	counts := Counts{}
	index := 0
	write := func(name string, size int64, r io.Reader, hashed bool) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := tw.WriteHeader(&tar.Header{Name: name, Size: size, Mode: 0600, Typeflag: tar.TypeReg}); err != nil {
			return err
		}
		var out io.Writer = tw
		if hashed {
			fmt.Fprintf(hash, "%s:%d:", name, size)
			out = io.MultiWriter(tw, hash)
		}
		n, err := io.Copy(out, contextReader{ctx, r})
		if err != nil {
			return err
		}
		if n != size {
			return io.ErrUnexpectedEOF
		}
		return nil
	}
	row := func(r archiveRow) error {
		var value any = r
		if scope.Plaintext {
			var err error
			value, err = s.publicRow(r)
			if err != nil {
				return err
			}
			if value == nil {
				return nil
			}
		}
		b, err := json.Marshal(value)
		if err != nil {
			return err
		}
		if !scope.Plaintext && len(b) > 16<<20 {
			return errors.New("archive_record_too_large")
		}
		index++
		return write(fmt.Sprintf("records/%09d.json", index), int64(len(b)), strings.NewReader(string(b)), true)
	}
	archives, err := snapshot.Archives()
	if err != nil {
		return counts, err
	}
	selected := map[string]bool{}
	for _, id := range scope.Conversations {
		selected[id] = true
	}
	for _, a := range archives {
		if len(selected) > 0 && !selected[a.Conversation.ID] {
			continue
		}
		var days int
		if err = snapshot.DB.QueryRowContext(ctx, "SELECT retention_days FROM archive_conversations WHERE conversation=?", a.Conversation.ID).Scan(&days); err != nil {
			return counts, err
		}
		copy := a
		if err = row(archiveRow{Kind: "archive", Archive: &copy, Retention: days}); err != nil {
			return counts, err
		}
	}
	where, args := scopeWhere(scope)
	rows, err := snapshot.DB.QueryContext(ctx, "SELECT m.id,m.conversation,m.direction,m.at,m.payload,coalesce(meta.origin,'bot'),coalesce(meta.status,''),coalesce(meta.owner_only,0),coalesce(a.payload,''),coalesce(i.payload,'') FROM messages m LEFT JOIN message_meta meta ON meta.message=m.id LEFT JOIN message_attachments a ON a.message=m.id LEFT JOIN message_interactions i ON i.message=m.id WHERE "+where+" ORDER BY m.id", args...)
	if err != nil {
		return counts, err
	}
	blobs := map[string]blobEntry{}
	for rows.Next() {
		r := archiveRow{Kind: "message"}
		if err = rows.Scan(&r.ID, &r.Conversation, &r.Direction, &r.At, &r.Payload, &r.Origin, &r.Status, &r.Private, &r.Attachments, &r.Interaction); err != nil {
			break
		}
		if r.Attachments != "" {
			var files []media.File
			files, err = snapshot.OpenAttachmentPayload(r.ID, r.Attachments)
			if err != nil {
				break
			}
			for i := range files {
				file := &files[i]
				if len(file.Data) > 0 {
					id := store.ID()
					path := filepath.Join(temp, id+".enc")
					output, openErr := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
					if openErr != nil {
						err = openErr
						break
					}
					digest := sha256.Sum256(file.Data)
					size, writeErr := s.Store.Vault.EncryptStream(output, bytes.NewReader(file.Data), "blob:"+id)
					closeErr := output.Close()
					if writeErr != nil {
						err = writeErr
						break
					}
					if closeErr != nil {
						err = closeErr
						break
					}
					file.Data = nil
					file.BlobID = id
					file.Digest = hex.EncodeToString(digest[:])
					file.Size = size
					file.ArchiveState = "saved"
					file.ArchiveError = ""
					blobs[id] = blobEntry{*file, path}
				} else if file.BlobID != "" {
					local, openErr := s.OpenLocal(*file)
					if openErr == nil {
						digest := sha256.New()
						_, openErr = io.Copy(digest, contextReader{ctx, local})
						local.Close()
						if hex.EncodeToString(digest.Sum(nil)) != file.Digest {
							openErr = errors.New("blob_integrity_failed")
						}
					}
					if openErr != nil {
						file.BlobID = ""
						file.ArchiveState = "unavailable"
						file.ArchiveError = "original_missing_or_corrupt"
					} else {
						path, _ := s.blobPath(file.BlobID)
						blobs[file.BlobID] = blobEntry{*file, path}
					}
				}
				if file.BlobID == "" {
					counts.MissingFiles++
				} else {
					counts.SavedFiles++
				}
			}
			if err != nil {
				break
			}
			b, marshalErr := json.Marshal(files)
			if marshalErr != nil {
				err = marshalErr
				break
			}
			r.Attachments, err = s.Store.Vault.Seal("attachments:"+r.ID, b)
			if err != nil {
				break
			}
		}
		if err = row(r); err != nil {
			break
		}
		counts.Messages++
		if progress != nil {
			progress(counts)
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return counts, err
	}
	rows, err = snapshot.DB.QueryContext(ctx, "SELECT a.alias,a.message FROM message_alias a JOIN messages m ON m.id=a.message WHERE "+where, args...)
	if err != nil {
		return counts, err
	}
	for rows.Next() {
		r := archiveRow{Kind: "alias"}
		if err = rows.Scan(&r.Alias, &r.Message); err != nil {
			break
		}
		if err = row(r); err != nil {
			break
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return counts, err
	}
	rows, err = snapshot.DB.QueryContext(ctx, "SELECT r.message,r.revision,r.payload,r.observed FROM message_revisions r JOIN messages m ON m.id=r.message WHERE "+where, args...)
	if err != nil {
		return counts, err
	}
	for rows.Next() {
		r := archiveRow{Kind: "revision"}
		if err = rows.Scan(&r.Message, &r.Revision, &r.Payload, &r.Observed); err != nil {
			break
		}
		if err = row(r); err != nil {
			break
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return counts, err
	}
	for id, blob := range blobs {
		f, err := os.Open(blob.path)
		if err != nil {
			return counts, err
		}
		info, err := f.Stat()
		if err == nil {
			if scope.Plaintext {
				var original io.ReadSeeker
				original, err = s.Store.Vault.OpenSeekStream(f, info.Size(), blob.file.Size, "blob:"+id)
				if err == nil {
					err = write("files/"+id+"/"+media.SafeName(blob.file.Name), blob.file.Size, original, true)
				}
			} else {
				err = write("blobs/"+id, info.Size(), f, true)
			}
		}
		f.Close()
		if err != nil {
			return counts, err
		}
		counts.Bytes += blob.file.Size
		if progress != nil {
			progress(counts)
		}
	}
	b, err := json.Marshal(manifest{Version: 1, Scope: scope, Counts: counts, Digest: hex.EncodeToString(hash.Sum(nil))})
	if err != nil {
		return counts, err
	}
	if err = write("manifest.json", int64(len(b)), strings.NewReader(string(b)), false); err != nil {
		return counts, err
	}
	return counts, tw.Close()
}

func (s *Service) Restore(ctx context.Context, r io.Reader, progress func(Counts)) (Counts, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(s.Root, 0700); err != nil {
		return Counts{}, err
	}
	temp, err := os.MkdirTemp(s.Root, "restore-")
	if err != nil {
		return Counts{}, err
	}
	defer os.RemoveAll(temp)
	staged, err := store.Open(filepath.Join(temp, "state.db"), s.Store.Vault)
	if err != nil {
		return Counts{}, err
	}
	defer staged.DB.Close()
	decrypted, err := s.Store.Vault.DecryptStream(contextReader{ctx, r}, "archive-backup-v1")
	if err != nil {
		return Counts{}, err
	}
	tr := tar.NewReader(decrypted)
	hash := sha256.New()
	var counts Counts
	var m manifest
	seenManifest := false
	seenNames := map[string]bool{}
	blobs := map[string]string{}
	// Offline recovery is bounded by filesystem writes, not the fresh
	// destination's background acquisition quota. A valid source may exceed it.
	for {
		header, nextErr := tr.Next()
		if nextErr == io.EOF {
			break
		}
		if nextErr != nil {
			return counts, errors.New("invalid_archive_package")
		}
		if seenManifest || seenNames[header.Name] || header.Typeflag != tar.TypeReg || header.Size < 0 {
			return counts, errors.New("invalid_archive_package")
		}
		seenNames[header.Name] = true
		if header.Name == "manifest.json" {
			if header.Size > 65536 {
				return counts, errors.New("invalid_archive_manifest")
			}
			if err = json.NewDecoder(io.LimitReader(tr, 65537)).Decode(&m); err != nil {
				return counts, errors.New("invalid_archive_manifest")
			}
			seenManifest = true
			continue
		}
		fmt.Fprintf(hash, "%s:%d:", header.Name, header.Size)
		entry := io.TeeReader(tr, hash)
		if strings.HasPrefix(header.Name, "records/") && strings.HasSuffix(header.Name, ".json") {
			if header.Size > 16<<20 {
				return counts, errors.New("archive_record_too_large")
			}
			b, readErr := io.ReadAll(entry)
			if readErr != nil {
				return counts, readErr
			}
			var row archiveRow
			if json.Unmarshal(b, &row) != nil {
				return counts, errors.New("invalid_archive_record")
			}
			if err = s.restoreRow(ctx, staged, row, &counts); err != nil {
				return counts, errors.New("invalid_archive_record")
			}
		} else if strings.HasPrefix(header.Name, "blobs/") && validID(strings.TrimPrefix(header.Name, "blobs/")) {
			id := strings.TrimPrefix(header.Name, "blobs/")
			path := filepath.Join(temp, id+".enc")
			f, openErr := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if openErr != nil {
				return counts, openErr
			}
			_, err = io.Copy(f, contextReader{ctx, entry})
			if err == nil {
				err = f.Sync()
			}
			closeErr := f.Close()
			if err != nil {
				return counts, err
			}
			if closeErr != nil {
				return counts, closeErr
			}
			blobs[id] = path
		} else {
			return counts, errors.New("invalid_archive_entry")
		}
		if progress != nil {
			progress(counts)
		}
	}
	n, err := io.Copy(io.Discard, decrypted)
	if err != nil || n != 0 || !seenManifest || m.Version != 1 || m.Digest != hex.EncodeToString(hash.Sum(nil)) {
		return counts, errors.New("invalid_archive_integrity")
	}
	// Verify every referenced original and reject unreferenced package blobs.
	used := map[string]bool{}
	after := ""
	for {
		batch, err := staged.AttachmentBatch(ctx, after, 100)
		if err != nil {
			return counts, err
		}
		if len(batch) == 0 {
			break
		}
		for _, record := range batch {
			after = record.Message
			for _, file := range record.Files {
				if file.BlobID == "" {
					counts.MissingFiles++
					continue
				}
				path, ok := blobs[file.BlobID]
				if !ok {
					return counts, errors.New("archive_original_missing")
				}
				f, err := os.Open(path)
				if err != nil {
					return counts, err
				}
				info, err := f.Stat()
				if err != nil {
					f.Close()
					return counts, err
				}
				stream, err := s.Store.Vault.OpenSeekStream(f, info.Size(), file.Size, "blob:"+file.BlobID)
				if err != nil {
					f.Close()
					return counts, err
				}
				digest := sha256.New()
				_, err = io.Copy(digest, contextReader{ctx, stream})
				f.Close()
				if err != nil || hex.EncodeToString(digest.Sum(nil)) != file.Digest {
					return counts, errors.New("archive_original_corrupt")
				}
				counts.SavedFiles++
				if !used[file.BlobID] {
					counts.Bytes += file.Size
				}
				used[file.BlobID] = true
			}
		}
	}
	if len(used) != len(blobs) || counts != m.Counts {
		return counts, errors.New("invalid_archive_counts")
	}
	if err = s.publishRestore(ctx, staged, blobs); err != nil {
		return counts, err
	}
	if progress != nil {
		progress(counts)
	}
	return counts, nil
}
func (s *Service) restoreRow(ctx context.Context, dst *store.Store, r archiveRow, c *Counts) error {
	switch r.Kind {
	case "archive":
		if r.Archive == nil || r.Archive.Conversation.ID == "" || r.Archive.Conversation.Group || r.Archive.Conversation.ParticipantID == "" || r.Retention < 0 || r.Retention > 3650 {
			return errors.New("invalid_archive")
		}
		a := *r.Archive
		a.State = "cancelled"
		a.Generation++
		a.Cursor = ""
		a.RecentCursor = ""
		a.RecentStarted = ""
		a.RetryAt = ""
		a.RecentRetryAt = ""
		a.Error = ""
		a.RecentError = ""
		var exists int
		if err := dst.DB.QueryRowContext(ctx, "SELECT count(*) FROM archive_conversations WHERE conversation=?", a.Conversation.ID).Scan(&exists); err != nil {
			return err
		}
		if exists != 0 {
			return errors.New("duplicate_archive")
		}
		return dst.SaveArchive(a, r.Retention)
	case "message":
		if r.ID == "" || (r.Direction != "incoming" && r.Direction != "outgoing") {
			return errors.New("invalid_message")
		}
		if _, err := time.Parse(time.RFC3339Nano, r.At); err != nil {
			return err
		}
		if _, err := dst.Archive(r.Conversation); err != nil {
			return err
		}
		if _, err := dst.Vault.Open("message:"+r.ID, r.Payload); err != nil {
			return err
		}
		tx, err := dst.DB.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if _, err = tx.ExecContext(ctx, "INSERT INTO messages VALUES(?,?,?,?,?)", r.ID, r.Conversation, r.Direction, r.At, r.Payload); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO message_meta VALUES(?,?,?,?)", r.ID, r.Origin, r.Status, r.Private); err != nil {
			return err
		}
		if r.Interaction != "" {
			if _, err = dst.OpenInteraction(r.ID, r.Interaction); err != nil {
				return err
			}
			if _, err = tx.ExecContext(ctx, "INSERT INTO message_interactions VALUES(?,?)", r.ID, r.Interaction); err != nil {
				return err
			}
		}
		if r.Attachments != "" {
			if _, err = dst.OpenAttachmentPayload(r.ID, r.Attachments); err != nil {
				return err
			}
			if _, err = tx.ExecContext(ctx, "INSERT INTO message_attachments VALUES(?,?)", r.ID, r.Attachments); err != nil {
				return err
			}
		}
		if err = tx.Commit(); err == nil {
			c.Messages++
		}
		return err
	case "alias":
		_, err := dst.DB.ExecContext(ctx, "INSERT INTO message_alias VALUES(?,?)", r.Alias, r.Message)
		return err
	case "revision":
		if _, err := dst.Vault.Open("message:"+r.Message, r.Payload); err != nil {
			return err
		}
		_, err := dst.DB.ExecContext(ctx, "INSERT INTO message_revisions VALUES(?,?,?,?)", r.Message, r.Revision, r.Payload, r.Observed)
		return err
	default:
		return errors.New("invalid_record_kind")
	}
}
func (s *Service) publishRestore(ctx context.Context, staged *store.Store, blobs map[string]string) error {
	tx, err := s.Store.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var count int
	if err = tx.QueryRowContext(ctx, "SELECT (SELECT count(*) FROM messages)+(SELECT count(*) FROM archive_conversations)+(SELECT count(*) FROM jobs)+(SELECT count(*) FROM enrollment)+(SELECT count(*) FROM observed_events)+(SELECT count(*) FROM memory)+(SELECT count(*) FROM message_revisions)+(SELECT count(*) FROM message_changes)+(SELECT count(*) FROM archive_pending)").Scan(&count); err != nil {
		return err
	}
	if count != 0 {
		return errors.New("archive_restore_requires_fresh_state")
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Join(s.Root, "blobs"), 0700); err != nil {
		return err
	}
	// All package authentication/integrity checks completed before publication.
	moved := []string{}
	committed := false
	defer func() {
		if !committed {
			for _, path := range moved {
				_ = os.Remove(path)
			}
		}
	}()
	for id, path := range blobs {
		target, _ := s.blobPath(id)
		if _, err = os.Lstat(target); !os.IsNotExist(err) {
			return errors.New("archive_blob_conflict")
		}
		if err = os.Rename(path, target); err != nil {
			return err
		}
		moved = append(moved, target)
	}
	if _, err = tx.ExecContext(ctx, "ATTACH DATABASE ? AS archive_restore", staged.Path); err != nil {
		return err
	}
	// DETACH after commit/rollback releases the staging snapshot on Windows.
	defer func() { _ = tx.Rollback(); _, _ = s.Store.DB.Exec("DETACH DATABASE archive_restore") }()
	for _, table := range []string{"archive_conversations", "messages", "message_meta", "message_alias", "message_attachments", "message_revisions", "message_interactions"} {
		if _, err = tx.ExecContext(ctx, "INSERT INTO "+table+" SELECT * FROM archive_restore."+table); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, "UPDATE settings SET value='true' WHERE key='paused'"); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM settings WHERE key IN ('inbox_settings','archive_discovery')"); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	committed = true
	return nil
}

func exportSuffix(scope Scope) string {
	if scope.Plaintext {
		return ".tar"
	}
	return ".sma"
}
func (s *Service) publicRow(r archiveRow) (any, error) {
	switch r.Kind {
	case "archive":
		return map[string]any{"type": "conversation", "conversation": r.Archive.Conversation}, nil
	case "alias":
		return nil, nil
	case "revision":
		text, err := s.Store.Vault.Open("message:"+r.Message, r.Payload)
		if err != nil {
			return nil, err
		}
		return map[string]any{"type": "revision", "message_id": r.Message, "revision": r.Revision, "observed_at": r.Observed, "text": string(text)}, nil
	case "message":
		text, err := s.Store.Vault.Open("message:"+r.ID, r.Payload)
		if err != nil {
			return nil, err
		}
		attachments := []map[string]any{}
		if r.Attachments != "" {
			files, err := s.Store.OpenAttachmentPayload(r.ID, r.Attachments)
			if err != nil {
				return nil, err
			}
			for _, f := range files {
				path := ""
				if f.BlobID != "" {
					path = "files/" + f.BlobID + "/" + media.SafeName(f.Name)
				}
				attachments = append(attachments, map[string]any{"id": f.ID, "name": f.Name, "mime": f.MIME, "size": f.Size, "archive_state": f.ArchiveState, "file": path})
			}
		}
		var interaction any
		if r.Interaction != "" {
			interaction, err = s.Store.OpenInteraction(r.ID, r.Interaction)
			if err != nil {
				return nil, err
			}
		}
		return map[string]any{"type": "message", "id": r.ID, "conversation_id": r.Conversation, "direction": r.Direction, "at": r.At, "text": string(text), "origin": r.Origin, "status": r.Status, "private": r.Private, "attachments": attachments, "interaction": interaction}, nil
	default:
		return nil, errors.New("invalid_public_record")
	}
}

func (s *Service) cleanOldExports(keep string) error {
	entries, err := os.ReadDir(filepath.Join(s.Root, "exports"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if len(name) < 32 || !validID(name[:32]) || name[:32] == keep {
			continue
		}
		suffix := name[32:]
		if suffix != ".tar" && suffix != ".sma" && suffix != ".tar.part" && suffix != ".sma.part" {
			continue
		}
		if err = os.Remove(filepath.Join(s.Root, "exports", name)); err != nil {
			return err
		}
	}
	return nil
}
func (s *Service) DeleteExport() error {
	s.jobsMu.Lock()
	defer s.jobsMu.Unlock()
	if s.cancel != nil {
		return errors.New("backup_running")
	}
	if err := s.cleanOldExports(""); err != nil {
		return err
	}
	if err := s.Store.SetSecretSetting("archive_backup_job", "archive-backup-job", nil); err != nil {
		return err
	}
	s.job = nil
	return nil
}
