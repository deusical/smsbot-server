package httpapi

import (
	"bytes"
	"compress/gzip"
	"embed"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"maps"
	"math"
	"mime"
	"net/http"
	"net/url"
	"strconv"
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
	Notify       *notify.Service
	PhonePairing *PhonePairing
	// Workers is the task workers' configuration; nil answers 503.
	Workers *bot.Tasks
	mux     *http.ServeMux
}

func New(e *bot.Engine, c *collection.Service, origin string, fixture bool) *Server {
	s := &Server{Engine: e, Collection: c, Store: e.Store, Transport: e.Transport, Origin: origin, Fixture: fixture, mux: http.NewServeMux()}
	s.featureRoutes()
	s.notificationRoutes()
	s.pairingRoutes()
	s.workerRoutes()
	s.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { s.json(w, 200, map[string]string{"status": "running"}) })
	s.mux.HandleFunc("POST /api/v1/login", s.login)
	s.mux.HandleFunc("POST /api/v1/logout", s.protected(s.logout))
	s.mux.HandleFunc("POST /api/v1/sessions/revoke", s.protected(s.revokeSessions))
	s.mux.HandleFunc("GET /api/v1/state", s.protected(s.state))
	s.mux.HandleFunc("POST /api/v1/pause", s.protected(s.pause))
	s.mux.HandleFunc("POST /api/v1/enrollment", s.protected(s.enroll))
	s.mux.HandleFunc("DELETE /api/v1/enrollment/{id}", s.protected(s.unenroll))
	s.mux.HandleFunc("PUT /api/v1/enrollment/{id}/members", s.protected(s.setGroupMembers))
	s.mux.HandleFunc("DELETE /api/v1/enrollment/{id}/members/{member}", s.protected(s.unenrollMember))
	s.mux.HandleFunc("POST /api/v1/enrollment/{id}/approve", s.protected(s.approveGroup))
	s.mux.HandleFunc("PUT /api/v1/enrollment/{id}/shared", s.protected(s.sharedGroup))
	s.mux.HandleFunc("GET /api/v1/activity", s.protected(s.activity))
	s.mux.HandleFunc("POST /api/v1/fixture/incoming", s.protected(s.incoming))
	s.mux.HandleFunc("POST /api/v1/fixture/typing", s.protected(s.fixtureTyping))
	s.mux.HandleFunc("POST /api/v1/fixture/group-members", s.protected(s.fixtureGroupMembers))
	sub, _ := fs.Sub(assets, "assets")
	s.mux.Handle("/", dashboardHandler(sub))
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
	if p := s.PhonePairing; p != nil && strings.HasPrefix(r.URL.Path, "/api/") && r.URL.Path != "/api/v1/phone-pairing" {
		p.mu.Lock()
		blocked := p.blocked
		p.mu.Unlock()
		if blocked {
			s.pairingMaintenance(w, r)
			return
		}
		p.gate.RLock()
		defer p.gate.RUnlock()
		p.mu.Lock()
		blocked = p.blocked
		p.mu.Unlock()
		if blocked {
			s.pairingMaintenance(w, r)
			return
		}
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
	if r.URL.Path == "/api/v1/inbox/settings" || r.URL.Path == "/api/v1/command-pipeline" || r.URL.Path == "/api/v1/playground" {
		limit = 256 * 1024
	}
	if r.URL.Path == "/api/v1/config" || r.URL.Path == "/api/v1/recipes/import" {
		// Commands retain both representations during the registry migration.
		limit = 4 * 1024 * 1024
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	gw := &gzipResponseWriter{
		ResponseWriter: w,
		acceptsGzip:    strings.Contains(r.Header.Get("Accept-Encoding"), "gzip"),
		isRange:        r.Header.Get("Range") != "",
	}
	s.mux.ServeHTTP(gw, r)
}

type gzipAcceptor interface {
	AcceptsGzip() bool
}

type gzipResponseWriter struct {
	http.ResponseWriter
	acceptsGzip bool
	isRange     bool
}

func (g *gzipResponseWriter) AcceptsGzip() bool {
	return g.acceptsGzip && !g.isRange
}

func (g *gzipResponseWriter) Unwrap() http.ResponseWriter {
	return g.ResponseWriter
}

func (g *gzipResponseWriter) Flush() {
	if f, ok := g.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (s *Server) json(w http.ResponseWriter, status int, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		s.bad(w, 500, "internal_error")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	var acceptsGzip bool
	if ga, ok := w.(gzipAcceptor); ok {
		acceptsGzip = ga.AcceptsGzip()
	}
	if acceptsGzip && len(b) >= 1024 {
		var buf bytes.Buffer
		gz, _ := gzip.NewWriterLevel(&buf, gzip.BestSpeed)
		_, _ = gz.Write(b)
		_ = gz.Close()
		compressed := buf.Bytes()
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Add("Vary", "Accept-Encoding")
		w.Header().Set("Content-Length", strconv.Itoa(len(compressed)))
		w.WriteHeader(status)
		_, _ = w.Write(compressed)
		return
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(b)))
	w.WriteHeader(status)
	_, _ = w.Write(b)
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
	if s.PhonePairing != nil {
		value["capabilities"] = append(value["capabilities"].([]string), "phone_pairing")
	}
	if reporter, ok := s.Transport.(transport.ConnectionReporter); ok {
		connection := reporter.ConnectionStatus()
		value["connection_state"] = connection.State
		if connection.RetryAt != "" {
			value["connection_retry_at"] = connection.RetryAt
		}
	}

	if reporter, ok := s.Transport.(transport.SIMReporter); ok {
		if sims, known := reporter.SIMs(); known {
			value["phone_sims"] = sims
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
	value["inbox_revision"] = s.Collection.SettingsRevision()
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

// conversationForToggle verifies a conversation for an enrollment or group
// memory toggle, preferring the transport's cached phone record so dashboard
// switches do not wake the phone unnecessarily. Sends and admissions still
// verify live on the phone.
func (s *Server) conversationForToggle(w http.ResponseWriter, r *http.Request, id string, match func(transport.Conversation) bool) (transport.Conversation, bool) {
	if id == "" {
		s.bad(w, 400, "ineligible_conversation")
		return transport.Conversation{}, false
	}
	if cache, ok := s.Transport.(transport.CachedLookup); ok {
		if c, found := cache.CachedConversation(id); found && match(c) {
			return c, true
		}
	}
	c, err := transport.Lookup(r.Context(), s.Transport, id)
	if err != nil {
		if errors.Is(err, transport.ErrUnavailable) || errors.Is(err, transport.ErrLookupFailed) || !transport.SessionUp(s.Transport) {
			s.bad(w, 503, "transport_unavailable")
			return transport.Conversation{}, false
		}
		s.bad(w, 400, "ineligible_conversation")
		return transport.Conversation{}, false
	}
	if !match(c) {
		s.bad(w, 400, "ineligible_conversation")
		return transport.Conversation{}, false
	}
	return c, true
}

func (s *Server) writeGroupBot(w http.ResponseWriter, id string, extra map[string]any) {
	members, botRoster, err := s.Store.GroupBot(id)
	if err != nil {
		s.bad(w, 500, "storage_error")
		return
	}
	if members == nil {
		members = []string{}
	}
	out := map[string]any{"enrolled": len(members) > 0, "enrolled_members": members, "bot_roster": botRoster}
	maps.Copy(out, extra)
	s.json(w, 200, out)
}

func (s *Server) enroll(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ID   string `json:"conversation_id"`
		Peer string `json:"participant_id"`
		// Roster is, for a group, who the owner saw in it. Peer is then a member.
		Roster string `json:"roster"`
	}
	if !s.decode(w, r, &in) {
		return
	}
	c, ok := s.conversationForToggle(w, r, in.ID, func(c transport.Conversation) bool {
		if c.Group {
			return c.Writable && in.Roster != "" && c.Roster == in.Roster && hasMember(c, in.Peer)
		}
		return c.Writable && in.Roster == "" && c.ParticipantID == in.Peer
	})
	if !ok {
		return
	}
	if c.Group {
		switch e := s.Engine.EnrollMember(c.ID, in.Peer, c.Roster); {
		case e == nil:
			s.writeGroupBot(w, c.ID, nil)
		case errors.Is(e, store.ErrGroupNeedsApproval):
			s.bad(w, 409, "group_needs_approval")
		default:
			s.bad(w, 500, "storage_error")
		}
		return
	}
	if e := s.Engine.Enroll(c.ID, c.ParticipantID); e != nil {
		s.bad(w, 500, "storage_error")
		return
	}
	s.json(w, 200, map[string]bool{"enrolled": true})
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
	// As the phone does, say who in a group sent the message.
	if c, err := transport.Lookup(r.Context(), s.Transport, m.ConversationID); err == nil && c.Group && !m.Outgoing {
		m.Interaction = &transport.MessageInteraction{UpstreamID: m.ID, Protocol: "RCS", Sender: m.ParticipantID, Reactions: []transport.Reaction{}}
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

// fixtureGroupMembers changes who is in the demo group, to exercise a group
// changing between viewing it and sending to it.
func (s *Server) fixtureGroupMembers(w http.ResponseWriter, r *http.Request) {
	fixture, ok := s.Transport.(*transport.Fixture)
	if !s.Fixture || !ok {
		s.bad(w, 404, "not_found")
		return
	}
	var v struct {
		Members []string `json:"members"`
	}
	if !s.decode(w, r, &v) || len(v.Members) == 0 || len(v.Members) > 20 {
		return
	}
	fixture.SetGroupMembers(v.Members...)
	s.json(w, 200, map[string]bool{"ok": true})
}

func hasMember(c transport.Conversation, id string) bool {
	for _, m := range c.Members {
		if m.ID == id {
			return true
		}
	}
	return false
}

func hasAllMembers(c transport.Conversation, ids []string) bool {
	for _, id := range ids {
		if id == "" || !hasMember(c, id) {
			return false
		}
	}
	return true
}

func (s *Server) setGroupMembers(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Members *[]string `json:"members"`
		Roster  string    `json:"roster"`
	}
	if !s.decode(w, r, &in) {
		return
	}
	if in.Members == nil || len(*in.Members) > 200 {
		s.bad(w, 400, "invalid_request")
		return
	}
	id := r.PathValue("id")
	if len(*in.Members) == 0 {
		if err := s.Engine.SetGroupMembers(id, "", nil); err != nil {
			s.bad(w, 500, "storage_error")
			return
		}
		s.writeGroupBot(w, id, nil)
		return
	}
	c, ok := s.conversationForToggle(w, r, id, func(c transport.Conversation) bool {
		return c.Group && c.Writable && in.Roster != "" && c.Roster == in.Roster && hasAllMembers(c, *in.Members)
	})
	if !ok {
		return
	}
	switch err := s.Engine.SetGroupMembers(c.ID, c.Roster, *in.Members); {
	case err == nil:
		s.writeGroupBot(w, c.ID, nil)
	case errors.Is(err, store.ErrGroupNeedsApproval):
		s.bad(w, 409, "group_needs_approval")
	default:
		s.bad(w, 500, "storage_error")
	}
}

func (s *Server) unenrollMember(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if e := s.Engine.UnenrollMember(id, r.PathValue("member")); e != nil {
		s.bad(w, 500, "storage_error")
		return
	}
	s.writeGroupBot(w, id, map[string]any{"enrolled": false})
}

// sharedGroup switches a group between one AI conversation each and one for
// everyone. The group's AI conversations start over either way.
func (s *Server) sharedGroup(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Shared *bool `json:"shared"`
	}
	if !s.decode(w, r, &in) {
		return
	}
	if in.Shared == nil {
		s.bad(w, 400, "shared_required")
		return
	}
	c, ok := s.conversationForToggle(w, r, r.PathValue("id"), func(c transport.Conversation) bool {
		return c.Group && c.Writable
	})
	if !ok {
		return
	}
	if e := s.Engine.SetGroupShared(c.ID, *in.Shared); e != nil {
		s.bad(w, 500, "storage_error")
		return
	}
	s.json(w, 200, map[string]bool{"shared": *in.Shared})
}

// approveGroup accepts who is in a group now for the members still in it. The
// roster must be the one the owner was shown, and still the group's.
func (s *Server) approveGroup(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Roster string `json:"roster"`
	}
	if !s.decode(w, r, &in) {
		return
	}
	c, e := transport.Lookup(r.Context(), s.Transport, r.PathValue("id"))
	if e != nil {
		s.bad(w, 503, "transport_unavailable")
		return
	}
	if !c.Group || !c.Writable || in.Roster == "" || c.Roster != in.Roster {
		s.bad(w, 409, "group_changed")
		return
	}
	present := make([]string, len(c.Members))
	for i, m := range c.Members {
		present[i] = m.ID
	}
	if e = s.Engine.ApproveGroup(c.ID, c.Roster, present); e != nil {
		s.bad(w, 500, "storage_error")
		return
	}
	s.writeGroupBot(w, c.ID, map[string]any{"approved": true})
}
