package httpapi

import (
	"net/http"
	"smsbot-server/internal/bot"
	"smsbot-server/internal/transport"
	"strconv"
)

func (s *Server) inboxRoutes() {
	s.mux.HandleFunc("GET /api/v1/inbox/settings", s.protected(s.inboxSettings))
	s.mux.HandleFunc("PUT /api/v1/inbox/settings", s.protected(s.saveInboxSettings))
	s.mux.HandleFunc("GET /api/v1/inbox/conversations", s.protected(s.inboxConversations))
	s.mux.HandleFunc("GET /api/v1/inbox/conversations/{id}/history", s.protected(s.inboxHistory))
	s.mux.HandleFunc("POST /api/v1/inbox/conversations/{id}/history/import", s.protected(s.inboxImport))
	s.mux.HandleFunc("POST /api/v1/inbox/conversations/{id}/compose", s.protected(s.inboxCompose))
}
func (s *Server) inboxSettings(w http.ResponseWriter, r *http.Request) {
	v, err := s.Engine.InboxSettings()
	if err != nil {
		s.bad(w, 500, "settings_unavailable")
		return
	}
	s.json(w, 200, v)
}
func (s *Server) saveInboxSettings(w http.ResponseWriter, r *http.Request) {
	var v bot.InboxSettings
	if !s.decode(w, r, &v) {
		return
	}
	if err := s.Engine.SetInboxSettings(r.Context(), v); err != nil {
		s.bad(w, 409, err.Error())
		return
	}
	s.inboxSettings(w, r)
}
func (s *Server) inboxConversations(w http.ResponseWriter, r *http.Request) {
	source, ok := s.Engine.Transport.(transport.ConversationPageSource)
	if !ok {
		s.bad(w, 503, "discovery_unavailable")
		return
	}
	p, err := source.DiscoverConversations(r.Context(), r.URL.Query().Get("cursor"))
	if err != nil {
		s.bad(w, 503, "discovery_unavailable")
		return
	}
	for i := range p.Conversations {
		p.Conversations[i].Enrolled = s.Engine.Store.Enrolled(p.Conversations[i].ID, p.Conversations[i].ParticipantID)
	}
	s.json(w, 200, p)
}
func (s *Server) inboxHistory(w http.ResponseWriter, r *http.Request) {
	limit := 50
	var err error
	if q := r.URL.Query().Get("limit"); q != "" {
		limit, err = strconv.Atoi(q)
		if err != nil {
			s.bad(w, 400, "invalid_limit")
			return
		}
	}
	p, err := s.Engine.InboxPage(r.Context(), r.PathValue("id"), r.URL.Query().Get("cursor"), r.URL.Query().Get("search"), limit)
	if err != nil {
		s.bad(w, 409, "history_unavailable_or_access_disabled")
		return
	}
	s.json(w, 200, p)
}
func (s *Server) inboxImport(w http.ResponseWriter, r *http.Request) {
	var v struct {
		Cursor string `json:"cursor"`
	}
	if !s.decode(w, r, &v) {
		return
	}
	p, err := s.Engine.ImportInboxHistory(r.Context(), r.PathValue("id"), v.Cursor)
	if err != nil {
		s.bad(w, 409, "history_unavailable_or_access_disabled")
		return
	}
	s.json(w, 200, p)
}
func (s *Server) inboxCompose(w http.ResponseWriter, r *http.Request) {
	var v struct {
		Text      string `json:"text"`
		RequestID string `json:"request_id"`
	}
	if !s.decode(w, r, &v) {
		return
	}
	id, err := s.Engine.ComposeInbox(r.Context(), r.PathValue("id"), v.Text, v.RequestID)
	if err != nil {
		s.bad(w, 409, "compose_unavailable")
		return
	}
	s.json(w, 202, map[string]any{"id": id, "queued": true})
}
