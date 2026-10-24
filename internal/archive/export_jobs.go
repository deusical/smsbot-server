package archive

// Export jobs: the one owner-started export at a time, its persisted status,
// cancellation, and the finished file.

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"

	"github.com/deusical/smsbot-server/internal/store"
)

func (s *Service) Job() (*Job, error) {
	s.jobsMu.Lock()
	defer s.jobsMu.Unlock()
	if s.job != nil {
		copy := *s.job
		return &copy, nil
	}
	b, err := s.Store.GetSecretSetting("archive_backup_job", "archive-backup-job")
	if err != nil || len(b) == 0 {
		return nil, err
	}
	var job Job
	if err = json.Unmarshal(b, &job); err != nil {
		return nil, err
	}
	if job.State == "running" {
		if validID(job.ID) {
			_ = os.Remove(filepath.Join(s.Root, "exports", job.ID+exportSuffix(job.Scope)+".part"))
		}
		job.State = "interrupted"
		job.Error = "restart_interrupted_export"
	}
	s.job = &job
	return &job, nil
}

func (s *Service) saveJobLocked() error {
	b, err := json.Marshal(s.job)
	if err != nil {
		return err
	}
	return s.Store.SetSecretSetting("archive_backup_job", "archive-backup-job", b)
}

func (s *Service) Cancel() {
	s.jobsMu.Lock()
	defer s.jobsMu.Unlock()
	if s.cancel != nil {
		s.cancel()
	}
}

func (s *Service) StartExport(scope Scope) (*Job, error) {
	if err := scope.Validate(); err != nil {
		return nil, errors.New("invalid_backup_scope")
	}
	if _, err := s.Job(); err != nil {
		return nil, errors.New("backup_status_unavailable")
	}
	if len(scope.Conversations) == 0 {
		scope.Conversations = nil
	} else {
		scope.Conversations = append([]string(nil), scope.Conversations...)
	}
	s.jobsMu.Lock()
	defer s.jobsMu.Unlock()
	if s.closing {
		return nil, errors.New("archive_stopping")
	}
	if s.cancel != nil {
		if s.job != nil && reflect.DeepEqual(s.job.Scope, scope) {
			copy := *s.job
			return &copy, nil
		}
		return nil, errors.New("backup_busy")
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	job := &Job{ID: store.ID(), State: "running", Scope: scope}
	s.job = job
	if err := s.saveJobLocked(); err != nil {
		s.cancel = nil
		cancel()
		s.job = nil
		return nil, errors.New("cannot_persist_archive_job")
	}

	if err := s.cleanOldExports(job.ID); err != nil {
		s.cancel = nil
		cancel()
		s.job.State = "failed"
		s.job.Error = "cannot_remove_previous_export"
		_ = s.saveJobLocked()
		return nil, errors.New("cannot_remove_previous_export")
	}
	s.jobsWait.Add(1)
	go func() {
		defer s.jobsWait.Done()
		err := os.MkdirAll(filepath.Join(s.Root, "exports"), 0700)
		output := filepath.Join(s.Root, "exports", job.ID+exportSuffix(scope))
		var f *os.File
		if err == nil {
			f, err = os.OpenFile(output+".part", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		}
		var counts Counts
		if err == nil {
			counts, err = s.Export(ctx, f, scope, func(c Counts) { s.jobsMu.Lock(); s.job.Counts = c; s.jobsMu.Unlock() })
		}
		if f != nil {
			if err == nil {
				err = f.Sync()
			}
			ce := f.Close()
			if err == nil {
				err = ce
			}
		}
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		if err == nil {
			err = os.Rename(output+".part", output)
		}
		if err != nil {
			_ = os.Remove(output + ".part")
		}
		s.jobsMu.Lock()
		defer s.jobsMu.Unlock()
		s.job.Counts = counts
		s.job.State = "complete"
		s.job.Error = ""
		if err != nil {
			s.job.State = "failed"
			s.job.Error = "archive_export_failed"
			if ctx.Err() != nil {
				s.job.State = "cancelled"
				s.job.Error = ""
			}
		}
		s.cancel = nil
		cancel()
		if err := s.saveJobLocked(); err != nil {
			s.job.State = "failed"
			s.job.Error = "cannot_persist_archive_job"
		}
	}()
	copy := *job
	return &copy, nil
}

func (s *Service) ExportFile(id string) (*os.File, error) {
	job, err := s.Job()
	if err != nil || job == nil || job.ID != id || job.State != "complete" || !validID(id) {
		return nil, errors.New("backup_not_complete")
	}
	return os.Open(filepath.Join(s.Root, "exports", id+exportSuffix(job.Scope)))
}

func exportSuffix(scope Scope) string {
	if scope.Plaintext {
		return ".tar"
	}
	return ".sma"
}

func (s *Service) cleanOldExports(keep string) error {
	entries, err := os.ReadDir(filepath.Join(s.Root, "exports"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if len(name) < 32 || !validID(name[:32]) || name[:32] == keep {
			continue
		}
		suffix := name[32:]
		if suffix != ".tar" && suffix != ".sma" && suffix != ".tar.part" && suffix != ".sma.part" {
			continue
		}
		if err = os.Remove(filepath.Join(s.Root, "exports", name)); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) DeleteExport() error {
	s.jobsMu.Lock()
	defer s.jobsMu.Unlock()
	if s.cancel != nil {
		return errors.New("backup_running")
	}
	if err := s.cleanOldExports(""); err != nil {
		return err
	}
	if err := s.Store.SetSecretSetting("archive_backup_job", "archive-backup-job", nil); err != nil {
		return err
	}
	s.job = nil
	return nil
}
