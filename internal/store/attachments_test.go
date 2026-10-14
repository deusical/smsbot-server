package store

import (
	"bytes"
	"encoding/json"
	"smsbot-server/internal/media"
	"strings"
	"testing"
	"time"
)

func TestAttachmentEncryptionBackupRestoreAndRetention(t *testing.T) {
	s := inboxStore(t)
	files := []media.File{{Info: media.Info{ID: "synthetic-file", Name: "private-name.txt", MIME: "text/plain", Size: 17}, Data: []byte("Synthetic payload"), RemoteID: "private-remote", Key: []byte("private-key")}}
	if err := s.SaveObservedFiles("synthetic-chat", "synthetic-upstream", "", "incoming", "", "phone", "", time.Now(), files); err != nil {
		t.Fatal(err)
	}
	message := Hash("synthetic-chat\x00synthetic-upstream")
	var ciphertext string
	if err := s.DB.QueryRow("SELECT payload FROM message_attachments WHERE message=?", message).Scan(&ciphertext); err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"private-name", "Synthetic payload", "private-remote", "private-key"} {
		if strings.Contains(ciphertext, secret) {
			t.Fatal("unencrypted attachment")
		}
	}
	page, err := s.OwnerMessagePage("synthetic-chat", "", "", 50)
	if err != nil || len(page.Messages) != 1 || len(page.Messages[0].Attachments) != 1 {
		t.Fatal("attachment metadata missing", err)
	}
	encoded, _ := json.Marshal(page)
	if bytes.Contains(encoded, []byte("private-remote")) || bytes.Contains(encoded, []byte("private-key")) || bytes.Contains(encoded, []byte(`"data"`)) {
		t.Fatal("secret descriptor leaked in message list")
	}
	backup, err := s.ExportBackup()
	if err != nil {
		t.Fatal(err)
	}
	target := inboxStore(t)
	if err = target.RestoreBackup(backup); err != nil {
		t.Fatal(err)
	}
	file, err := target.Attachment("synthetic-chat", message, "synthetic-file")
	if err != nil || string(file.Data) != "Synthetic payload" || file.RemoteID != "private-remote" {
		t.Fatal("attachment lost on restore", err)
	}
	if _, err = target.DB.Exec("UPDATE messages SET at='2020-01-01T00:00:00Z'"); err != nil {
		t.Fatal(err)
	}
	if _, err = target.Retention(7); err != nil {
		t.Fatal(err)
	}
	if _, err = target.Attachment("synthetic-chat", message, "synthetic-file"); err == nil {
		t.Fatal("retention left attachment")
	}
	var count int
	target.DB.QueryRow("SELECT count(*) FROM message_attachments").Scan(&count)
	if count != 0 {
		t.Fatal("attachment ciphertext orphaned after retention")
	}
}

func TestAttachmentMigrationFromVersionThree(t *testing.T) {
	s := inboxStore(t)
	if err := s.SaveMessage("synthetic-old", "synthetic-chat", "incoming", "Synthetic existing text"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec("DROP TABLE message_attachments; PRAGMA user_version=3"); err != nil {
		t.Fatal(err)
	}
	if err := s.migrate(); err != nil {
		t.Fatal(err)
	}
	var version int
	s.DB.QueryRow("PRAGMA user_version").Scan(&version)
	p, err := s.MessagePage("synthetic-chat", "", "", 50)
	if version != 4 || err != nil || len(p.Messages) != 1 || p.Messages[0].Text != "Synthetic existing text" {
		t.Fatal("migration lost existing message", err)
	}
}
