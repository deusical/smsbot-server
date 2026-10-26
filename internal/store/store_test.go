package store

import (
	"bytes"
	"github.com/deusical/smsbot-server/internal/vault"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPersistenceAndEncryption(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	v, _ := vault.New(make([]byte, 32))
	s, e := Open(path, v)
	if e != nil {
		t.Fatal(e)
	}
	s.Enroll("fixture-chat", "fixture-peer")
	s.SaveMessage("fixture-msg", "fixture-chat", "incoming", "unique synthetic message body")
	s.AddCredential("unique synthetic credential", "Owner", "owner", 0)
	s.DB.Close()
	for _, file := range []string{path, path + "-wal"} {
		b, _ := os.ReadFile(file)
		if bytes.Contains(b, []byte("unique synthetic message body")) || bytes.Contains(b, []byte("unique synthetic credential")) {
			t.Fatal("plaintext in database")
		}
	}
	s, e = Open(path, v)
	if e != nil {
		t.Fatal(e)
	}
	defer s.DB.Close()
	if !s.Enrolled("fixture-chat", "fixture-peer") || !s.Credential("unique synthetic credential", "owner") {
		t.Fatal("state lost")
	}
	m, e := s.Messages("fixture-chat")
	if e != nil || len(m) != 1 || m[0].Text != "unique synthetic message body" {
		t.Fatal("cannot recover encrypted message")
	}
}
func TestFutureSchemaRejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	v, _ := vault.New(make([]byte, 32))
	s, _ := Open(path, v)
	s.DB.Exec("PRAGMA user_version=999")
	s.DB.Close()
	if _, e := Open(path, v); e == nil {
		t.Fatal("future schema accepted")
	}
}
func TestCredentialRevocationAndExpiry(t *testing.T) {
	v, _ := vault.New(make([]byte, 32))
	s, e := Open(filepath.Join(t.TempDir(), "test.db"), v)
	if e != nil {
		t.Fatal(e)
	}
	defer s.DB.Close()
	s.AddCredential("fixture-session", "test", "session", 0)
	s.AddCredential("fixture-expired", "test", "session", 1)
	if s.Credential("fixture-expired", "session") || s.Credential("fixture-session", "owner") {
		t.Fatal("credential scope/expiry")
	}
}

func TestRequestLimitsCountBotRepliesOnly(t *testing.T) {
	v, _ := vault.New(make([]byte, 32))
	s, err := Open(filepath.Join(t.TempDir(), "test.db"), v)
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	now := time.Now().UTC()
	for _, command := range []string{"manual", "manual", "manual", "reaction"} {
		if _, err := s.Enqueue(ID(), "fixture-chat", command, Plan{Participant: "fixture-peer", Reply: "synthetic", Manual: true}); err != nil {
			t.Fatal(err)
		}
	}
	if ok, err := s.Limits(now, 1, 1, 0, 0); err != nil || !ok {
		t.Fatalf("messages sent by hand used up the bot's limits: %v %v", ok, err)
	}
	if _, err := s.Enqueue(ID(), "fixture-chat", "!ask", Plan{Participant: "fixture-peer", Reply: "synthetic"}); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.Limits(now, 1, 5, 0, 0); err != nil || ok {
		t.Fatalf("a bot reply did not count: %v %v", ok, err)
	}
}
