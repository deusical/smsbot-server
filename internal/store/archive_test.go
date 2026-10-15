package store

import (
	"database/sql"
	"fmt"

	"os"
	"path/filepath"
	"smsbot-server/internal/transport"
	"smsbot-server/internal/vault"
	"testing"
	"time"
)

func TestArchiveEditsChangesAndRetention(t *testing.T) {
	s := inboxStore(t)
	at := time.Now().AddDate(-1, 0, 0)
	for i := 0; i < 125; i++ {
		if err := s.SaveObserved("chat", fmt.Sprint(i), "", "incoming", "original", "history", "", at); err != nil {
			t.Fatal(err)
		}
	}
	a := Archive{State: "queued", Generation: 1}
	a.Conversation.ID = "chat"
	a.Conversation.ParticipantID = "peer"
	a.Conversation.Writable = true
	if err := s.SaveArchive(a, 0); err != nil {
		t.Fatal(err)
	}
	p, err := s.OwnerMessagePage("chat", "", "", 50)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.SaveObserved("chat", "0", "", "incoming", "edited", "history", "", time.Now()); err != nil {
		t.Fatal(err)
	}
	changes, err := s.Changes("chat", fmt.Sprint(p.ChangeCursor), "")
	if err != nil || len(changes.Messages) != 1 || changes.Messages[0].Text != "edited" {
		t.Fatal("edit not published", err)
	}
	if changes.Messages[0].At != at.UTC().Format("2006-01-02T15:04:05.000000000Z") {
		t.Fatal("edit moved original timestamp")
	}
	var n int
	s.DB.QueryRow("SELECT count(*) FROM message_revisions").Scan(&n)
	if n != 1 {
		t.Fatal("previous content not retained")
	}
	if _, err = s.Retention(7); err != nil {
		t.Fatal(err)
	}
	s.DB.QueryRow("SELECT count(*) FROM messages").Scan(&n)
	if n != 125 {
		t.Fatal("legacy retention deleted archive")
	}
	c, err := s.Changes("chat", "0", "")
	if err != nil || !c.More {
		t.Fatal("change feed cannot page bursts", err)
	}
	if err = s.migrate(); err != nil {
		t.Fatal(err)
	}
	if err = s.ArchiveRetention(); err != nil {
		t.Fatal(err)
	}
}

func TestVerifiedArchiveClearsPendingAndRestoreRejectsExistingArchive(t *testing.T) {
	s := inboxStore(t)
	if _, err := s.DB.Exec("INSERT INTO archive_pending VALUES('chat')"); err != nil {
		t.Fatal(err)
	}
	a := Archive{Conversation: transport.Conversation{ID: "chat", ParticipantID: "peer", Writable: true}, State: "cancelled", Generation: 1}
	if err := s.SaveArchive(a, 0); err != nil {
		t.Fatal(err)
	}
	var n int
	s.DB.QueryRow("SELECT count(*) FROM archive_pending").Scan(&n)
	if n != 0 {
		t.Fatal("verified archive left pending exemption")
	}
	empty := inboxStore(t)
	data, err := empty.ExportBackup()
	if err != nil {
		t.Fatal(err)
	}
	if err = s.RestoreBackup(data); err == nil || err.Error() != "restore requires fresh bot state" {
		t.Fatal("archive-only destination passed fresh preflight", err)
	}
}

// This opt-in check snapshots the real database using a read-only connection.
// It never opens the real key or decrypts personal payloads. Migrations run twice
// only on the disposable copy; output contains no contacts or message content.
func TestMigrationOnRealDatabaseCopy(t *testing.T) {
	source := os.Getenv("SMSBOT_MIGRATION_SOURCE")
	if source == "" {
		t.Skip("real database snapshot not requested")
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(source)+"?mode=ro")
	if err != nil {
		t.Fatal("source open failed")
	}
	defer db.Close()
	dest := filepath.Join(t.TempDir(), "snapshot.db")
	if _, err = db.Exec("VACUUM INTO ?", dest); err != nil {
		t.Fatal("read-only snapshot failed", err)
	}
	v, _ := vault.New(make([]byte, 32))
	s, err := Open(dest, v)
	if err != nil {
		t.Fatal("copy migration failed")
	}
	defer s.DB.Close()
	if err = s.migrate(); err != nil {
		t.Fatal("second migration failed")
	}
	var integrity string
	if err = s.DB.QueryRow("PRAGMA integrity_check").Scan(&integrity); err != nil || integrity != "ok" {
		t.Fatal("copy integrity check failed")
	}
}

func BenchmarkArchiveLocalPage(b *testing.B) {
	v, _ := vault.New(make([]byte, 32))
	s, err := Open(filepath.Join(b.TempDir(), "bench.db"), v)
	if err != nil {
		b.Fatal(err)
	}
	defer s.DB.Close()
	tx, err := s.DB.Begin()
	if err != nil {
		b.Fatal(err)
	}
	stmt, err := tx.Prepare("INSERT INTO messages VALUES(?,?,'incoming',?,?)")
	if err != nil {
		b.Fatal(err)
	}
	for i := 0; i < 20000; i++ {
		id := fmt.Sprint(i)
		payload, err := v.Seal("message:"+id, []byte("Synthetic benchmark text"))
		if err != nil {
			b.Fatal(err)
		}
		if _, err = stmt.Exec(id, "bench-chat", time.Unix(int64(i), 0).UTC().Format("2006-01-02T15:04:05.000000000Z"), payload); err != nil {
			b.Fatal(err)
		}
	}
	stmt.Close()
	if err = tx.Commit(); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		p, err := s.OwnerMessagePage("bench-chat", "", "", 50)
		if err != nil || len(p.Messages) != 50 {
			b.Fatal("page failed", err)
		}
	}
}
