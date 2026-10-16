package httpapi

import (
	"context"
	"net/http"
	"smsbot-server/internal/store"
	"time"
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
		a, err := s.Engine.RequestArchiveRefresh(r.Context(), r.PathValue("id"), v.Since, v.Until)
		if err != nil {
			s.bad(w, 409, "refresh_unavailable_or_invalid_range")
			return
		}
		s.json(w, 202, a)
	}))
	s.mux.HandleFunc("GET /api/v1/inbox/archive", s.protected(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		a, err := s.Engine.Store.ArchivesContext(ctx)
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
		a, err := s.Engine.RequestArchive(r.Context(), r.PathValue("id"), v.Since, v.Cancel)
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
		if _, err := s.Engine.Store.Archive(conv); err != nil {
			s.bad(w, 409, "archive_unavailable")
			return
		}
		p, err := s.Engine.Store.Changes(conv, r.URL.Query().Get("after"), "")
		if err != nil {
			s.bad(w, 400, "invalid_change_cursor")
			return
		}
		s.json(w, 200, p)
	}))
	s.mux.HandleFunc("GET /api/v1/inbox/conversations/{id}/revisions/{message}", s.protected(func(w http.ResponseWriter, r *http.Request) {
		conv, id := r.PathValue("id"), r.PathValue("message")
		if _, err := s.Engine.Store.Archive(conv); err != nil {
			s.bad(w, 404, "archive_unavailable")
			return
		}
		var count int
		if err := s.Engine.Store.DB.QueryRow("SELECT count(*) FROM messages WHERE conversation=? AND id=?", conv, id).Scan(&count); err != nil || count != 1 {
			s.bad(w, 404, "message_unavailable")
			return
		}
		revisions := []map[string]any{}
		rows, err := s.Engine.Store.DB.Query("SELECT revision,payload,observed FROM message_revisions WHERE message=? ORDER BY revision", id)
		if err != nil {
			s.bad(w, 500, "revisions_unavailable")
			return
		}
		defer rows.Close()
		for rows.Next() {
			var n int
			var enc, at string
			if rows.Scan(&n, &enc, &at) != nil {
				s.bad(w, 500, "revisions_unavailable")
				return
			}
			b, err := s.Engine.Store.Vault.Open("message:"+id, enc)
			if err != nil {
				s.bad(w, 500, "revisions_unavailable")
				return
			}
			revisions = append(revisions, map[string]any{"revision": n, "text": string(b), "observed_at": at})
		}
		if rows.Err() != nil {
			s.bad(w, 500, "revisions_unavailable")
			return
		}
		s.json(w, 200, revisions)
	}))
}

// LocalArchivePage never calls a transport and never performs destructive global
// legacy retention. Archive expiry uses the archive's separate policy.
func (s *Server) localArchivePage(conv, cursor, search string, limit int) (store.InboxPage, error) {
	if err := s.Engine.Store.ArchiveRetention(); err != nil {
		return store.InboxPage{}, err
	}
	return s.Engine.Store.OwnerMessagePage(conv, cursor, search, limit)
}
