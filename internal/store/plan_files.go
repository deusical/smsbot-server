package store

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"github.com/deusical/smsbot-server/internal/media"
)

// A plan's attachment bytes are kept out of the plan itself. A job's row changes
// state several times while it runs, and SQLite rewrites a whole row on every
// update, so bytes inside the plan (base64 inside the sealed, base64 plan) were
// rewritten on each change: about 40 MB per state change for 22 MB of files.
//
// job_files holds each file once, sealed as a BLOB under a scope that binds it to
// its job and position. The plan records each file's SHA-256, so a row cannot be
// swapped or replaced unnoticed, and a re-saved plan rewrites only files whose
// bytes changed. The rows live exactly as long as the plan: every statement that
// clears a plan, or deletes its job, removes them in the same statement.
const jobFilesSchema = `CREATE TABLE IF NOT EXISTS job_files(job TEXT NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,idx INTEGER NOT NULL,data BLOB NOT NULL,PRIMARY KEY(job,idx));
CREATE TRIGGER IF NOT EXISTS job_files_follow_plan AFTER UPDATE OF plan ON jobs WHEN NEW.plan='' BEGIN DELETE FROM job_files WHERE job=NEW.id; END;`

// planFile is a file held in job_files for attachment Index.
type planFile struct {
	Index  int    `json:"index"`
	SHA256 string `json:"sha256"`
}

// storedPlan is a plan as sealed in jobs.plan: attachments without their bytes,
// and where those bytes are.
type storedPlan struct {
	Plan
	Files []planFile `json:"stored_files,omitempty"`
}

type planFileWrite struct {
	index int
	data  []byte
}

type rowQuerier interface {
	QueryRow(query string, args ...any) *sql.Row
}

func planFileScope(job string, index int) string {
	return "plan-file:" + job + ":" + strconv.Itoa(index)
}

// sealPlan seals p without its file bytes. It returns the files the plan now
// references and those to write: the ones not already stored as previous.
func (s *Store) sealPlan(id string, p Plan, previous []planFile) (string, []planFile, []planFileWrite, error) {
	stored := storedPlan{Plan: p}
	stored.Attachments = append([]media.File(nil), p.Attachments...)
	kept := make(map[planFile]bool, len(previous))
	for _, f := range previous {
		kept[f] = true
	}
	var writes []planFileWrite
	for i := range stored.Attachments {
		data := stored.Attachments[i].Data
		if len(data) == 0 {
			continue
		}
		sum := sha256.Sum256(data)
		ref := planFile{Index: i, SHA256: hex.EncodeToString(sum[:])}
		stored.Files = append(stored.Files, ref)
		if !kept[ref] {
			writes = append(writes, planFileWrite{i, data})
		}
		stored.Attachments[i].Data = nil
	}
	b, err := json.Marshal(stored)
	if err != nil {
		return "", nil, nil, err
	}
	enc, err := s.Vault.Seal("plan:"+id, b)
	return enc, stored.Files, writes, err
}

// writePlanFiles stores writes and removes rows the plan no longer references.
func (s *Store) writePlanFiles(tx *sql.Tx, id string, refs []planFile, writes []planFileWrite) error {
	for _, w := range writes {
		sealed, err := s.Vault.SealBytes(planFileScope(id, w.index), w.data)
		if err != nil {
			return err
		}
		if _, err = tx.Exec("INSERT INTO job_files(job,idx,data) VALUES(?,?,?) ON CONFLICT(job,idx) DO UPDATE SET data=excluded.data", id, w.index, sealed); err != nil {
			return err
		}
	}
	query, args := "DELETE FROM job_files WHERE job=?", []any{id}
	if len(refs) > 0 {
		query += " AND idx NOT IN (" + strings.TrimSuffix(strings.Repeat("?,", len(refs)), ",") + ")"
		for _, r := range refs {
			args = append(args, r.Index)
		}
	}
	_, err := tx.Exec(query, args...)
	return err
}

// openPlan opens a sealed plan. With files, it reads each file's bytes back and
// checks them against the plan; without, attachments come back without bytes.
func (s *Store) openPlan(q rowQuerier, id, enc string, files bool) (Plan, []planFile, error) {
	b, err := s.Vault.Open("plan:"+id, enc)
	if err != nil {
		return Plan{}, nil, err
	}
	var stored storedPlan
	if err = json.Unmarshal(b, &stored); err != nil {
		return Plan{}, nil, err
	}
	if !files {
		return stored.Plan, stored.Files, nil
	}
	for _, ref := range stored.Files {
		if ref.Index < 0 || ref.Index >= len(stored.Attachments) {
			return Plan{}, nil, errors.New("plan file out of range")
		}
		var sealed []byte
		if err = q.QueryRow("SELECT data FROM job_files WHERE job=? AND idx=?", id, ref.Index).Scan(&sealed); err != nil {
			return Plan{}, nil, err
		}
		data, err := s.Vault.OpenBytes(planFileScope(id, ref.Index), sealed)
		if err != nil {
			return Plan{}, nil, err
		}
		if sum := sha256.Sum256(data); hex.EncodeToString(sum[:]) != ref.SHA256 {
			return Plan{}, nil, errors.New("plan file does not match its plan")
		}
		stored.Attachments[ref.Index].Data = data
	}
	return stored.Plan, stored.Files, nil
}

// Job returns a job without reading its files: for checking a job's state or
// conversation. It fails when the stored plan does not open, as JobPlan does.
func (s *Store) Job(id string) (Job, error) {
	var j Job
	var enc string
	if err := s.DB.QueryRow("SELECT id,conversation,command,state,created,txn,code,plan FROM jobs WHERE id=?", id).Scan(&j.ID, &j.ConversationID, &j.Command, &j.State, &j.CreatedAt, &j.TransactionID, &j.Code, &enc); err != nil {
		return j, err
	}
	_, _, err := s.openPlan(s.DB, id, enc, false)
	return j, err
}
