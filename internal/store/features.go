package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

func (s *Store) SetSecretSetting(key, scope string, plain []byte) error {
	enc, err := s.Vault.Seal(scope, plain)
	if err != nil {
		return err
	}
	_, err = s.DB.Exec("INSERT INTO settings VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", key, enc)
	return err
}
func (s *Store) GetSecretSetting(key, scope string) ([]byte, error) {
	return s.GetSecretSettingContext(context.Background(), key, scope)
}
func (s *Store) GetSecretSettingContext(ctx context.Context, key, scope string) ([]byte, error) {
	var enc string
	err := s.DB.QueryRowContext(ctx, "SELECT value FROM settings WHERE key=?", key).Scan(&enc)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return s.Vault.Open(scope, enc)
}
func (s *Store) SetPublicSetting(key string, value any) error {
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = s.DB.Exec("INSERT INTO settings VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", key, string(b))
	return err
}
func (s *Store) GetPublicSetting(key string, value any) error {
	var raw string
	err := s.DB.QueryRow("SELECT value FROM settings WHERE key=?", key).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return json.Unmarshal([]byte(raw), value)
}
func (s *Store) JobPlan(id string) (Job, Plan, error) {
	var j Job
	var enc string
	err := s.DB.QueryRow("SELECT id,conversation,command,state,created,txn,code,plan FROM jobs WHERE id=?", id).Scan(&j.ID, &j.ConversationID, &j.Command, &j.State, &j.CreatedAt, &j.TransactionID, &j.Code, &enc)
	if err != nil {
		return j, Plan{}, err
	}
	b, err := s.Vault.Open("plan:"+id, enc)
	if err != nil {
		return j, Plan{}, err
	}
	var p Plan
	err = json.Unmarshal(b, &p)
	return j, p, err
}
func (s *Store) SavePlan(id string, p Plan) error {
	b, err := json.Marshal(p)
	if err != nil {
		return err
	}
	enc, err := s.Vault.Seal("plan:"+id, b)
	if err != nil {
		return err
	}
	_, err = s.DB.Exec("UPDATE jobs SET plan=? WHERE id=?", enc, id)
	return err
}
func (s *Store) Transition(id, from, to string) (bool, error) {
	r, err := s.DB.Exec("UPDATE jobs SET state=? WHERE id=? AND state=?", to, id, from)
	if err != nil {
		return false, err
	}
	n, err := r.RowsAffected()
	return n == 1, err
}
func (s *Store) Cancel(id string) error {
	_, err := s.DB.Exec("UPDATE jobs SET state='cancelled',code='owner_cancelled',plan='' WHERE id=? AND state IN ('queued','generating','awaiting_approval')", id)
	return err
}
func (s *Store) Usage(id, model string, prompt, completion, reasoning int, cost *float64) error {
	_, err := s.DB.Exec("INSERT INTO usage(job,model,prompt_tokens,completion_tokens,reasoning_tokens,cost) VALUES(?,?,?,?,?,?) ON CONFLICT(job) DO UPDATE SET model=excluded.model,prompt_tokens=excluded.prompt_tokens,completion_tokens=excluded.completion_tokens,reasoning_tokens=excluded.reasoning_tokens,cost=excluded.cost", id, model, prompt, completion, reasoning, cost)
	return err
}
func (s *Store) Limits(now time.Time, hourly, daily int, budget float64, reserve float64) (bool, error) {
	var h, d int
	var spent float64
	var unknown int
	err := s.DB.QueryRow("SELECT count(*) FROM jobs WHERE created>=?", now.Add(-time.Hour).UTC().Format(time.RFC3339Nano)).Scan(&h)
	if err != nil {
		return false, err
	}
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
	err = s.DB.QueryRow("SELECT count(*) FROM jobs WHERE created>=?", day).Scan(&d)
	if err != nil {
		return false, err
	}
	err = s.DB.QueryRow("SELECT coalesce(sum(u.cost),0),coalesce(sum(CASE WHEN u.cost IS NULL THEN 1 ELSE 0 END),0) FROM usage u JOIN jobs j ON j.id=u.job WHERE j.created>=?", day).Scan(&spent, &unknown)
	return err == nil && h < hourly && d < daily && (budget == 0 || unknown == 0 && spent+reserve <= budget), err
}

type Turn struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

func (s *Store) Memory(scope string, now time.Time) ([]Turn, error) {
	var enc, expires string
	err := s.DB.QueryRow("SELECT payload,expires FROM memory WHERE scope=?", scope).Scan(&enc, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return []Turn{}, nil
	}
	if err != nil {
		return nil, err
	}
	at, err := time.Parse(time.RFC3339Nano, expires)
	if err != nil || !at.After(now) {
		return []Turn{}, nil
	}
	b, err := s.Vault.Open("memory:"+scope, enc)
	if err != nil {
		return nil, err
	}
	var turns []Turn
	err = json.Unmarshal(b, &turns)
	return turns, err
}
func (s *Store) Remember(scope string, turns []Turn, expires time.Time) error {
	if len(turns) > 8 {
		turns = turns[len(turns)-8:]
	}
	b, err := json.Marshal(turns)
	if err != nil {
		return err
	}
	enc, err := s.Vault.Seal("memory:"+scope, b)
	if err != nil {
		return err
	}
	_, err = s.DB.Exec("INSERT INTO memory VALUES(?,?,?) ON CONFLICT(scope) DO UPDATE SET payload=excluded.payload,expires=excluded.expires", scope, enc, expires.UTC().Format(time.RFC3339Nano))
	return err
}
func (s *Store) ClearMemory(conv string) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = clearAIThreads(tx, conv); err != nil {
		return err
	}
	if _, err = tx.Exec("DELETE FROM memory WHERE scope LIKE ?", Hash(conv)+":%"); err != nil {
		return err
	}
	if _, err = tx.Exec("INSERT INTO settings VALUES(?, '1') ON CONFLICT(key) DO UPDATE SET value=CAST(value AS INTEGER)+1", "memory_epoch:"+Hash(conv)); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) MemoryEpoch(conv string) (int64, error) {
	var epoch int64
	err := s.DB.QueryRow("SELECT CAST(value AS INTEGER) FROM settings WHERE key=?", "memory_epoch:"+Hash(conv)).Scan(&epoch)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return epoch, err
}

// InvalidateMemory is used when consent is removed from any command. Conservatively
// discard all stored context and invalidate every enrolled conversation's plans.
func (s *Store) InvalidateMemory() error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.Query("SELECT conversation FROM enrollment UNION SELECT conversation FROM jobs")
	if err != nil {
		return err
	}
	var conversations []string
	for rows.Next() {
		var conv string
		if err = rows.Scan(&conv); err != nil {
			rows.Close()
			return err
		}
		conversations = append(conversations, conv)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if err = clearAIThreads(tx, ""); err != nil {
		return err
	}
	if _, err = tx.Exec("DELETE FROM memory"); err != nil {
		return err
	}
	for _, conv := range conversations {
		if _, err = tx.Exec("INSERT INTO settings VALUES(?,'1') ON CONFLICT(key) DO UPDATE SET value=CAST(value AS INTEGER)+1", "memory_epoch:"+Hash(conv)); err != nil {
			return err
		}
	}
	return tx.Commit()
}
