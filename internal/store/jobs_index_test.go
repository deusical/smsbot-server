package store

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/deusical/smsbot-server/internal/vault"
)

func explainPlan(t *testing.T, db *sql.DB, query string, args ...any) string {
	t.Helper()
	rows, err := db.Query("EXPLAIN QUERY PLAN "+query, args...)
	if err != nil {
		t.Fatalf("explain %s: %v", query, err)
	}
	defer rows.Close()
	var lines []string
	for rows.Next() {
		var id, parent, notused int
		var detail string
		if err := rows.Scan(&id, &parent, &notused, &detail); err != nil {
			t.Fatal(err)
		}
		lines = append(lines, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return strings.Join(lines, " | ")
}

func TestJobsIndexesAndQueryPlans(t *testing.T) {
	t.Parallel()
	queries := []struct {
		name  string
		sql   string
		args  []any
		index string
	}{
		{
			name:  "Next",
			sql:   "SELECT id,conversation,command,state,created,txn,code,plan FROM jobs WHERE state='queued' AND id NOT IN (SELECT job FROM job_work WHERE next_poll>?) ORDER BY created LIMIT 1",
			args:  []any{int64(1000)},
			index: "jobs_state_created",
		},
		{
			name:  "Jobs",
			sql:   "SELECT j.id,j.conversation,j.command,j.state,j.created,j.txn,j.code,coalesce(u.model,''),coalesce(u.prompt_tokens,0),coalesce(u.completion_tokens,0),coalesce(u.reasoning_tokens,0),u.cost,coalesce((SELECT json_extract(value,'$') FROM settings WHERE key='requested_reasoning:'||j.id),'') ,coalesce(w.stage,''),coalesce((SELECT json_extract(value,'$') FROM settings WHERE key='send_reason:'||j.id),'') FROM jobs j LEFT JOIN usage u ON u.job=j.id LEFT JOIN job_work w ON w.job=j.id ORDER BY j.created DESC LIMIT 200",
			index: "jobs_created",
		},
		{
			name:  "AlertJobs",
			sql:   "SELECT id,conversation,command,state,code FROM jobs WHERE state IN ('awaiting_approval','unknown_submission','failed') ORDER BY created DESC LIMIT 100",
			index: "jobs_state_created",
		},
		{
			name:  "Retention",
			sql:   "UPDATE jobs SET plan='' WHERE plan!='' AND julianday(created)<julianday(?) AND state NOT IN ('queued','generating','processing','awaiting_approval','submitting')",
			args:  []any{"2026-01-01T00:00:00Z"},
			index: "jobs_open_plan",
		},
		{
			name:  "Limits",
			sql:   "SELECT count(*) FROM jobs WHERE command NOT IN ('manual','reaction') AND created>=?",
			args:  []any{"2026-01-01T00:00:00Z"},
			index: "jobs_created",
		},
		{
			name:  "JobByTxn",
			sql:   "SELECT id,state,code FROM jobs WHERE conversation=? AND txn=? ORDER BY created DESC LIMIT 1",
			args:  []any{"conv-1", "txn-1"},
			index: "jobs_conversation_txn",
		},
	}

	v, err := vault.New(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "jobs.db")
	s, err := Open(path, v)
	if err != nil {
		t.Fatal(err)
	}

	// Verify schema version stayed at 10 (additive index migration pattern).
	var version int
	if err := s.DB.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 10 {
		t.Fatalf("unexpected user_version=%d err=%v", version, err)
	}

	after := map[string]string{}
	for _, q := range queries {
		plan := explainPlan(t, s.DB, q.sql, q.args...)
		after[q.name] = plan
		t.Logf("AFTER  %-10s: %s", q.name, plan)
		if !strings.Contains(plan, q.index) {
			t.Errorf("%s plan %q did not use %s", q.name, plan, q.index)
		}
	}

	// Drop the indexes to capture the pre-change plans and simulate an existing
	// schema-10 database upgrading on reopen.
	if _, err := s.DB.Exec("DROP INDEX jobs_state_created; DROP INDEX jobs_created; DROP INDEX jobs_open_plan; DROP INDEX jobs_conversation_txn;"); err != nil {
		t.Fatal(err)
	}
	for _, q := range queries {
		before := explainPlan(t, s.DB, q.sql, q.args...)
		t.Logf("BEFORE %-10s: %s", q.name, before)
		if strings.Contains(before, "SCAN jobs") && strings.Contains(before, "jobs_") {
			t.Errorf("unexpected jobs index before migration: %s", before)
		}
	}
	s.DB.Close()

	// Reopening an existing version-10 database must recreate all indexes.
	reopened, err := Open(path, v)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.DB.Close()
	for _, q := range queries {
		plan := explainPlan(t, reopened.DB, q.sql, q.args...)
		if plan != after[q.name] {
			t.Fatalf("reopened %s plan = %q, want %q", q.name, plan, after[q.name])
		}
	}
}
