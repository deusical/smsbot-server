package store

import (
	"database/sql"
	"errors"
	"time"
)

// MaxWorkerWindow is the longest execution deadline a bot job can give a worker
// task (execution_minutes <= 60). Older ended jobs have already expired on the
// worker and need no cancel request.
const MaxWorkerWindow = time.Hour

// CancelUnsentJobs cancels every job that has not reached submission, with a
// sanitized code. Submitted and uncertain jobs keep their state and evidence.
func (s *Store) CancelUnsentJobs(code string) error {
	_, err := s.DB.Exec("UPDATE jobs SET state='cancelled',code=?,plan='' WHERE state IN ('queued','generating','awaiting_approval')", code)
	return err
}

// JobExists reports whether a job with this ID was ever recorded.
func (s *Store) JobExists(id string) (bool, error) {
	var n int
	err := s.DB.QueryRow("SELECT count(*) FROM jobs WHERE id=?", id).Scan(&n)
	return n > 0, err
}

// ManualJobExists reports whether this owner-typed send was already recorded
// for the conversation.
func (s *Store) ManualJobExists(id, conv string) (bool, error) {
	var n int
	err := s.DB.QueryRow("SELECT count(*) FROM jobs WHERE id=? AND conversation=? AND command='manual'", id, conv).Scan(&n)
	return n > 0, err
}

// SpendSince totals provider cost for jobs created at or after since, and
// counts usage whose cost is still unknown.
func (s *Store) SpendSince(since string) (spent float64, unknown int, err error) {
	err = s.DB.QueryRow("SELECT coalesce(sum(u.cost),0),coalesce(sum(CASE WHEN u.cost IS NULL THEN 1 ELSE 0 END),0) FROM usage u JOIN jobs j ON j.id=u.job WHERE j.created>=?", since).Scan(&spent, &unknown)
	return spent, unknown, err
}

// JobByTransaction finds the job that submitted txn in conv. ok is false when
// no job matches.
func (s *Store) JobByTransaction(conv, txn string) (id, state string, ok bool, err error) {
	err = s.DB.QueryRow("SELECT id,state FROM jobs WHERE conversation=? AND txn=?", conv, txn).Scan(&id, &state)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", false, nil
	}
	return id, state, err == nil, err
}

// ReactionJobStatus is the state and sanitized code of a reaction intent job.
func (s *Store) ReactionJobStatus(conv, id string) (state, code string, err error) {
	err = s.DB.QueryRow("SELECT j.state,j.code FROM jobs j JOIN interaction_intents a ON a.job=j.id WHERE j.id=? AND j.conversation=?", id, conv).Scan(&state, &code)
	return state, code, err
}

// DeferWork records a sanitized preparation stage and lets other queued sends run.
func (s *Store) DeferWork(id, stage string, until int64) error {
	_, err := s.DB.Exec("INSERT INTO job_work(job,stage,next_poll) VALUES(?,?,?) ON CONFLICT(job) DO UPDATE SET stage=excluded.stage,next_poll=excluded.next_poll,cancel_sent=0", id, stage, until)
	return err
}

// RecordInterruptedWorkerPoll ensures a job interrupted during an in-flight
// worker poll has a live worker stage recorded for cancellation, without
// overwriting an already-recorded live stage.
func (s *Store) RecordInterruptedWorkerPoll(id string) error {
	_, err := s.DB.Exec("INSERT INTO job_work(job,stage,next_poll) VALUES(?,'waiting_worker',0) ON CONFLICT(job) DO UPDATE SET stage='waiting_worker',cancel_sent=0 WHERE job_work.stage IN ('','complete')", id)
	return err
}

func (s *Store) ensureJobWorkCancelSent() error {
	var n int
	if err := s.DB.QueryRow("SELECT count(*) FROM pragma_table_info('job_work') WHERE name='cancel_sent'").Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	_, err := s.DB.Exec("ALTER TABLE job_work ADD COLUMN cancel_sent INTEGER NOT NULL DEFAULT 0")
	return err
}

// PendingWorkerCancels returns ended job IDs whose last recorded worker stage
// is a live worker stage (not a finished or bot-side upload stage) and whose
// worker cancellation has not yet been delivered. Jobs older than created +
// MaxWorkerWindow are marked done immediately because their worker deadline and
// lease have already expired.
func (s *Store) PendingWorkerCancels(now time.Time) ([]string, error) {
	rows, err := s.DB.Query(`SELECT w.job, j.created FROM job_work w JOIN jobs j ON j.id=w.job
		WHERE w.cancel_sent=0
		AND j.state NOT IN ('queued','generating','awaiting_approval','submitting')
		AND w.stage NOT IN ('','complete','receiving','waiting_upload','uploading','ready','failed','expired','cancelled')`)
	if err != nil {
		return nil, err
	}
	var ids, stale []string
	for rows.Next() {
		var id, createdAt string
		if err := rows.Scan(&id, &createdAt); err != nil {
			rows.Close()
			return nil, err
		}
		if created, err := time.Parse(time.RFC3339Nano, createdAt); err == nil && now.Sub(created) > MaxWorkerWindow {
			stale = append(stale, id)
			continue
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	for _, id := range stale {
		if err := s.MarkWorkerCancelSent(id); err != nil {
			return nil, err
		}
	}
	return ids, nil
}

// MarkWorkerCancelSent records that worker cancellation for job id was
// delivered (or is no longer needed) without altering job_work.stage.
func (s *Store) MarkWorkerCancelSent(id string) error {
	_, err := s.DB.Exec("UPDATE job_work SET cancel_sent=1 WHERE job=?", id)
	return err
}
