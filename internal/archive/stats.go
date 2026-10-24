package archive

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Stats describes physical file sizes, not a promise of history completeness.
// DatabaseBytes includes the shared service database and its WAL/SHM files.
type Stats struct {
	OriginalBytes  int64  `json:"original_bytes"`
	OriginalFiles  int64  `json:"original_files"`
	DatabaseBytes  int64  `json:"database_bytes"`
	ExportBytes    int64  `json:"export_bytes"`
	TemporaryBytes int64  `json:"temporary_bytes"`
	TotalBytes     int64  `json:"total_bytes"`
	Messages       int64  `json:"messages"`
	QuotaBytes     int64  `json:"quota_bytes"`
	MeasuredAt     string `json:"measured_at"`
}

func (s *Service) Stats(ctx context.Context) (Stats, error) {
	var result Stats
	policy, err := s.policy(ctx)
	if err != nil {
		return result, err
	}
	result.QuotaBytes = policy.QuotaMiB << 20
	if result.Messages, err = s.Store.ArchivedMessageCount(ctx); err != nil {
		return result, err
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		info, err := os.Stat(s.Store.Path + suffix)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return result, err
		}
		result.DatabaseBytes += info.Size()
	}
	// Do not take the publication/export mutex: a large export must not block
	// this owner read. Files removed concurrently are counted on the next poll.
	err = filepath.WalkDir(s.Root, func(path string, entry fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if os.IsNotExist(walkErr) {
			return nil
		}
		if walkErr != nil {
			return walkErr
		}
		if (path == s.Root || path == filepath.Join(s.Root, "blobs") || path == filepath.Join(s.Root, "exports")) && !entry.IsDir() {
			return errors.New("archive_storage_not_directory")
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		info, err := entry.Info()
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if filepath.Dir(path) == filepath.Join(s.Root, "blobs") && strings.HasSuffix(entry.Name(), ".enc") && validID(strings.TrimSuffix(entry.Name(), ".enc")) {
			result.OriginalBytes += info.Size()
			result.OriginalFiles++
		} else if filepath.Dir(path) == filepath.Join(s.Root, "exports") && (strings.HasSuffix(entry.Name(), ".sma") || strings.HasSuffix(entry.Name(), ".tar")) {
			result.ExportBytes += info.Size()
		} else {
			result.TemporaryBytes += info.Size()
		}
		return nil
	})
	if err != nil {
		return result, err
	}
	result.TotalBytes = result.OriginalBytes + result.DatabaseBytes + result.ExportBytes + result.TemporaryBytes
	result.MeasuredAt = time.Now().UTC().Format(time.RFC3339Nano)
	return result, ctx.Err()
}
