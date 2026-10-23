package store

import (
	"database/sql"
	"github.com/deusical/smsbot-server/internal/vault"
	"os"
	"testing"
	"time"
)

func TestBackupPreservesPrivateMessageVisibility(t *testing.T) {
	s := inboxStore(t)
	s.SaveObserved("synthetic-chat", "synthetic-message", "", "incoming", "Synthetic private text", "phone", "", time.Now())
	b, err := s.ExportBackup()
	if err != nil {
		t.Fatal(err)
	}
	target := inboxStore(t)
	if err = target.RestoreBackup(b); err != nil {
		t.Fatal(err)
	}
	p, _ := target.MessagePage("synthetic-chat", "", "", 50)
	if len(p.Messages) != 0 {
		t.Fatal("restored private message became visible to client")
	}
	p, _ = target.OwnerMessagePage("synthetic-chat", "", "", 50)
	if len(p.Messages) != 1 {
		t.Fatal("private message lost")
	}
}
func TestMigrationCopyIsIdempotent(t *testing.T) {
	path := os.Getenv("SMSBOT_MIGRATION_COPY")
	if path == "" {
		t.Skip("explicit disposable database copy required")
	}
	// No decrypted payload or real identity is read or printed. The supplied copy
	// can use a synthetic vault because migrations operate on ciphertext only.
	v, _ := vault.New(make([]byte, 32))
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	var counts [4]int
	for i, table := range []string{"messages", "jobs", "enrollment", "credentials"} {
		if err = db.QueryRow("SELECT count(*) FROM " + table).Scan(&counts[i]); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()
	s, err := Open(path, v)
	if err != nil {
		t.Fatal(err)
	}
	s.DB.Close()
	s, err = Open(path, v)
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	for i, table := range []string{"messages", "jobs", "enrollment", "credentials"} {
		var n int
		if err = s.DB.QueryRow("SELECT count(*) FROM " + table).Scan(&n); err != nil || n != counts[i] {
			t.Fatal("migration changed existing row count")
		}
	}
	var version int
	s.DB.QueryRow("PRAGMA user_version").Scan(&version)
	if version != 3 {
		t.Fatal("migration did not reach schema 3")
	}
}

func TestReimportRepairsExactTransactionDuplicateAndPreservesIdenticalSends(t *testing.T) {
	s := inboxStore(t)
	conv := "synthetic-chat"
	at := time.Now()
	// Pre-feature send and legacy import used different IDs.
	s.Enqueue("synthetic-job-one", conv, "manual", Plan{})
	s.Submitting("synthetic-job-one", "synthetic-transaction-one")
	s.SaveMessageAt("synthetic-transaction-one", conv, "outgoing", "Synthetic repeated reply", at)
	alias := Hash(conv + "\x00synthetic-upstream-one")
	s.SaveMessageAt(alias, conv, "outgoing", "Synthetic repeated reply", at.Add(time.Second))
	// A second genuine send with identical text must never be collapsed.
	s.Enqueue("synthetic-job-two", conv, "manual", Plan{})
	s.Submitting("synthetic-job-two", "synthetic-transaction-two")
	s.SaveMessageAt("synthetic-transaction-two", conv, "outgoing", "Synthetic repeated reply", at.Add(2*time.Second))
	for i := 0; i < 2; i++ {
		if err := s.SaveLegacyImported(conv, "synthetic-upstream-one", "synthetic-transaction-one", "outgoing", "Synthetic repeated reply", "read", at.Add(time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	// Later history may omit the tmpID: the persisted alias still resolves it.
	if err := s.SaveLegacyImported(conv, "synthetic-upstream-one", "", "outgoing", "Synthetic repeated reply", "submitted", at); err != nil {
		t.Fatal(err)
	}
	p, err := s.OwnerMessagePage(conv, "", "", 50)
	if err != nil || len(p.Messages) != 2 {
		t.Fatal("duplicate repaired incorrectly", err, len(p.Messages))
	}
	var canonical Message
	for _, m := range p.Messages {
		if m.ID == "synthetic-transaction-one" {
			canonical = m
		}
	}
	if canonical.ID != "synthetic-transaction-one" || canonical.Status != "read" {
		t.Fatal("canonical row/status lost")
	}
	if err = s.SaveObserved(conv, "synthetic-private", "", "incoming", "Synthetic private capture", "phone", "", at); err != nil {
		t.Fatal(err)
	}
	if err = s.SaveLegacyImported(conv, "synthetic-private", "", "incoming", "Synthetic private capture", "", at); err != nil {
		t.Fatal(err)
	}
	public, err := s.MessagePage(conv, "", "", 50)
	if err != nil || len(public.Messages) != 2 {
		t.Fatal("legacy import published private capture")
	}
}
