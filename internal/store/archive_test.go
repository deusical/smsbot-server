package store

import (
	"database/sql"
	"errors"
	"fmt"

	"github.com/deusical/smsbot-server/internal/transport"
	"github.com/deusical/smsbot-server/internal/vault"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestArchiveEditsChangesAndRetention(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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

func TestRelocateArchive(t *testing.T) {
	t.Parallel()
	s := inboxStore(t)

	// Create original archive and messages for "chat"
	a := Archive{
		Conversation: transport.Conversation{
			ID:            "chat",
			ParticipantID: "peer-1",
			Label:         "First Peer",
			Protocol:      "RCS",
			Writable:      true,
		},
		State:      "available_pages_exhausted",
		Generation: 1,
	}
	if err := s.SaveArchive(a, 30); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveMessage("m1", "chat", "incoming", "hello from peer 1"); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveMessage("m2", "chat", "outgoing", "reply to peer 1"); err != nil {
		t.Fatal(err)
	}

	// Relocate "chat" to "archived-chat-peer-1"
	targetID, err := s.RelocateArchive("chat", "archived-chat-peer-1")
	if err != nil {
		t.Fatal(err)
	}
	if targetID != "archived-chat-peer-1" {
		t.Fatalf("expected targetID %q, got %q", "archived-chat-peer-1", targetID)
	}

	// Old ID "chat" should no longer exist in archives or messages
	if _, err := s.Archive("chat"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expected ErrNoRows for old ID, got %v", err)
	}
	oldMsgs, err := s.Messages("chat")
	if err != nil {
		t.Fatal(err)
	}
	if len(oldMsgs) != 0 {
		t.Fatalf("expected 0 messages in old ID, got %d", len(oldMsgs))
	}

	// Relocated ID should have the archive record and all messages intact
	relocated, err := s.Archive("archived-chat-peer-1")
	if err != nil {
		t.Fatal(err)
	}
	if relocated.Conversation.ID != "archived-chat-peer-1" || relocated.Conversation.ParticipantID != "peer-1" || relocated.Conversation.Label != "First Peer" {
		t.Fatalf("unexpected relocated archive: %+v", relocated.Conversation)
	}
	relocatedMsgs, err := s.Messages("archived-chat-peer-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(relocatedMsgs) != 2 {
		t.Fatalf("expected 2 messages in relocated ID, got %d", len(relocatedMsgs))
	}
	if relocatedMsgs[0].Text != "reply to peer 1" || relocatedMsgs[1].Text != "hello from peer 1" {
		t.Fatalf("message text corrupted after relocation: %+v", relocatedMsgs)
	}

	// Now new conversation with ID "chat" can be created under a different participant
	newA := Archive{
		Conversation: transport.Conversation{
			ID:            "chat",
			ParticipantID: "peer-2",
			Label:         "Second Peer",
			Protocol:      "SMS",
			Writable:      true,
		},
		State:      "running",
		Generation: 1,
	}
	if err := s.SaveArchive(newA, 30); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveMessage("m3", "chat", "incoming", "hello from peer 2"); err != nil {
		t.Fatal(err)
	}

	// Verify "chat" only has peer-2's message, and "archived-chat-peer-1" only has peer-1's messages
	chatMsgs, err := s.Messages("chat")
	if err != nil {
		t.Fatal(err)
	}
	if len(chatMsgs) != 1 || chatMsgs[0].Text != "hello from peer 2" {
		t.Fatalf("unexpected chat messages: %+v", chatMsgs)
	}
	relocatedMsgsAgain, err := s.Messages("archived-chat-peer-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(relocatedMsgsAgain) != 2 {
		t.Fatalf("expected 2 messages in relocated ID, got %d", len(relocatedMsgsAgain))
	}

	// Relocating again to the same target should auto-disambiguate with suffix
	targetID2, err := s.RelocateArchive("chat", "archived-chat-peer-1")
	if err != nil {
		t.Fatal(err)
	}
	if targetID2 != "archived-chat-peer-1-1" {
		t.Fatalf("expected auto-disambiguated targetID %q, got %q", "archived-chat-peer-1-1", targetID2)
	}
	if a2, err := s.Archive("archived-chat-peer-1-1"); err != nil || a2.Conversation.ParticipantID != "peer-2" {
		t.Fatalf("failed to retrieve auto-disambiguated archive: %v, %+v", err, a2)
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
