package store

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/deusical/smsbot-server/internal/media"
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
	if version != 7 || err != nil || len(p.Messages) != 1 || p.Messages[0].Text != "Synthetic existing text" {
		t.Fatal("migration lost existing message", err)
	}
}

func TestReimportReplacesLegacyInlinePreview(t *testing.T) {
	s := inboxStore(t)
	old := media.File{Info: media.Info{ID: "part-0", MIME: "image/jpeg", Size: 8}, Data: []byte("preview")}
	if err := s.SaveObservedFiles("synthetic-chat", "synthetic-message", "", "outgoing", "", "phone", "", time.Now(), []media.File{old}); err != nil {
		t.Fatal(err)
	}
	corrected := media.File{Info: media.Info{ID: "part-0", MIME: "image/jpeg", Size: 100000}, RemoteID: "synthetic-original", Key: make([]byte, 32), MessageID: "synthetic-message", ActionID: "synthetic-action"}
	if err := s.SaveObservedFiles("synthetic-chat", "synthetic-message", "", "outgoing", "", "phone", "", time.Now(), []media.File{corrected}); err != nil {
		t.Fatal(err)
	}
	f, err := s.Attachment("synthetic-chat", Hash("synthetic-chat\x00synthetic-message"), "part-0")
	if err != nil || len(f.Data) != 0 || f.RemoteID != "synthetic-original" {
		t.Fatal("legacy preview blocked repaired import", err)
	}
}

func TestMeasuredOriginalSizeSurvivesUpstreamRefresh(t *testing.T) {
	s := inboxStore(t)
	file := media.File{Info: media.Info{ID: "part-0", MIME: "image/jpeg", Size: 32000}, RemoteID: "synthetic-original", MessageID: "synthetic-message", ActionID: "synthetic-action"}
	save := func(f media.File) {
		t.Helper()
		if err := s.SaveObservedFiles("chat", "message", "", "incoming", "", "phone", "", time.Now(), []media.File{f}); err != nil {
			t.Fatal(err)
		}
	}
	save(file)
	file.Size = 25000
	file.SizeUnverified = true
	save(file)
	measured, err := s.Attachment("chat", Hash("chat\x00message"), "part-0")
	if err != nil || measured.Size != 32000 || measured.SizeUnverified {
		t.Fatal("upstream size replaced measured original", err)
	}
}

func TestForegroundSizeMeasurementPreservesConcurrentArchivedOriginal(t *testing.T) {
	s := inboxStore(t)
	source := media.File{Info: media.Info{ID: "part-0", MIME: "image/jpeg", Size: 25000}, RemoteID: "synthetic-original", MessageID: "synthetic-message", SizeUnverified: true}
	if err := s.SaveObservedFiles("chat", "message", "", "incoming", "", "phone", "", time.Now(), []media.File{source}); err != nil {
		t.Fatal(err)
	}
	id := Hash("chat\x00message")
	archived := source
	archived.BlobID = "synthetic-saved-blob"
	archived.ArchiveState = "saved"
	archived.Size = 32000
	archived.SizeUnverified = false
	if err := s.UpdateArchivedFile(context.Background(), id, source, archived); err != nil {
		t.Fatal(err)
	}
	measured := source
	measured.Size = 32000
	measured.SizeUnverified = false
	if err := s.UpdateArchivedFile(context.Background(), id, source, measured); err != nil {
		t.Fatal(err)
	}
	saved, err := s.Attachment("chat", id, "part-0")
	if err != nil || saved.BlobID != archived.BlobID || saved.ArchiveState != "saved" || saved.Size != 32000 {
		t.Fatal("foreground measurement discarded archived original", err)
	}
}
