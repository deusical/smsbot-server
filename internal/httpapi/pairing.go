package httpapi

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/deusical/smsbot-server/internal/transport"
)

// PhonePairing owns one bounded pairing attempt. All ordinary requests drain
// before Replace stops workers; status and cancellation remain available.
type PhonePairing struct {
	Replace func(context.Context, string, func(string)) (bool, error)
	Context context.Context
	gate    sync.RWMutex
	mu      sync.Mutex
	state   string
	emoji   string
	blocked bool
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	stopped bool
}

func (p *PhonePairing) Stop() {
	p.mu.Lock()
	p.stopped = true
	if p.cancel != nil {
		p.cancel()
	}
	p.mu.Unlock()
	p.wg.Wait()
}

// maintenance keeps an authenticated browser recoverable after a reload without
// reading any transport or service fields that are being replaced.
func (s *Server) pairingMaintenance(w http.ResponseWriter, r *http.Request) {
	if r.Method == "GET" && r.URL.Path == "/api/v1/state" {
		s.protected(func(w http.ResponseWriter, r *http.Request) {
			s.json(w, 200, map[string]any{"api_version": 1, "paused": true, "transport_ready": false, "transport": "google_messages", "fixture": false, "capabilities": []string{"phone_pairing"}, "phone_pairing_maintenance": true})
		})(w, r)
		return
	}
	s.bad(w, 503, "phone_pairing_maintenance")
}

func (s *Server) pairingRoutes() {
	s.mux.HandleFunc("GET /api/v1/phone-pairing", s.protected(func(w http.ResponseWriter, r *http.Request) {
		p := s.PhonePairing
		if p == nil {
			s.json(w, 200, map[string]string{"state": "unsupported"})
			return
		}
		p.mu.Lock()
		defer p.mu.Unlock()
		state := p.state
		if state == "" {
			state = "idle"
		}
		s.json(w, 200, map[string]any{"state": state, "emoji": p.emoji, "maintenance": p.blocked})
	}))
	s.mux.HandleFunc("POST /api/v1/phone-pairing", s.protected(func(w http.ResponseWriter, r *http.Request) {
		p := s.PhonePairing
		if p == nil {
			s.bad(w, 409, "pairing_unsupported")
			return
		}
		var in struct {
			Cookies string `json:"cookies"`
		}
		if !s.decode(w, r, &in) {
			return
		}
		if _, err := transport.ParseCookies(in.Cookies); err != nil {
			s.bad(w, 400, "invalid_pairing_cookies")
			return
		}
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.cancel != nil {
			s.bad(w, 409, "pairing_in_progress")
			return
		}
		if p.stopped {
			s.bad(w, 503, "service_stopping")
			return
		}
		// Pause immediately, then repeat after existing requests drain so an
		// already-running unpause request cannot outlive the transition.
		if err := s.Engine.Pause(true); err != nil {
			s.bad(w, 500, "storage_error")
			return
		}
		ctx, cancel := context.WithTimeout(p.Context, 3*time.Minute)
		p.cancel, p.state, p.emoji, p.blocked = cancel, "pairing", "", true
		p.wg.Add(1)
		go func(input string) {
			defer p.wg.Done()
			defer cancel()
			p.gate.Lock()
			defer p.gate.Unlock()
			available, err := p.Replace(ctx, input, func(emoji string) {
				p.mu.Lock()
				p.emoji = emoji
				p.mu.Unlock()
			})
			p.mu.Lock()
			defer p.mu.Unlock()
			p.cancel, p.emoji = nil, ""
			p.state = "complete"
			p.blocked = !available
			if err != nil {
				p.state = "failed"
			}
		}(in.Cookies)
		s.json(w, 202, map[string]string{"state": "pairing"})
	}))
	s.mux.HandleFunc("DELETE /api/v1/phone-pairing", s.protected(func(w http.ResponseWriter, r *http.Request) {
		if p := s.PhonePairing; p != nil {
			p.mu.Lock()
			if p.cancel != nil {
				p.cancel()
			}
			p.mu.Unlock()
		}
		s.json(w, 200, map[string]bool{"cancel_requested": true})
	}))
}
