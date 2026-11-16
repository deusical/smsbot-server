package store

import (
	"bytes"
	"os"
	"testing"

	"github.com/deusical/smsbot-server/internal/media"
)

// walGrowth returns how many bytes fn appends to the write-ahead log, which is
// what SQLite writes to disk for it. Automatic checkpoints are off so the log
// keeps every frame.
func walGrowth(t *testing.T, s *Store, fn func()) int64 {
	t.Helper()
	if _, err := s.DB.Exec("PRAGMA wal_autocheckpoint=0; PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		t.Fatal(err)
	}
	size := func() int64 {
		info, err := os.Stat(s.Path + "-wal")
		if err != nil {
			t.Fatal(err)
		}
		return info.Size()
	}
	before := size()
	fn()
	return size() - before
}

// syntheticFile is an attachment of n bytes that starts with marker.
func syntheticFile(id, marker string, n int) media.File {
	data := bytes.Repeat([]byte{0x5a}, n)
	copy(data, marker)
	return media.File{Info: media.Info{ID: id, Name: id + ".bin", MIME: "application/octet-stream", Size: int64(n)}, Data: data}
}

// A job carrying large files changes state several times before it is sent.
// Each change must write only the job's small row, not the files again.
func TestJobStateChangesDoNotRewriteFiles(t *testing.T) {
	t.Parallel()
	s := inboxStore(t)
	files := []media.File{syntheticFile("a", "synthetic-a", 11<<20), syntheticFile("b", "synthetic-b", 11<<20)}
	if ok, err := s.Enqueue("job", "fixture-chat", "manual", Plan{Manual: true, Attachments: files}); err != nil || !ok {
		t.Fatal(ok, err)
	}
	const limit = 256 << 10
	for _, step := range []struct {
		name string
		run  func() error
	}{
		{"start upload", func() error { _, err := s.Transition("job", "queued", "generating"); return err }},
		{"record upload", func() error {
			_, p, err := s.JobPlan("job")
			if err != nil {
				return err
			}
			for i := range p.Attachments {
				p.Attachments[i].RemoteID = "synthetic-remote-" + p.Attachments[i].ID
			}
			return s.SavePlan("job", p)
		}},
		{"back to queue", func() error { _, err := s.Transition("job", "generating", "queued"); return err }},
	} {
		var err error
		if grew := walGrowth(t, s, func() { err = step.run() }); err != nil || grew > limit {
			t.Fatalf("%s wrote %d bytes (limit %d): %v", step.name, grew, limit, err)
		}
	}
	_, p, err := s.JobPlan("job")
	if err != nil {
		t.Fatal(err)
	}
	for i, f := range p.Attachments {
		if !bytes.Equal(f.Data, files[i].Data) || f.RemoteID != "synthetic-remote-"+f.ID {
			t.Fatalf("file %d not kept: %d bytes, remote %q", i, len(f.Data), f.RemoteID)
		}
	}
}

