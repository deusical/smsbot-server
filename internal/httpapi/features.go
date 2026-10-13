package httpapi

import (
	"encoding/json"
	"net/http"
	"smsbot-server/internal/bot"
	"strconv"
)

func (s *Server) featureRoutes() {
	s.mux.HandleFunc("GET /api/v1/config", s.protected(s.config))
	s.mux.HandleFunc("PUT /api/v1/config", s.protected(s.saveConfig))
	s.mux.HandleFunc("GET /api/v1/models", s.protected(s.models))
	s.mux.HandleFunc("POST /api/v1/models/refresh", s.protected(s.refreshModels))
	s.mux.HandleFunc("GET /api/v1/provider-key", s.protected(s.providerKey))
	s.mux.HandleFunc("PUT /api/v1/provider-key", s.protected(s.saveProviderKey))
	s.mux.HandleFunc("GET /api/v1/compatible-provider", s.protected(s.compatibleProvider))
	s.mux.HandleFunc("PUT /api/v1/compatible-provider", s.protected(s.saveCompatibleProvider))
	s.mux.HandleFunc("POST /api/v1/models/refresh-compatible", s.protected(s.refreshCompatibleModels))
	s.mux.HandleFunc("POST /api/v1/jobs/{id}/approve", s.protected(s.approve))
	s.mux.HandleFunc("POST /api/v1/jobs/{id}/cancel", s.protected(s.cancel))
	s.mux.HandleFunc("POST /api/v1/playground", s.protected(s.playground))
	s.mux.HandleFunc("GET /api/v1/conversations/{id}/history", s.protected(s.history))
	s.mux.HandleFunc("POST /api/v1/conversations/{id}/compose", s.protected(s.compose))
	s.mux.HandleFunc("POST /api/v1/conversations/{id}/history/import", s.protected(s.importHistory))
	s.mux.HandleFunc("GET /api/v1/recipes/export", s.protected(s.exportRecipe))
	s.mux.HandleFunc("POST /api/v1/recipes/import", s.protected(s.importRecipe))
}
func (s *Server) importHistory(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Cursor string `json:"cursor"`
	}
	if !s.decode(w, r, &in) {
		return
	}
	result, err := s.Engine.ImportHistory(r.Context(), r.PathValue("id"), in.Cursor)
	if err != nil {
		s.bad(w, 409, "history_import_unavailable")
		return
	}
	s.json(w, 200, result)
}
func (s *Server) config(w http.ResponseWriter, r *http.Request) {
	c, err := s.Engine.Configuration()
	if err != nil {
		s.bad(w, 500, "configuration_unavailable")
		return
	}
	s.json(w, 200, c)
}
func (s *Server) saveConfig(w http.ResponseWriter, r *http.Request) {
	var c bot.Configuration
	if !s.decode(w, r, &c) {
		return
	}
	if err := s.Engine.SetConfiguration(c); err != nil {
		s.bad(w, 400, "invalid_configuration")
		return
	}
	s.config(w, r)
}
func (s *Server) models(w http.ResponseWriter, r *http.Request) {
	m, err := s.Engine.Models()
	if err != nil {
		s.bad(w, 500, "catalog_unavailable")
		return
	}
	s.json(w, 200, m)
}
func (s *Server) refreshModels(w http.ResponseWriter, r *http.Request) {
	if s.Fixture {
		s.bad(w, 409, "fixture_catalog_is_offline")
		return
	}
	m, err := s.Engine.RefreshModels(r.Context())
	if err != nil {
		s.bad(w, 502, "catalog_refresh_failed")
		return
	}
	s.json(w, 200, m)
}
func (s *Server) providerKey(w http.ResponseWriter, r *http.Request) {
	s.json(w, 200, map[string]bool{"configured": s.Engine.ProviderConfigured()})
}
func (s *Server) saveProviderKey(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Key string `json:"key"`
	}
	if !s.decode(w, r, &in) {
		return
	}
	if err := s.Engine.SetProviderKey(in.Key); err != nil {
		s.bad(w, 400, "invalid_provider_key")
		return
	}
	s.json(w, 200, map[string]bool{"configured": s.Engine.ProviderConfigured()})
}
func (s *Server) compatibleProvider(w http.ResponseWriter, r *http.Request) {
	status, err := s.Engine.CompatibleProviderStatus()
	if err != nil {
		s.bad(w, 500, "provider_configuration_unavailable")
		return
	}
	s.json(w, 200, status)
}
func (s *Server) saveCompatibleProvider(w http.ResponseWriter, r *http.Request) {
	var in struct {
		BaseURL  string `json:"base_url"`
		Key      string `json:"key"`
		ClearKey bool   `json:"clear_key"`
	}
	if !s.decode(w, r, &in) {
		return
	}
	if err := s.Engine.SetCompatibleProvider(in.BaseURL, in.Key, in.ClearKey); err != nil {
		s.bad(w, 400, "invalid_provider_configuration")
		return
	}
	s.compatibleProvider(w, r)
}
func (s *Server) refreshCompatibleModels(w http.ResponseWriter, r *http.Request) {
	if s.Fixture {
		s.bad(w, 409, "fixture_catalog_is_offline")
		return
	}
	models, err := s.Engine.RefreshCompatibleModels(r.Context())
	if err != nil {
		s.bad(w, 502, "compatible_catalog_refresh_failed")
		return
	}
	s.json(w, 200, models)
}
func (s *Server) approve(w http.ResponseWriter, r *http.Request) {
	if err := s.Engine.Approve(r.PathValue("id")); err != nil {
		s.bad(w, 409, "approval_unavailable_or_expired")
		return
	}
	s.json(w, 200, map[string]bool{"approved": true})
}
func (s *Server) cancel(w http.ResponseWriter, r *http.Request) {
	if err := s.Engine.Cancel(r.PathValue("id")); err != nil {
		s.bad(w, 500, "cancel_failed")
		return
	}
	s.json(w, 200, map[string]bool{"cancelled": true})
}
func (s *Server) playground(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Command string `json:"command"`
		Input   string `json:"input"`
	}
	if !s.decode(w, r, &in) {
		return
	}
	if len(in.Input) > 12000 {
		s.bad(w, 400, "input_too_long")
		return
	}
	result, err := s.Engine.Playground(in.Command, in.Input)
	if err != nil {
		s.bad(w, 400, "command_unavailable")
		return
	}
	s.json(w, 200, result)
}
func (s *Server) enrolled(id string) bool {
	var n int
	return s.Engine.Store.DB.QueryRow("SELECT count(*) FROM enrollment WHERE conversation=?", id).Scan(&n) == nil && n == 1
}
func (s *Server) history(w http.ResponseWriter, r *http.Request) {
	if !s.enrolled(r.PathValue("id")) {
		s.bad(w, 403, "enrollment_required")
		return
	}
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil {
			s.bad(w, 400, "invalid_limit")
			return
		}
		limit = value
	}
	page, err := s.Engine.Store.MessagePage(r.PathValue("id"), r.URL.Query().Get("cursor"), r.URL.Query().Get("search"), limit)
	if err != nil {
		s.bad(w, 400, "history_unavailable_or_invalid_query")
		return
	}
	s.json(w, 200, page)
}
func (s *Server) compose(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Text string `json:"text"`
	}
	if !s.decode(w, r, &in) {
		return
	}
	id, err := s.Engine.Compose(r.Context(), r.PathValue("id"), in.Text)
	if err != nil {
		s.bad(w, 409, "compose_unavailable")
		return
	}
	s.json(w, 202, map[string]any{"id": id, "queued": true})
}
func (s *Server) exportRecipe(w http.ResponseWriter, r *http.Request) {
	c, err := s.Engine.Configuration()
	if err != nil {
		s.bad(w, 500, "configuration_unavailable")
		return
	}
	s.json(w, 200, bot.CleanRecipe(c))
}
func (s *Server) importRecipe(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Configuration json.RawMessage `json:"configuration"`
	}
	if !s.decode(w, r, &in) {
		return
	}
	if err := s.Engine.ImportRecipeJSON(in.Configuration); err != nil {
		s.bad(w, 400, "invalid_recipe")
		return
	}
	s.config(w, r)
}
