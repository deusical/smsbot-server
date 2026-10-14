package main

import (
	"os"
	"path/filepath"
	"smsbot-server/internal/media"
	"smsbot-server/internal/store"
	"smsbot-server/internal/vault"
	"strings"
	"testing"
)

func TestOfflineCLIBackupRestoreWithOriginalKeyAndNewOwner(t *testing.T) {
	root := t.TempDir()
	key := filepath.Join(root, "master.key")
	source := filepath.Join(root, "source")
	owner := filepath.Join(root, "owner.txt")
	if err := runWithInput([]string{"--data", source, "--key", key, "--init", "--owner-file", owner}, strings.NewReader("")); err != nil {
		t.Fatal(err)
	}
	v, err := vault.Open(key)
	if err != nil {
		t.Fatal(err)
	}
	s, err := store.Open(filepath.Join(source, "smsbot.db"), v)
	if err != nil {
		t.Fatal(err)
	}
	s.Enroll("fixture-conv", "fixture-peer")
	s.SaveMessage("fixture-message", "fixture-conv", "incoming", "Synthetic recoverable text")
	files, err := media.Prepare([]media.Upload{
		{Name: "synthetic-a.bin", Data: make([]byte, 7<<20)},
		{Name: "synthetic-b.bin", Data: make([]byte, 7<<20)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.SaveSentFiles("fixture-files", "fixture-conv", "", files); err != nil {
		t.Fatal(err)
	}
	s.DB.Close()
	backup := filepath.Join(root, "backup.enc")
	if err = runWithInput([]string{"--data", source, "--key", key, "--backup", backup}, strings.NewReader("")); err != nil {
		t.Fatal(err)
	}
	if info, statErr := os.Stat(backup); statErr != nil || info.Size() <= 32<<20 {
		t.Fatal("fixture backup must exceed the old 32 MiB bound")
	}
	if err = runWithInput([]string{"--data", source, "--key", key, "--backup", backup}, strings.NewReader("")); err == nil {
		t.Fatal("backup overwritten")
	}
	target := filepath.Join(root, "restored")
	newOwner := filepath.Join(root, "restored-owner.txt")
	if err = runWithInput([]string{"--data", target, "--key", key, "--init", "--owner-file", newOwner}, strings.NewReader("")); err != nil {
		t.Fatal(err)
	}
	if err = runWithInput([]string{"--data", target, "--key", key, "--restore", backup}, strings.NewReader("")); err != nil {
		t.Fatal(err)
	}
	s, err = store.Open(filepath.Join(target, "smsbot.db"), v)
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	token, _ := os.ReadFile(newOwner)
	if !s.Credential(string(token), "owner") {
		t.Fatal("restored destination owner lost")
	}
	restoredFile, fileErr := s.Attachment("fixture-conv", "fixture-files", files[0].ID)
	if fileErr != nil || len(restoredFile.Data) != 7<<20 {
		t.Fatal("large attachment backup did not restore", fileErr)
	}
	messages, err := s.Messages("fixture-conv")
	paused, _ := s.Paused()
	if err != nil || len(messages) != 1 || !paused {
		t.Fatal("restore incomplete or active")
	}
}