func planFileRows(t *testing.T, s *Store, job string) int {
	t.Helper()
	var n int
	if err := s.DB.QueryRow("SELECT count(*) FROM job_files WHERE job=?", job).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// File bytes are sealed, bound to their job and position, and checked against
// the plan; a step that produces a new file (a downloaded video) replaces it.
func TestPlanFilesAreSealedBoundAndReplaceable(t *testing.T) {
	t.Parallel()
	s := inboxStore(t)
	for _, id := range []string{"a", "b"} {
		if ok, err := s.Enqueue(id, "fixture-chat", "manual", Plan{Manual: true, Attachments: []media.File{syntheticFile(id, "synthetic-plaintext-"+id, 4096)}}); err != nil || !ok {
			t.Fatal(ok, err)
		}
	}
	if _, err := s.DB.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{s.Path, s.Path + "-wal"} {
		if b, _ := os.ReadFile(file); bytes.Contains(b, []byte("synthetic-plaintext-")) {
			t.Fatal("file bytes stored in plaintext in", file)
		}
	}

	// A row copied from another job does not open as this job's file.
	if _, err := s.DB.Exec("UPDATE job_files SET data=(SELECT data FROM job_files WHERE job='a') WHERE job='b'"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.JobPlan("b"); err == nil {
		t.Fatal("another job's file accepted")
	}
	// Job reads no files, so the state can still be checked.
	if j, err := s.Job("b"); err != nil || j.State != "queued" {
		t.Fatal(j.State, err)
	}

	// A new file from a step replaces the old one; dropping it removes its row.
	video := syntheticFile("video", "synthetic-video", 8192)
	if err := s.SavePlan("a", Plan{Manual: true, Attachments: []media.File{video}}); err != nil {
		t.Fatal(err)
	}
	if _, p, err := s.JobPlan("a"); err != nil || len(p.Attachments) != 1 || !bytes.Equal(p.Attachments[0].Data, video.Data) {
		t.Fatal("new file not saved", err)
	}
	if err := s.SavePlan("a", Plan{Manual: true, Reply: "synthetic text only"}); err != nil {
		t.Fatal(err)
	}
	if n := planFileRows(t, s, "a"); n != 0 {
		t.Fatalf("%d rows left after the file was dropped", n)
	}
}

// Every way a plan is cleared also removes its files, and a repeated request
// keeps the files of the job it already made.
func TestPlanFilesLiveAsLongAsThePlan(t *testing.T) {
	t.Parallel()
	s := inboxStore(t)
	enqueue := func(id, marker string) bool {
		ok, err := s.Enqueue(id, "fixture-chat", "manual", Plan{Manual: true, Attachments: []media.File{syntheticFile(id, marker, 1024)}})
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}
	if !enqueue("repeat", "synthetic-first") || enqueue("repeat", "synthetic-second") {
		t.Fatal("a repeated request made a second job")
	}
	if _, p, err := s.JobPlan("repeat"); err != nil || !bytes.HasPrefix(p.Attachments[0].Data, []byte("synthetic-first")) {
		t.Fatal("a repeated request replaced the job's file", err)
	}
	for name, clear := range map[string]func(id string) error{
		"complete": func(id string) error { return s.Complete(id, "submitted", "") },
		"cancel":   s.Cancel,
		"pause":    func(string) error { return s.Pause(true) },
		"delete":   func(id string) error { _, err := s.DB.Exec("DELETE FROM jobs WHERE id=?", id); return err },
	} {
		id := "clear-" + name
		enqueue(id, "synthetic-"+name)
		if planFileRows(t, s, id) != 1 {
			t.Fatal(name, "file not stored")
		}
		if err := clear(id); err != nil {
			t.Fatal(name, err)
		}
		if n := planFileRows(t, s, id); n != 0 {
			t.Fatalf("%s left %d file rows", name, n)
		}
		if err := s.Pause(false); err != nil {
			t.Fatal(err)
		}
	}
}

// A step that fails ends its job, which erases the plan. cmdflow then saves
// the run once more; that late save must not bring the prompt, history or
// files back on the ended job.
func TestEndedJobPlanIsNotWrittenBack(t *testing.T) {
	t.Parallel()
	s := inboxStore(t)
	p := Plan{Manual: true, Reply: "synthetic reply", Attachments: []media.File{syntheticFile("late", "synthetic-late", 1024)}}
	if ok, err := s.Enqueue("late", "fixture-chat", "manual", p); err != nil || !ok {
		t.Fatal("enqueue", ok, err)
	}
	for _, state := range []string{"failed", "cancelled", "expired", "submitted"} {
		if _, err := s.DB.Exec("UPDATE jobs SET state='queued' WHERE id='late'"); err != nil {
			t.Fatal(err)
		}
		if err := s.SavePlan("late", p); err != nil {
			t.Fatal(err)
		}
		if err := s.Complete("late", state, "synthetic_code"); err != nil {
			t.Fatal(err)
		}
		if err := s.SavePlan("late", p); err != nil {
			t.Fatal(state, "late save:", err)
		}
		var plan string
		if err := s.DB.QueryRow("SELECT plan FROM jobs WHERE id='late'").Scan(&plan); err != nil {
			t.Fatal(err)
		}
		if plan != "" || planFileRows(t, s, "late") != 0 {
			t.Fatalf("a late save wrote the plan back on a %s job", state)
		}
	}
	// A job still in progress keeps saving normally.
	if _, err := s.DB.Exec("UPDATE jobs SET state='generating' WHERE id='late'"); err != nil {
		t.Fatal(err)
	}
	if err := s.SavePlan("late", p); err != nil {
		t.Fatal(err)
	}
	if _, got, err := s.JobPlan("late"); err != nil || got.Reply != "synthetic reply" {
		t.Fatal("an in-progress job lost its plan", err)
	}
}

// A file staged by a pipeline step is sealed in job_files out of jobs.plan,
// bound to its job and position, promoted into Attachments without rewriting
// its bytes, and removed when the plan is cleared.
func TestStagedPlanFilesAreSealedBoundAndPromotedWithoutRewrite(t *testing.T) {
	t.Parallel()
	s := inboxStore(t)
	video := syntheticFile("v", "synthetic-staged-video", 256<<10)
	var p Plan
	handle := p.StageFile("video", video)
	if ok, err := s.Enqueue("staged-a", "fixture-chat", "!download", p); err != nil || !ok {
		t.Fatal(ok, err)
	}
	var other Plan
	other.StageFile("video", syntheticFile("v2", "synthetic-staged-other", 256<<10))
	if ok, err := s.Enqueue("staged-b", "fixture-chat", "!download", other); err != nil || !ok {
		t.Fatal(ok, err)
	}

	var enc string
	if err := s.DB.QueryRow("SELECT plan FROM jobs WHERE id='staged-a'").Scan(&enc); err != nil {
		t.Fatal(err)
	}
	if len(enc) > 4096 {
		t.Fatalf("sealed plan carried file bytes (%d bytes)", len(enc))
	}
	plain, err := s.Vault.Open("plan:staged-a", enc)
	if err != nil || bytes.Contains(plain, []byte("synthetic-staged-video")) {
		t.Fatal("staged video bytes present in saved plan", err)
	}
	if _, err := s.DB.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{s.Path, s.Path + "-wal"} {
		if b, _ := os.ReadFile(file); bytes.Contains(b, []byte("synthetic-staged-")) {
			t.Fatal("staged file bytes stored in plaintext in", file)
		}
	}

	// Another job's staged file row is rejected.
	if _, err := s.DB.Exec("UPDATE job_files SET data=(SELECT data FROM job_files WHERE job='staged-a') WHERE job='staged-b'"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.JobPlan("staged-b"); err == nil {
		t.Fatal("another job's staged file accepted")
	}

	_, loaded, err := s.JobPlan("staged-a")
	if err != nil {
		t.Fatal(err)
	}
	got, ok := loaded.StagedFile(handle)
	if !ok || !bytes.Equal(got.Data, video.Data) {
		t.Fatal("staged file did not round-trip intact")
	}

	// Promoting the staged file into Attachments reuses the job_files slot.
	loaded.Attachments = []media.File{got}
	loaded.Staged = nil
	if grew := walGrowth(t, s, func() { err = s.SavePlan("staged-a", loaded) }); err != nil || grew > 32<<10 {
		t.Fatalf("promoting staged file wrote %d bytes: %v", grew, err)
	}
	if _, after, err := s.JobPlan("staged-a"); err != nil || len(after.Attachments) != 1 || !bytes.Equal(after.Attachments[0].Data, video.Data) || len(after.Staged) != 0 {
		t.Fatal("promoted attachment lost", err)
	}
	if err := s.Cancel("staged-a"); err != nil {
		t.Fatal(err)
	}
	if n := planFileRows(t, s, "staged-a"); n != 0 {
		t.Fatalf("%d rows left after cancel", n)
	}
}
