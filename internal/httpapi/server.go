package httpapi

import (
	"context"
	"embed"
	"encoding/json"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"net/url"
	"smsbot-server/internal/bot"
	"smsbot-server/internal/store"
	"smsbot-server/internal/transport"
	"strings"
	"sync"
	"time"
)

//go:embed all:assets
var assets embed.FS

type Server struct {
	Engine     *bot.Engine
	Origin     string
	Fixture    bool
	mux        *http.ServeMux
	failuresMu sync.Mutex
	failures   int
	cooldown   time.Time
}

func New(e *bot.Engine, origin string, fixture bool) *Server {
	s := &Server{Engine: e, Origin: origin, Fixture: fixture, mux: http.NewServeMux()}
	s.featureRoutes()
	s.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { s.json(w, 200, map[string]string{"status": "running"}) })
	s.mux.HandleFunc("POST /api/v1/login", s.login)
	s.mux.HandleFunc("POST /api/v1/logout", s.protected(s.logout))
	s.mux.HandleFunc("GET /api/v1/state", s.protected(s.state))
	s.mux.HandleFunc("POST /api/v1/pause", s.protected(s.pause))
	s.mux.HandleFunc("GET /api/v1/conversations", s.protected(s.conversations))
	s.mux.HandleFunc("POST /api/v1/enrollment", s.protected(s.enroll))
	s.mux.HandleFunc("DELETE /api/v1/enrollment/{id}", s.protected(s.unenroll))
	s.mux.HandleFunc("GET /api/v1/activity", s.protected(s.activity))
	s.mux.HandleFunc("GET /api/v1/conversations/{id}/messages", s.protected(s.messages))
	s.mux.HandleFunc("POST /api/v1/clients", s.protected(s.client))
	s.mux.HandleFunc("DELETE /api/v1/clients/{id}", s.protected(s.revoke))
	s.mux.HandleFunc("GET /api/v1/clients", s.protected(s.clients))
	s.mux.HandleFunc("POST /api/v1/fixture/incoming", s.protected(s.incoming))
	sub, _ := fs.Sub(assets, "assets")
	s.mux.Handle("/", http.FileServer(http.FS(sub)))
	return s
}
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Exact Host protects loopback endpoints against DNS rebinding.
	origin, _ := url.Parse(s.Origin)
	if r.Host != origin.Host {
		http.Error(w, "invalid host", 403)
		return
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self' data:; frame-ancestors 'none'; base-uri 'none'")
	if strings.HasPrefix(r.URL.Path, "/api/") {
		w.Header().Set("Cache-Control", "no-store")
	}
	if r.Method != "GET" && r.Method != "HEAD" && r.Header.Get("Origin") != "" && r.Header.Get("Origin") != s.Origin {
		s.json(w, 403, map[string]string{"error": "invalid_origin"})
		return
	}
	limit := int64(32 * 1024)
	if r.URL.Path == "/api/v1/config" || r.URL.Path == "/api/v1/recipes/import" {
		limit = 2 * 1024 * 1024
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	s.mux.ServeHTTP(w, r)
}
func (s *Server) json(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func (s *Server) bad(w http.ResponseWriter, status int, code string) {
	s.json(w, status, map[string]string{"error": code})
}
func (s *Server) decode(w http.ResponseWriter, r *http.Request, v any) bool {
	mediaType, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" || params["charset"] != "" && !strings.EqualFold(params["charset"], "utf-8") {
		s.bad(w, 415, "json_required")
		return false
	}
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if d.Decode(v) != nil {
		s.bad(w, 400, "invalid_request")
		return false
	}
	if d.Decode(&struct{}{}) != io.EOF {
		s.bad(w, 400, "invalid_request")
		return false
	}
	return true
}
func (s *Server) protected(fn http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		isClient := r.Header.Get("Authorization") != "" && s.Engine.Store.Credential(token, "client")
		if r.Header.Get("Authorization") != "" && !isClient {
			s.bad(w, 401, "authentication_required")
			return
		}
		if !isClient {
			c, e := r.Cookie("smsbot_session")
			if e != nil || !s.Engine.Store.Credential(c.Value, "session") {
				s.bad(w, 401, "authentication_required")
				return
			}
			if r.Method != "GET" && r.Header.Get("Origin") != s.Origin {
				s.bad(w, 403, "origin_required")
				return
			}
		}
		// Client credentials can control the bot, but cannot grant/revoke other credentials.
		if isClient && (strings.HasPrefix(r.URL.Path, "/api/v1/clients") || r.URL.Path == "/api/v1/provider-key" || r.URL.Path == "/api/v1/compatible-provider" || r.URL.Path == "/api/v1/models/refresh-compatible") {
			s.bad(w, 403, "owner_required")
			return
		}
		fn(w, r)
	}
}
func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Token string `json:"token"`
	}
	if !s.decode(w, r, &in) {
		return
	}
	s.failuresMu.Lock()
	defer s.failuresMu.Unlock()
	if time.Now().Before(s.cooldown) {
		s.bad(w, 429, "try_later")
		return
	}
	if !s.Engine.Store.Credential(in.Token, "owner") {
		s.failures++
		if s.failures >= 5 {
			s.cooldown = time.Now().Add(time.Minute)
			s.failures = 0
		}
		s.bad(w, 401, "invalid_credentials")
		return
	}
	s.failures = 0
	token := store.ID() + store.ID()
	if e := s.Engine.Store.AddCredential(token, "Browser", "session", time.Now().Add(12*time.Hour).Unix()); e != nil {
		s.bad(w, 500, "storage_error")
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "smsbot_session", Value: token, Path: "/api/v1", HttpOnly: true, Secure: strings.HasPrefix(s.Origin, "https://"), SameSite: http.SameSiteStrictMode, MaxAge: 43200})
	s.json(w, 200, map[string]bool{"authenticated": true})
}
func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if c, e := r.Cookie("smsbot_session"); e == nil {
		_, err := s.Engine.Store.DB.Exec("DELETE FROM credentials WHERE hash=? AND kind='session'", store.Hash(c.Value))
		if err != nil {
			s.bad(w, 500, "storage_error")
			return
		}
	}
	http.SetCookie(w, &http.Cookie{Name: "smsbot_session", Path: "/api/v1", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode})
	s.json(w, 200, map[string]bool{"authenticated": false})
}
func (s *Server) state(w http.ResponseWriter, r *http.Request) {
	p, e := s.Engine.Store.Paused()
	if e != nil {
		s.bad(w, 500, "storage_error")
		return
	}
	s.json(w, 200, map[string]any{"api_version": 1, "paused": p, "transport_ready": s.Engine.Transport.Ready(), "transport": map[bool]string{true: "fixture", false: "google_messages"}[s.Fixture], "fixture": s.Fixture, "capabilities": []string{"fixed_commands", "enrollment", "activity", "encrypted_inbox", "configuration", "models", "history", "history_import", "playground", "approvals", "manual_compose", "recipes", "ai_commands"}})
}
func (s *Server) pause(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Paused *bool `json:"paused"`
	}
	if !s.decode(w, r, &in) {
		return
	}
	if in.Paused == nil {
		s.bad(w, 400, "paused_required")
		return
	}
	if !*in.Paused && !s.Engine.Transport.Ready() {
		s.bad(w, 503, "transport_unavailable")
		return
	}
	if e := s.Engine.Pause(*in.Paused); e != nil {
		s.bad(w, 500, "storage_error")
		return
	}
	s.state(w, r)
}
func (s *Server) conversations(w http.ResponseWriter, r *http.Request) {
	list, e := s.Engine.Transport.Conversations(r.Context())
	if e != nil {
		s.bad(w, 503, "transport_unavailable")
		return
	}
	for i := range list {
		list[i].Enrolled = s.Engine.Store.Enrolled(list[i].ID, list[i].ParticipantID)
	}
	s.json(w, 200, list)
}
func (s *Server) enroll(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ID   string `json:"conversation_id"`
		Peer string `json:"participant_id"`
	}
	if !s.decode(w, r, &in) {
		return
	}
	list, e := s.Engine.Transport.Conversations(r.Context())
	if e != nil {
		s.bad(w, 503, "transport_unavailable")
		return
	}
	for _, c := range list {
		if c.ID == in.ID && c.ParticipantID == in.Peer && c.Writable && !c.Group {
			if e = s.Engine.Enroll(c.ID, c.ParticipantID); e != nil {
				s.bad(w, 500, "storage_error")
				return
			}
			s.json(w, 200, map[string]bool{"enrolled": true})
			return
		}
	}
	s.bad(w, 400, "ineligible_conversation")
}
func (s *Server) unenroll(w http.ResponseWriter, r *http.Request) {
	if e := s.Engine.Unenroll(r.PathValue("id")); e != nil {
		s.bad(w, 500, "storage_error")
		return
	}
	s.json(w, 200, map[string]bool{"enrolled": false})
}
func (s *Server) activity(w http.ResponseWriter, r *http.Request) {
	jobs, e := s.Engine.Store.Jobs()
	if e != nil {
		s.bad(w, 500, "storage_error")
		return
	}
	s.json(w, 200, jobs)
}
func (s *Server) messages(w http.ResponseWriter, r *http.Request) {
	var exists int
	if e := s.Engine.Store.DB.QueryRow("SELECT count(*) FROM enrollment WHERE conversation=?", r.PathValue("id")).Scan(&exists); e != nil {
		s.bad(w, 500, "storage_error")
		return
	}
	if exists == 0 {
		s.bad(w, 403, "enrollment_required")
		return
	}
	list, e := s.Engine.Store.Messages(r.PathValue("id"))
	if e != nil {
		s.bad(w, 500, "storage_error")
		return
	}
	s.json(w, 200, list)
}
func (s *Server) client(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Label string `json:"label"`
	}
	if !s.decode(w, r, &in) {
		return
	}
	label := strings.TrimSpace(in.Label)
	if label == "" || len(label) > 80 {
		s.bad(w, 400, "invalid_label")
		return
	}
	token := store.ID() + store.ID()
	if e := s.Engine.Store.AddCredential(token, label, "client", 0); e != nil {
		s.bad(w, 500, "storage_error")
		return
	}
	s.json(w, 201, map[string]string{"id": store.Hash(token), "token": token, "label": label})
}
func (s *Server) revoke(w http.ResponseWriter, r *http.Request) {
	if e := s.Engine.Store.Revoke(r.PathValue("id")); e != nil {
		s.bad(w, 500, "storage_error")
		return
	}
	s.json(w, 200, map[string]bool{"revoked": true})
}
func (s *Server) clients(w http.ResponseWriter, r *http.Request) {
	rows, e := s.Engine.Store.DB.Query("SELECT hash,label FROM credentials WHERE kind='client'")
	if e != nil {
		s.bad(w, 500, "storage_error")
		return
	}
	defer rows.Close()
	out := []map[string]string{}
	for rows.Next() {
		var id, label string
		if e = rows.Scan(&id, &label); e != nil {
			s.bad(w, 500, "storage_error")
			return
		}
		out = append(out, map[string]string{"id": id, "label": label})
	}
	if rows.Err() != nil {
		s.bad(w, 500, "storage_error")
		return
	}
	s.json(w, 200, out)
}
func (s *Server) incoming(w http.ResponseWriter, r *http.Request) {
	if !s.Fixture {
		s.bad(w, 404, "not_found")
		return
	}
	var m transport.Incoming
	if !s.decode(w, r, &m) {
		return
	}
	if m.ID == "" {
		m.ID = store.ID()
	}
	if m.At.IsZero() {
		m.At = time.Now().UTC()
	}
	accepted, e := s.Engine.Accept(r.Context(), m)
	if e != nil {
		s.bad(w, 500, "processing_error")
		return
	}
	s.json(w, 200, map[string]any{"accepted": accepted, "id": m.ID})
}
func Shutdown(ctx context.Context, h *http.Server) error { return h.Shutdown(ctx) }
