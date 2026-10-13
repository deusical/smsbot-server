package store

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestBackupRestoreEncryptedExcludedCredentialsAndNoReplay(t *testing.T) {
	source := inboxStore(t)
	if e := source.Enroll("fixture-chat", "fixture-peer"); e != nil {
		t.Fatal(e)
	}
	if e := source.AddCredential("synthetic-source-token", "fixture", "owner", 0); e != nil {
		t.Fatal(e)
	}
	if _, e := source.DB.Exec("INSERT INTO settings VALUES('provider_key','synthetic-key')"); e != nil {
		t.Fatal(e)
	}
	if e := source.SetSecretSetting("provider_compatible", "provider-compatible", []byte(`{"base_url":"https://gateway.example.test/v1","key":"synthetic-compatible-key"}`)); e != nil {
		t.Fatal(e)
	}
	if e := source.SaveMessage("fixture-msg", "fixture-chat", "incoming", "synthetic private body"); e != nil {
		t.Fatal(e)
	}
	for _, id := range []string{"fixture-queued", "fixture-submitting"} {
		if _, e := source.Enqueue(id, "fixture-chat", "ping", Plan{Reply: "synthetic", Participant: "fixture-peer"}); e != nil {
			t.Fatal(e)
		}
	}
	if e := source.Submitting("fixture-submitting", "fixture-txn"); e != nil {
		t.Fatal(e)
	}
	if _, e := source.DB.Exec("INSERT INTO usage VALUES('fixture-submitting','fixture-model',10,20,3,NULL)"); e != nil {
		t.Fatal(e)
	}
	data, e := source.ExportBackup()
	if e != nil {
		t.Fatal(e)
	}
	if bytes.Contains(data, []byte("synthetic private body")) {
		t.Fatal("plaintext backup")
	}
	plain, e := source.Vault.Open("backup", string(data))
	if e != nil {
		t.Fatal(e)
	}
	if bytes.Contains(plain, []byte("synthetic-source-token")) || bytes.Contains(plain, []byte("synthetic-key")) || bytes.Contains(plain, []byte("synthetic-compatible-key")) || bytes.Contains(plain, []byte("gateway.example.test")) || bytes.Contains(plain, []byte("credentials")) {
		t.Fatal("backup includes secrets")
	}
	target := inboxStore(t)
	if e = target.AddCredential("synthetic-target-token", "fixture", "owner", 0); e != nil {
		t.Fatal(e)
	}
	if e = target.RestoreBackup(data); e != nil {
		t.Fatal(e)
	}
	var prompt, completion int
	if e = target.DB.QueryRow("SELECT prompt_tokens,completion_tokens FROM usage WHERE job='fixture-submitting'").Scan(&prompt, &completion); e != nil || prompt != 10 || completion != 20 {
		t.Fatal("usage ledger lost", e)
	}
	paused, e := target.Paused()
	if e != nil || !paused {
		t.Fatal("restore not paused")
	}
	if !target.Enrolled("fixture-chat", "fixture-peer") || !target.Credential("synthetic-target-token", "owner") || target.Credential("synthetic-source-token", "owner") {
		t.Fatal("credentials or enrollment incorrect")
	}
	jobs, e := target.Jobs()
	if e != nil || len(jobs) != 2 {
		t.Fatal("jobs lost")
	}
	for _, j := range jobs {
		if j.ID == "fixture-submitting" && j.State != "unknown_submission" {
			t.Fatal("uncertain job replayed")
		}
		if j.ID == "fixture-queued" && j.State != "cancelled" {
			t.Fatal("queued job replayed")
		}
	}
	messages, e := target.Messages("fixture-chat")
	if e != nil || len(messages) != 1 || messages[0].Text != "synthetic private body" {
		t.Fatal("messages not restored")
	}
	if e = target.RestoreBackup(data); e == nil {
		t.Fatal("overwrote existing state")
	}
}
func TestBackupRejectsFutureSchemaTamperingAndRollsBack(t *testing.T) {
	source := inboxStore(t)
	data, e := source.ExportBackup()
	if e != nil {
		t.Fatal(e)
	}
	target := inboxStore(t)
	tampered := append([]byte(nil), data...)
	tampered[len(tampered)/2] ^= 1
	if e = target.RestoreBackup(tampered); e == nil {
		t.Fatal("tampered backup accepted")
	}
	plain, e := source.Vault.Open("backup", string(data))
	if e != nil {
		t.Fatal(e)
	}
	var b backupSnapshot
	if e = json.Unmarshal(plain, &b); e != nil {
		t.Fatal(e)
	}
	b.Schema = 999
	plain, _ = json.Marshal(b)
	sealed, _ := source.Vault.Seal("backup", plain)
	if e = target.RestoreBackup([]byte(sealed)); e == nil {
		t.Fatal("future schema accepted")
	}
	b.Schema = 1
	b.Enrollment = []backupEnrollment{{"fixture-chat", "fixture-peer"}}
	b.Messages = []backupMessage{{ID: "bad", Conversation: "fixture-chat", Direction: "incoming", At: "2026-01-01T00:00:00Z", Payload: "bad"}}
	plain, _ = json.Marshal(b)
	sealed, _ = source.Vault.Seal("backup", plain)
	if e = target.RestoreBackup([]byte(sealed)); e == nil {
		t.Fatal("invalid payload accepted")
	}
	if target.Enrolled("fixture-chat", "fixture-peer") {
		t.Fatal("partial restore")
	}
}
