package store

import (
	"database/sql"
	"path/filepath"
	"sync"
	"testing"

	_ "modernc.org/sqlite"
)

func TestModerncPragmaDSN(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "pragma_test.db")

	dsn := "file:" + filepath.ToSlash(dbPath) + "?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=synchronous(FULL)&_pragma=journal_mode(WAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open error: %v", err)
	}
	defer db.Close()

	var fk int
	if err := db.QueryRow("PRAGMA foreign_keys").Scan(&fk); err != nil {
		t.Fatalf("query foreign_keys: %v", err)
	}
	t.Logf("foreign_keys = %d", fk)

	var syncMode int
	if err := db.QueryRow("PRAGMA synchronous").Scan(&syncMode); err != nil {
		t.Fatalf("query synchronous: %v", err)
	}
	t.Logf("synchronous = %d (FULL is 2)", syncMode)

	var journalMode string
	if err := db.QueryRow("PRAGMA journal_mode").Scan(&journalMode); err != nil {
		t.Fatalf("query journal_mode: %v", err)
	}
	t.Logf("journal_mode = %s", journalMode)

	var busyTimeout int
	if err := db.QueryRow("PRAGMA busy_timeout").Scan(&busyTimeout); err != nil {
		t.Fatalf("query busy_timeout: %v", err)
	}
	t.Logf("busy_timeout = %d", busyTimeout)

	if fk != 1 {
		t.Fatalf("expected foreign_keys=1, got %d", fk)
	}
	if syncMode != 2 {
		t.Fatalf("expected synchronous=2 (FULL), got %d", syncMode)
	}
	if journalMode != "wal" {
		t.Fatalf("expected journal_mode=wal, got %s", journalMode)
	}
	if busyTimeout != 5000 {
		t.Fatalf("expected busy_timeout=5000, got %d", busyTimeout)
	}
}

func TestForeignKeysEnforcedOnEveryConnection(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "fk_pool_test.db")
	dsn := "file:" + filepath.ToSlash(dbPath) + "?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=synchronous(FULL)&_pragma=journal_mode(WAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(4)

	if _, err := db.Exec("CREATE TABLE parent(id TEXT PRIMARY KEY)"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("CREATE TABLE child(id TEXT PRIMARY KEY, parent_id TEXT REFERENCES parent(id))"); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			_, err := db.Exec("INSERT INTO child VALUES(?, 'missing-parent')", filepath.Join(dir, string(rune('a'+n))))
			if err == nil {
				t.Errorf("expected foreign key violation on connection, but insert succeeded")
			}
		}(i)
	}
	wg.Wait()
}
