package store

import (
	"strings"
	"testing"
	"time"
)

func TestStepUsageTotalsEachJob(t *testing.T) {
	s := inboxStore(t)
	if _, err := s.Enqueue("job-a", "synthetic-chat", "!chain", Plan{}); err != nil {
		t.Fatal(err)
	}
	one, two := 0.25, 0.5
	if err := s.StepUsage("job-a", "summary", "model/a", 10, 5, 1, &one); err != nil {
		t.Fatal(err)
	}
	// While the second step is generating its cost is unknown.
	if err := s.StepUsage("job-a", "translation", "model/b", 0, 0, 0, nil); err != nil {
		t.Fatal(err)
	}
	day := time.Now().UTC().Format("2006-01-02") + "T00:00:00Z"
	if spent, unknown, err := s.SpendSince(day); err != nil || spent != 0 || unknown != 1 {
		t.Fatal("pending step not unknown", spent, unknown, err)
	}
	if err := s.StepUsage("job-a", "translation", "model/b", 20, 7, 0, &two); err != nil {
		t.Fatal(err)
	}
	if spent, unknown, err := s.SpendSince(day); err != nil || spent != 0.75 || unknown != 0 {
		t.Fatal("steps not totalled", spent, unknown, err)
	}
	jobs, err := s.Jobs()
	if err != nil || len(jobs) != 1 {
		t.Fatal(err)
	}
	j := jobs[0]
	if j.Model != "model/a, model/b" || j.PromptTokens != 30 || j.CompletionTokens != 12 || j.ReasoningTokens != 1 || j.Cost == nil || *j.Cost != 0.75 {
		t.Fatalf("job total %+v", j)
	}
	// A single-request job keeps its old shape.
	if err := s.Usage("job-a", "model/a", 1, 1, 0, &one); err != nil {
		t.Fatal(err)
	}
	jobs, _ = s.Jobs()
	if *jobs[0].Cost != 1.0 || jobs[0].Model != "model/a, model/b" {
		t.Fatalf("legacy step not added %+v", jobs[0])
	}
}

func TestUsageStepsMigrationKeepsExistingUsage(t *testing.T) {
	s := inboxStore(t)
	if _, err := s.Enqueue("job-old", "synthetic-chat", "!ask", Plan{}); err != nil {
		t.Fatal(err)
	}
	cost := 0.1
	if _, err := s.DB.Exec("DROP TABLE usage_steps; INSERT INTO usage VALUES('job-old','model/a',3,4,5,?); PRAGMA user_version=8", cost); err != nil {
		t.Fatal(err)
	}
	if err := s.migrate(); err != nil {
		t.Fatal(err)
	}
	var version, n int
	s.DB.QueryRow("PRAGMA user_version").Scan(&version)
	s.DB.QueryRow("SELECT count(*) FROM usage_steps WHERE job='job-old' AND step='' AND cost=0.1").Scan(&n)
	if version != 10 || n != 1 {
		t.Fatal("existing usage not carried into steps", version, n)
	}
	// A later step adds to it instead of replacing it.
	if err := s.StepUsage("job-old", "second", "model/a", 1, 1, 1, &cost); err != nil {
		t.Fatal(err)
	}
	jobs, _ := s.Jobs()
	if jobs[0].PromptTokens != 4 || *jobs[0].Cost != 0.2 {
		t.Fatalf("migrated usage replaced %+v", jobs[0])
	}
}

func TestRestoreRefusesLeftoverStepUsage(t *testing.T) {
	s := inboxStore(t)
	if _, err := s.DB.Exec("INSERT INTO usage_steps VALUES('orphan','x','m',0,0,0,NULL)"); err != nil {
		t.Fatal(err)
	}
	backup, err := s.ExportBackup()
	if err != nil {
		t.Fatal(err)
	}
	if err = s.RestoreBackup(backup); err == nil || !strings.Contains(err.Error(), "fresh") {
		t.Fatal("restore over leftover step usage", err)
	}
}
