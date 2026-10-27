package archive

// Offline restore into fresh initialized state: rows and blobs are verified
// and staged, then published atomically. Collection and sending stay off.

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/deusical/smsbot-server/internal/store"
)

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
		if r.Archive == nil || r.Archive.Conversation.ID == "" || !r.Archive.Conversation.Collectable() || r.Retention < 0 || r.Retention > 3650 {
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
