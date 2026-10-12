package main

import (
	"os"
	"path/filepath"
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
	s.DB.Close()
	backup := filepath.Join(root, "backup.enc")
	if err = runWithInput([]string{"--data", source, "--key", key, "--backup", backup}, strings.NewReader("")); err != nil {
		t.Fatal(err)
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
	messages, err := s.Messages("fixture-conv")
	paused, _ := s.Paused()
	if err != nil || len(messages) != 1 || !paused {
		t.Fatal("restore incomplete or active")
	}
}
