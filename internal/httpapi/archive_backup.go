package httpapi

import (
	"context"
	"net/http"
	"time"

	"github.com/deusical/smsbot-server/internal/archive"
)

func (s *Server) archiveBackupRoutes() {
	s.mux.HandleFunc("GET /api/v1/inbox/archive/stats", s.protected(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		stats, err := s.Collection.Archive.Stats(ctx)
		if err != nil {
			s.bad(w, 503, "archive_stats_unavailable")
			return
		}
		s.json(w, 200, stats)
	}))
	s.mux.HandleFunc("DELETE /api/v1/inbox/archive/backup", s.protected(func(w http.ResponseWriter, r *http.Request) {
		if err := s.Collection.Archive.DeleteExport(); err != nil {
			s.bad(w, 409, "cannot_delete_export")
			return
		}
		s.json(w, 200, map[string]bool{"deleted": true})
	}))
	s.mux.HandleFunc("GET /api/v1/inbox/archive/files/policy", s.protected(func(w http.ResponseWriter, r *http.Request) {
		p, err := s.Collection.Archive.Policy()
		if err != nil {
			s.bad(w, 500, "archive_policy_unavailable")
			return
		}
		s.json(w, 200, p)
	}))
	s.mux.HandleFunc("PUT /api/v1/inbox/archive/files/policy", s.protected(func(w http.ResponseWriter, r *http.Request) {
		var p archive.Policy
		if !s.decode(w, r, &p) {
			return
		}
		if err := s.Collection.Archive.SetPolicy(p); err != nil {
			s.bad(w, 400, "invalid_archive_policy")
			return
		}
		s.json(w, 200, p)
	}))
	s.mux.HandleFunc("GET /api/v1/inbox/archive/backup", s.protected(func(w http.ResponseWriter, r *http.Request) {
		job, err := s.Collection.Archive.Job()
		if err != nil {
			s.bad(w, 500, "backup_status_unavailable")
			return
		}
		s.json(w, 200, job)
	}))
	s.mux.HandleFunc("POST /api/v1/inbox/archive/backup", s.protected(func(w http.ResponseWriter, r *http.Request) {
		var scope archive.Scope
		if !s.decode(w, r, &scope) {
			return
		}
		job, err := s.Collection.Archive.StartExport(scope)
		if err != nil {
			s.bad(w, 409, "archive_export_unavailable")
			return
		}
		s.json(w, 202, job)
	}))
	s.mux.HandleFunc("POST /api/v1/inbox/archive/backup/cancel", s.protected(func(w http.ResponseWriter, r *http.Request) {
		s.Collection.Archive.Cancel()
		s.json(w, 200, map[string]bool{"cancelling": true})
	}))
	s.mux.HandleFunc("GET /api/v1/inbox/archive/backup/{job}/file", s.protected(func(w http.ResponseWriter, r *http.Request) {
		f, err := s.Collection.Archive.ExportFile(r.PathValue("job"))
		if err != nil {
			s.bad(w, 409, "backup_not_complete")
			return
		}
		defer f.Close()
		// Large local exports must not inherit the normal 30-second API write deadline.
		// Each write still has a bounded stall deadline via the response controller.
		controller := http.NewResponseController(w)
		_ = controller.SetWriteDeadline(time.Time{})
		job, _ := s.Collection.Archive.Job()
		name := "messages-archive.sma"
		if job != nil && job.Scope.Plaintext {
			name = "messages-export.tar"
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", "attachment; filename="+name)
		http.ServeContent(deadlineResponse{ResponseWriter: w, controller: controller}, r, name, time.Time{}, f)
	}))
}

type deadlineResponse struct {
	http.ResponseWriter
	controller *http.ResponseController
}

func (w deadlineResponse) Write(b []byte) (int, error) {
	_ = w.controller.SetWriteDeadline(time.Now().Add(30 * time.Second))
	return w.ResponseWriter.Write(b)
}
