package store

import (
	"database/sql"
	"errors"
)

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

// RecordSubmitted marks a just-submitted outgoing message with its origin and
// visibility. A later delivered or read status is never downgraded.
func (s *Store) RecordSubmitted(txn, origin string, ownerOnly bool) error {
	_, err := s.DB.Exec("INSERT INTO message_meta VALUES(?,?,'submitted',?) ON CONFLICT(message) DO UPDATE SET origin=excluded.origin,owner_only=excluded.owner_only,status=CASE WHEN message_meta.status IN ('read','delivered') THEN message_meta.status ELSE 'submitted' END", txn, origin, ownerOnly)
	return err
}
