package store

import (
	"database/sql"
	"strings"
)

// JobStep is one step of a job's pipeline as Activity shows it: the step's
// name and module (owner configuration, no message content), whether it has
// finished, and, for an AI step, its usage.
type JobStep struct {
	ID               string   `json:"id"`
	Module           string   `json:"module"`
	Done             bool     `json:"done"`
	Model            string   `json:"model,omitempty"`
	PromptTokens     int      `json:"prompt_tokens,omitempty"`
	CompletionTokens int      `json:"completion_tokens,omitempty"`
	ReasoningTokens  int      `json:"reasoning_tokens,omitempty"`
	Cost             *float64 `json:"cost,omitempty"`
	// Used is set when the step made an AI request, whatever its cost.
	Used bool `json:"used,omitempty"`
}

func (s *Store) migrateJobSteps() error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`CREATE TABLE IF NOT EXISTS job_steps(job TEXT NOT NULL,position INTEGER NOT NULL,step TEXT NOT NULL,module TEXT NOT NULL,done INTEGER NOT NULL DEFAULT 0,PRIMARY KEY(job,position));
 PRAGMA user_version=10;`); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	return s.migrate()
}

type execer interface {
	Exec(string, ...any) (sql.Result, error)
}

// recordSteps writes the job's step outline when it is enqueued.
func recordSteps(q execer, job string, p Plan) error {
	if p.Pipeline == nil {
		return nil
	}
	for i, step := range p.Pipeline.Steps {
		if i == 0 {
			continue // the trigger
		}
		if _, err := q.Exec("INSERT OR IGNORE INTO job_steps VALUES(?,?,?,?,?)", job, i, step.ID, step.Module, step.Finished()); err != nil {
			return err
		}
	}
	return nil
}

// syncSteps marks the steps the plan has finished.
func syncSteps(q execer, job string, p Plan) error {
	if p.Pipeline == nil {
		return nil
	}
	for i, step := range p.Pipeline.Steps {
		if i > 0 && step.Finished() {
			if _, err := q.Exec("UPDATE job_steps SET done=1 WHERE job=? AND position=? AND done=0", job, i); err != nil {
				return err
			}
		}
	}
	return nil
}

// attachSteps adds each listed job's steps and per-step AI usage.
func (s *Store) attachSteps(jobs []Job) error {
	if len(jobs) == 0 {
		return nil
	}
	index := map[string]int{}
	args := make([]any, len(jobs))
	for i, j := range jobs {
		index[j.ID] = i
		args[i] = j.ID
	}
	in := strings.TrimSuffix(strings.Repeat("?,", len(jobs)), ",")
	rows, err := s.DB.Query("SELECT s.job,s.step,s.module,s.done,u.model,u.prompt_tokens,u.completion_tokens,u.reasoning_tokens,u.cost,u.job IS NOT NULL FROM job_steps s LEFT JOIN usage_steps u ON u.job=s.job AND u.step=s.step WHERE s.job IN ("+in+") ORDER BY s.job,s.position", args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var job string
		var st JobStep
		var model sql.NullString
		var p, c, r sql.NullInt64
		if err = rows.Scan(&job, &st.ID, &st.Module, &st.Done, &model, &p, &c, &r, &st.Cost, &st.Used); err != nil {
			return err
		}
		st.Model, st.PromptTokens, st.CompletionTokens, st.ReasoningTokens = model.String, int(p.Int64), int(c.Int64), int(r.Int64)
		jobs[index[job]].Steps = append(jobs[index[job]].Steps, st)
	}
	return rows.Err()
}
