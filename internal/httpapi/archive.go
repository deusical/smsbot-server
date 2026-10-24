package httpapi

import (
	"context"
	"net/http"
	"time"

	"github.com/deusical/smsbot-server/internal/store"
)

func (s *Server) archiveRoutes() {
	s.archiveBackupRoutes()
	s.mux.HandleFunc("POST /api/v1/inbox/conversations/{id}/refresh", s.protected(func(w http.ResponseWriter, r *http.Request) {
		var v struct {
			Since string `json:"since"`
			Until string `json:"until"`
		}
		if !s.decode(w, r, &v) {
			return
		}
		a, err := s.Collection.RequestArchiveRefresh(r.Context(), r.PathValue("id"), v.Since, v.Until)
		if err != nil {
			s.bad(w, 409, "refresh_unavailable_or_invalid_range")
			return
		}
		s.json(w, 202, a)
	}))
	s.mux.HandleFunc("GET /api/v1/inbox/archive", s.protected(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		a, err := s.Store.ArchivesContext(ctx)
		if err != nil {
			s.bad(w, 500, "archive_unavailable")
			return
		}
		s.json(w, 200, a)
	}))
	s.mux.HandleFunc("POST /api/v1/inbox/conversations/{id}/sync", s.protected(func(w http.ResponseWriter, r *http.Request) {
		var v struct {
			Since  string `json:"since"`
			Cancel bool   `json:"cancel"`
		}
		if !s.decode(w, r, &v) {
			return
		}
		a, err := s.Collection.RequestArchive(r.Context(), r.PathValue("id"), v.Since, v.Cancel)
		if err != nil {
			s.bad(w, 409, "sync_unavailable_or_invalid_range")
			return
		}
		s.json(w, 200, a)
	}))
	s.mux.HandleFunc("GET /api/v1/inbox/conversations/{id}/changes", s.protected(func(w http.ResponseWriter, r *http.Request) {
		// A persisted archive is an owner-readable local artifact even if syncing is
		// stopped. Access never grants collection, command enrollment or send rights.
		conv := r.PathValue("id")
		if _, err := s.Store.Archive(conv); err != nil {
			s.bad(w, 409, "archive_unavailable")
			return
		}
		p, err := s.Store.Changes(conv, r.URL.Query().Get("after"), "")
		if err != nil {
			s.bad(w, 400, "invalid_change_cursor")
			return
		}
		s.json(w, 200, p)
	}))
	s.mux.HandleFunc("GET /api/v1/inbox/conversations/{id}/revisions/{message}", s.protected(func(w http.ResponseWriter, r *http.Request) {
		conv, id := r.PathValue("id"), r.PathValue("message")
		if _, err := s.Store.Archive(conv); err != nil {
			s.bad(w, 404, "archive_unavailable")
			return
		}
		if exists, err := s.Store.MessageExists(conv, id); err != nil || !exists {
			s.bad(w, 404, "message_unavailable")
			return
		}
		revisions, err := s.Store.MessageRevisions(id)
		if err != nil {
			s.bad(w, 500, "revisions_unavailable")
			return
		}
		s.json(w, 200, revisions)
	}))
}

// LocalArchivePage never calls a transport and never performs destructive global
// legacy retention. Archive expiry uses the archive's separate policy.
func (s *Server) localArchivePage(conv, cursor, search string, limit int) (store.InboxPage, error) {
	if err := s.Store.ArchiveRetention(); err != nil {
		return store.InboxPage{}, err
	}
	return s.Store.OwnerMessagePage(conv, cursor, search, limit)
}
