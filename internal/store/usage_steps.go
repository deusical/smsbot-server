package store

import (
	"strings"
)

// A job can make several AI requests, one per AI step. Each step's usage is
// kept in usage_steps; the job's row in usage is their total, recomputed in
// the same transaction, so Activity, the daily budget and backups keep
// reading one row per job. A step whose cost is unknown makes the job's cost
// unknown, which blocks further AI requests until it is known.
func (s *Store) migrateUsageSteps() error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`CREATE TABLE IF NOT EXISTS usage_steps(job TEXT NOT NULL,step TEXT NOT NULL,model TEXT NOT NULL,prompt_tokens INTEGER NOT NULL,completion_tokens INTEGER NOT NULL,reasoning_tokens INTEGER NOT NULL,cost REAL,PRIMARY KEY(job,step));
 INSERT OR IGNORE INTO usage_steps SELECT job,'',model,prompt_tokens,completion_tokens,reasoning_tokens,cost FROM usage;
 PRAGMA user_version=9;`); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	return s.migrate()
}

// StepUsage records one AI step's usage and updates the job's total.
func (s *Store) StepUsage(job, step, model string, prompt, completion, reasoning int, cost *float64) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec("INSERT INTO usage_steps(job,step,model,prompt_tokens,completion_tokens,reasoning_tokens,cost) VALUES(?,?,?,?,?,?,?) ON CONFLICT(job,step) DO UPDATE SET model=excluded.model,prompt_tokens=excluded.prompt_tokens,completion_tokens=excluded.completion_tokens,reasoning_tokens=excluded.reasoning_tokens,cost=excluded.cost", job, step, model, prompt, completion, reasoning, cost); err != nil {
		return err
	}
	rows, err := tx.Query("SELECT model,prompt_tokens,completion_tokens,reasoning_tokens,cost FROM usage_steps WHERE job=? ORDER BY rowid", job)
	if err != nil {
		return err
	}
	models := []string{}
	var p, c, r int
	var total float64
	known := true
	for rows.Next() {
		var m string
		var sp, sc, sr int
		var cost *float64
		if err = rows.Scan(&m, &sp, &sc, &sr, &cost); err != nil {
			rows.Close()
			return err
		}
		if m != "" && !contains(models, m) {
			models = append(models, m)
		}
		p, c, r = p+sp, c+sc, r+sr
		if cost == nil {
			known = false
		} else {
			total += *cost
		}
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return err
	}
	var sum *float64
	if known {
		sum = &total
	}
	if _, err = tx.Exec("INSERT INTO usage(job,model,prompt_tokens,completion_tokens,reasoning_tokens,cost) VALUES(?,?,?,?,?,?) ON CONFLICT(job) DO UPDATE SET model=excluded.model,prompt_tokens=excluded.prompt_tokens,completion_tokens=excluded.completion_tokens,reasoning_tokens=excluded.reasoning_tokens,cost=excluded.cost", job, strings.Join(models, ", "), p, c, r, sum); err != nil {
		return err
	}
	return tx.Commit()
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}
