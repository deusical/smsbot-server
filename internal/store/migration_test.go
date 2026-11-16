package store

import (
	"github.com/deusical/smsbot-server/internal/vault"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func TestVersionOneMigrationPreservesEncryptedDataAndOrdersTimestamps(t *testing.T) {
	t.Parallel()
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

func TestJobWorkCancelSentMigrationAndQuery(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "state.db")
	v, _ := vault.New(make([]byte, 32))
	s, err := Open(path, v)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a pre-column database whose job_work table has only 3 columns.
	if _, err := s.DB.Exec(`DROP TABLE job_work; CREATE TABLE job_work(job TEXT PRIMARY KEY REFERENCES jobs(id) ON DELETE CASCADE,stage TEXT NOT NULL,next_poll INTEGER NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Enqueue("migrated", "fixture-conv", "!download", Plan{Participant: "fixture-peer"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(`INSERT INTO job_work VALUES('migrated','downloading',0)`); err != nil {
		t.Fatal(err)
	}
	if err := s.Cancel("migrated"); err != nil {
		t.Fatal(err)
	}
	s.DB.Close()

	s, err = Open(path, v)
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()

	var version int
	if err := s.DB.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 10 {
		t.Fatalf("unexpected user_version=%d err=%v", version, err)
	}

	// Active jobs and bot-side stages must be ignored; live worker stages on ended jobs returned.
	addJob := func(id, state, stage string, created time.Time) {
		if _, err := s.Enqueue(id, "fixture-conv", "!download", Plan{Participant: "fixture-peer"}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.DB.Exec("UPDATE jobs SET created=? WHERE id=?", created.UTC().Format(time.RFC3339Nano), id); err != nil {
			t.Fatal(err)
		}
		if err := s.DeferWork(id, stage, 0); err != nil {
			t.Fatal(err)
		}
		if state != "queued" {
			if _, err := s.DB.Exec("UPDATE jobs SET state=?,plan='' WHERE id=?", state, id); err != nil {
				t.Fatal(err)
			}
		}
	}

	now := time.Now()
	addJob("active-queued", "queued", "downloading", now)
	addJob("active-gen", "generating", "compressing", now)
	addJob("active-approval", "awaiting_approval", "queued", now)
	addJob("active-submit", "submitting", "downloading", now)
	for _, stage := range []string{"complete", "receiving", "waiting_upload", "uploading", "ready"} {
		addJob("bot-"+stage, "cancelled", stage, now)
	}
	addJob("ended-compressing", "expired", "compressing", now)
	addJob("ended-failed-check", "failed", "verifying", now)
	addJob("ended-stale", "cancelled", "downloading", now.Add(-MaxWorkerWindow-time.Minute))

	got, err := s.PendingWorkerCancels(now)
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(got)
	want := []string{"ended-compressing", "ended-failed-check", "migrated"}
	if !slices.Equal(got, want) {
		t.Fatalf("PendingWorkerCancels=%v, want %v", got, want)
	}

	// Stale job was marked done without altering its stage; marking delivered omits it next time.
	var staleSent int
	if err := s.DB.QueryRow("SELECT cancel_sent FROM job_work WHERE job='ended-stale'").Scan(&staleSent); err != nil || staleSent != 1 {
		t.Fatalf("stale cancel_sent=%d err=%v", staleSent, err)
	}
	for _, id := range want {
		if err := s.MarkWorkerCancelSent(id); err != nil {
			t.Fatal(err)
		}
	}
	again, err := s.PendingWorkerCancels(now)
	if err != nil || len(again) != 0 {
		t.Fatalf("after mark: %v err=%v", again, err)
	}
	jobs, err := s.Jobs()
	if err != nil {
		t.Fatal(err)
	}
	for _, j := range jobs {
		if j.ID == "migrated" && j.PreparationStage != "downloading" {
			t.Fatalf("PreparationStage overwritten: %q", j.PreparationStage)
		}
	}
}
