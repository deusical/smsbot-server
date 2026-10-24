package httpapi

import (
	"context"
	"net/http"
	"time"
)

func (s *Server) interactionRoutes() {
	s.mux.HandleFunc("GET /api/v1/inbox/conversations/{id}/messages/{message}/actions", s.protected(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		a, err := s.Engine.MessageActions(ctx, r.PathValue("id"), r.PathValue("message"))
		if err != nil {
			s.bad(w, 409, "message_actions_unavailable")
			return
		}
		s.json(w, 200, a)
	}))
	s.mux.HandleFunc("POST /api/v1/inbox/conversations/{id}/messages/{message}/reactions", s.protected(func(w http.ResponseWriter, r *http.Request) {
		var v struct {
			Emoji     string `json:"emoji"`
			Operation string `json:"operation"`
			Version   string `json:"version"`
			RequestID string `json:"request_id"`
		}
		if !s.decode(w, r, &v) {
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		id, err := s.Engine.QueueReaction(ctx, r.PathValue("id"), r.PathValue("message"), v.Emoji, v.Operation, v.Version, v.RequestID)
		if err != nil {
			code := "reaction_unavailable"
			switch err.Error() {
			case "invalid_reaction", "reaction_conflict", "request_id_reused_with_different_action":
				code = err.Error()
			}
			s.bad(w, 409, code)
			return
		}
		s.json(w, 202, map[string]any{"id": id, "queued": true})
	}))
	s.mux.HandleFunc("GET /api/v1/inbox/conversations/{id}/actions/{action}", s.protected(func(w http.ResponseWriter, r *http.Request) {
		v, err := s.Engine.ReactionStatus(r.PathValue("id"), r.PathValue("action"))
		if err != nil {
			s.bad(w, 404, "action_unavailable")
			return
		}
		s.json(w, 200, v)
	}))
	s.mux.HandleFunc("GET /api/v1/inbox/conversations/{id}/messages/{message}/window", s.protected(func(w http.ResponseWriter, r *http.Request) {
		conv := r.PathValue("id")
		if _, err := s.Store.Archive(conv); err != nil {
			s.bad(w, 404, "archive_unavailable")
			return
		}
		p, err := s.Store.MessageWindow(conv, r.PathValue("message"))
		if err != nil {
			s.bad(w, 404, "message_unavailable")
			return
		}
		s.json(w, 200, p)
	}))
}
