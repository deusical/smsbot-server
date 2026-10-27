package httpapi

import (
	"embed"
	"encoding/json"
	"io"
	"io/fs"
	"math"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/deusical/smsbot-server/internal/bot"
	"github.com/deusical/smsbot-server/internal/collection"
	"github.com/deusical/smsbot-server/internal/media"
	"github.com/deusical/smsbot-server/internal/notify"
	"github.com/deusical/smsbot-server/internal/store"
	"github.com/deusical/smsbot-server/internal/transport"
)

//go:embed all:assets
var assets embed.FS

func init() {
	// Not every OS MIME table knows the web app manifest type, and nosniff is on.
	_ = mime.AddExtensionType(".webmanifest", "application/manifest+json")
}

type Server struct {
	Engine     *bot.Engine
	Collection *collection.Service
	Store      *store.Store
	Transport  transport.Transport
	Origin     string
	Fixture    bool
	// Notify is nil until the service wires it; its routes answer 503 until then.
	Notify *notify.Service
	mux    *http.ServeMux
}

func New(e *bot.Engine, c *collection.Service, origin string, fixture bool) *Server {
	s := &Server{Engine: e, Collection: c, Store: e.Store, Transport: e.Transport, Origin: origin, Fixture: fixture, mux: http.NewServeMux()}
	s.featureRoutes()
	s.notificationRoutes()
	s.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { s.json(w, 200, map[string]string{"status": "running"}) })
	s.mux.HandleFunc("POST /api/v1/login", s.login)
	s.mux.HandleFunc("POST /api/v1/logout", s.protected(s.logout))
	s.mux.HandleFunc("POST /api/v1/sessions/revoke", s.protected(s.revokeSessions))
	s.mux.HandleFunc("GET /api/v1/state", s.protected(s.state))
	s.mux.HandleFunc("POST /api/v1/pause", s.protected(s.pause))
	s.mux.HandleFunc("POST /api/v1/enrollment", s.protected(s.enroll))
	s.mux.HandleFunc("DELETE /api/v1/enrollment/{id}", s.protected(s.unenroll))
	s.mux.HandleFunc("GET /api/v1/activity", s.protected(s.activity))
	s.mux.HandleFunc("POST /api/v1/fixture/incoming", s.protected(s.incoming))
	s.mux.HandleFunc("POST /api/v1/fixture/typing", s.protected(s.fixtureTyping))
	sub, _ := fs.Sub(assets, "assets")
	s.mux.Handle("/", http.FileServer(http.FS(sub)))
	return s
}
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Exact Host protects loopback endpoints against DNS rebinding.
	origin, _ := url.Parse(s.Origin)
	if r.Host != origin.Host {
		http.Error(w, "invalid host", http.StatusForbidden)
		return
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self' data: blob:; frame-ancestors 'none'; base-uri 'none'")
	switch {
	case strings.HasPrefix(r.URL.Path, "/api/"):
		w.Header().Set("Cache-Control", "no-store")
	case strings.HasPrefix(r.URL.Path, "/assets/"):
		// Vite fingerprints bundle names, so they never change in place.
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	default:
		w.Header().Set("Cache-Control", "no-cache")
	}
	if r.Method != "GET" && r.Method != "HEAD" && r.Header.Get("Origin") != "" && r.Header.Get("Origin") != s.Origin {
		s.json(w, 403, map[string]string{"error": "invalid_origin"})
		return
	}
	limit := int64(32 * 1024)
	if (strings.HasPrefix(r.URL.Path, "/api/v1/inbox/conversations/") || strings.HasPrefix(r.URL.Path, "/api/v1/conversations/")) && strings.HasSuffix(r.URL.Path, "/compose") {
		limit = 128 * 1024
		if strings.HasPrefix(r.URL.Path, "/api/v1/inbox/") {
			limit = math.MaxInt64
		}
	}
	if s.Fixture && r.URL.Path == "/api/v1/fixture/incoming" {
		limit = math.MaxInt64
	}
	if r.URL.Path == "/api/v1/inbox/settings" {
		limit = 256 * 1024
	}
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
		// Only owner browser sessions are accepted; bearer credentials are never honoured.
		c, e := r.Cookie("smsbot_session")
		if e != nil {
			s.bad(w, 401, "authentication_required")
			return
		}
		expires, ok := s.Store.SessionExpiry(c.Value)
		if !ok {
			s.bad(w, 401, "authentication_required")
			return
		}
		// Remembered sessions never expire server-side; renew the cookie on
		// reads so the browser's own cap (about 400 days) slides while it is
		// in use. Writes such as sign-out must not race a renewal.
		if expires == 0 && r.Method == "GET" {
			s.sessionCookie(w, c.Value, rememberedCookieAge)
		}
		if r.Method != "GET" && r.Header.Get("Origin") != s.Origin {
			s.bad(w, 403, "origin_required")
			return
		}
		fn(w, r)
	}
}
func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Token    string `json:"token"`
		Remember bool   `json:"remember"`
	}
	if !s.decode(w, r, &in) {
		return
	}
	// Tolerate whitespace copied along with the credential file contents.
	if !s.Store.Credential(strings.TrimSpace(in.Token), "owner") {
		s.bad(w, 401, "invalid_credentials")
		return
	}
	// Remembered sessions last until signed out or revoked everywhere.
	token := store.ID() + store.ID()
	var expires int64
	age := int(sessionLifetime.Seconds())
	if in.Remember {
		age = rememberedCookieAge
	} else {
		expires = time.Now().Add(sessionLifetime).Unix()
	}
	if e := s.Store.AddCredential(token, "Browser", "session", expires); e != nil {
		s.bad(w, 500, "storage_error")
		return
	}
	s.sessionCookie(w, token, age)
	s.json(w, 200, map[string]bool{"authenticated": true})
}

