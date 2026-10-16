package archive

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestStatsIncludesDatabaseOriginalsExportsAndWorkingFiles(t *testing.T) {
	s, manager := setup(t)
	seed(t, s, nil)
	empty, err := manager.Stats(context.Background())
	if err != nil || empty.Messages != 1 || empty.DatabaseBytes <= 0 || empty.OriginalFiles != 0 {
		t.Fatal("initial stats", err)
	}
	for name, data := range map[string]string{"blobs/0123456789abcdef0123456789abcdef.enc": "original", "blobs/downloading.part": "partial", "exports/0123456789abcdef0123456789abcdef.tar": "export", "exports/snapshot-test/database.db": "snapshot", "restore-test/staged.db": "restoring"} {
		path := filepath.Join(manager.Root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	stats, err := manager.Stats(context.Background())
	if err != nil || stats.OriginalFiles != 1 || stats.OriginalBytes != 8 || stats.ExportBytes != 6 || stats.TemporaryBytes != 24 || stats.TotalBytes != stats.DatabaseBytes+38 || stats.QuotaBytes != 10*1024*1024*1024 {
		t.Fatal("disk accounting", stats, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = manager.Stats(ctx); err == nil {
		t.Fatal("cancelled stats succeeded")
	}
	if err = os.RemoveAll(filepath.Join(manager.Root, "blobs")); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(manager.Root, "blobs"), []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = manager.Stats(context.Background()); err == nil {
		t.Fatal("unreadable directory reported as empty")
	}
}
