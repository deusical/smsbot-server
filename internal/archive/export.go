package archive

// Streaming export: a consistent snapshot of the scoped archive written as an
// authenticated encrypted stream or a readable TAR, with original files.

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
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/deusical/smsbot-server/internal/media"
	"github.com/deusical/smsbot-server/internal/store"
)

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
