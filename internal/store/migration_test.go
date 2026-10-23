package store

import (
	"github.com/deusical/smsbot-server/internal/vault"
	"path/filepath"
	"testing"
)

func TestVersionOneMigrationPreservesEncryptedDataAndOrdersTimestamps(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	v, _ := vault.New(make([]byte, 32))
	s, err := Open(path, v)
	if err != nil {
		t.Fatal(err)
	}
	s.Enroll("fixture-conv", "fixture-peer")
	s.SaveMessage("fixture-a", "fixture-conv", "incoming", "Synthetic earlier")
	s.SaveMessage("fixture-b", "fixture-conv", "incoming", "Synthetic later")
	s.DB.Exec("UPDATE messages SET at='2026-01-01T00:00:00Z' WHERE id='fixture-a'")
	s.DB.Exec("UPDATE messages SET at='2026-01-01T00:00:00.1Z' WHERE id='fixture-b'")
	s.DB.Exec("DROP TABLE usage; DROP TABLE memory; PRAGMA user_version=1")
	s.DB.Close()
	s, err = Open(path, v)
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	page, err := s.MessagePage("fixture-conv", "", "", 1)
	if err != nil || len(page.Messages) != 1 || page.Messages[0].ID != "fixture-b" || page.NextCursor == "" {
		t.Fatal("migration ordering failed")
	}
	next, err := s.MessagePage("fixture-conv", page.NextCursor, "", 1)
	if err != nil || len(next.Messages) != 1 || next.Messages[0].Text != "Synthetic earlier" || !s.Enrolled("fixture-conv", "fixture-peer") {
		t.Fatal("migration lost data")
	}
}