const (
	sessionLifetime     = 12 * time.Hour
	rememberedCookieAge = 400 * 24 * 3600
)

func (s *Server) sessionCookie(w http.ResponseWriter, token string, age int) {
	http.SetCookie(w, &http.Cookie{Name: "smsbot_session", Value: token, Path: "/api/v1", HttpOnly: true, Secure: strings.HasPrefix(s.Origin, "https://"), SameSite: http.SameSiteStrictMode, MaxAge: age})
}
func (s *Server) revokeSessions(w http.ResponseWriter, r *http.Request) {
	if e := s.Store.RevokeSessions(); e != nil {
		s.bad(w, 500, "storage_error")
		return
	}
	s.sessionCookie(w, "", -1)
	s.json(w, 200, map[string]bool{"authenticated": false})
}
func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if c, e := r.Cookie("smsbot_session"); e == nil {
		if err := s.Store.RevokeSession(c.Value); err != nil {
			s.bad(w, 500, "storage_error")
			return
		}
	}
	s.sessionCookie(w, "", -1)
	s.json(w, 200, map[string]bool{"authenticated": false})
}
func (s *Server) state(w http.ResponseWriter, r *http.Request) {
	p, e := s.Store.Paused()
	if e != nil {
		s.bad(w, 500, "storage_error")
		return
	}
	value := map[string]any{"api_version": 1, "paused": p, "transport_ready": s.Transport.Ready(), "transport": map[bool]string{true: "fixture", false: "google_messages"}[s.Fixture], "fixture": s.Fixture, "capabilities": []string{"fixed_commands", "enrollment", "activity", "encrypted_inbox", "configuration", "models", "history", "history_import", "playground", "approvals", "manual_compose", "recipes", "ai_commands", "full_inbox", "attachments", "archive_history", "archive_refresh", "archive_files", "archive_backup", "archive_stats", "conversation_sync", "read_receipts", "typing", "message_actions", "rcs_replies", "rcs_reactions", "push_notifications"}}
	if reporter, ok := s.Transport.(transport.ConnectionReporter); ok {
		connection := reporter.ConnectionStatus()
		value["connection_state"] = connection.State
		if connection.RetryAt != "" {
			value["connection_retry_at"] = connection.RetryAt
		}
	}

	if reporter, ok := s.Transport.(transport.PhoneRequestReporter); ok {
		value["phone_requests"] = reporter.PhoneRequests()
	}

	if reporter, ok := s.Transport.(transport.PhonePurposeReporter); ok {
		value["phone_request_purposes"] = reporter.PhoneRequestPurposes()
		value["phone_request_coverage"] = "adapter_attempts_only"
	}
	if reporter, ok := s.Transport.(transport.PhoneRelayReporter); ok {
		value["phone_relay_requests"] = reporter.PhoneRelayRequests()
	}
	gaps, unknown := s.Collection.CaptureGapStatus()
	active := unknown
	for _, g := range gaps {
		if !store.GapSettled(g) {
			active = true
		}
	}
	value["inbox_capture_gap"] = active
	value["conversations_revision"] = s.Collection.ConversationRevision()
	value["inbox_capture_gaps"] = gaps
	value["inbox_capture_gap_unknown"] = unknown
	s.json(w, 200, value)
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
	if !*in.Paused && !s.Transport.Ready() {
		s.bad(w, 503, "transport_unavailable")
		return
	}
	if e := s.Engine.Pause(*in.Paused); e != nil {
		s.bad(w, 500, "storage_error")
		return
	}
	s.state(w, r)
}
func (s *Server) enroll(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ID   string `json:"conversation_id"`
		Peer string `json:"participant_id"`
	}
	if !s.decode(w, r, &in) {
		return
	}
	list, e := s.Transport.Conversations(r.Context())
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
	jobs, e := s.Store.Jobs()
	if e != nil {
		s.bad(w, 500, "storage_error")
		return
	}
	s.json(w, 200, jobs)
}
func (s *Server) incoming(w http.ResponseWriter, r *http.Request) {
	if !s.Fixture {
		s.bad(w, 404, "not_found")
		return
	}
	var input struct {
		transport.Incoming
		Attachments []media.Upload `json:"attachments"`
	}
	if !s.decode(w, r, &input) {
		return
	}
	m := input.Incoming
	settings, settingsErr := s.Collection.Settings()
	if settingsErr != nil {
		s.bad(w, 500, "settings_unavailable")
		return
	}
	files, fileErr := media.PrepareWithLimits(input.Attachments, settings.FileLimits())
	if fileErr != nil {
		s.bad(w, 400, "invalid_attachments")
		return
	}
	m.Attachments = files
	if m.ID == "" {
		m.ID = store.ID()
	}
	if m.At.IsZero() {
		m.At = time.Now().UTC()
	}
	accepted, e := s.Engine.Receive(r.Context(), m)
	if e != nil {
		s.bad(w, 500, "processing_error")
		return
	}
	s.json(w, 200, map[string]any{"accepted": accepted, "id": m.ID})
}

func (s *Server) fixtureTyping(w http.ResponseWriter, r *http.Request) {
	fixture, ok := s.Transport.(*transport.Fixture)
	if !s.Fixture || !ok {
		s.bad(w, 404, "not_found")
		return
	}
	var v struct {
		ConversationID string `json:"conversation_id"`
		Typing         bool   `json:"typing"`
	}
	if !s.decode(w, r, &v) {
		return
	}
	fixture.SetTyping(v.ConversationID, v.Typing)
	s.json(w, 200, map[string]bool{"ok": true})
}
