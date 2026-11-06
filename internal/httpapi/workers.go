package httpapi

import (
	"net/http"

	"github.com/deusical/smsbot-server/internal/bot"
)

// workerEntry is a worker as the dashboard edits it. Token is write-only:
// empty keeps the stored token, and it is never returned.
type workerEntry struct {
	bot.WorkerEntry
	Token string `json:"token,omitempty"`
}

type workersBody struct {
	Workers []workerEntry       `json:"workers"`
	Routes  map[string][]string `json:"routes"`
}

type workersView struct {
	Config bot.WorkerConfig   `json:"config"`
	Status []bot.WorkerStatus `json:"status"`
	Local  bool               `json:"local"`
}

func (s *Server) workerRoutes() {
	s.mux.HandleFunc("GET /api/v1/workers", s.protected(func(w http.ResponseWriter, r *http.Request) {
		s.workersView(w, r)
	}))
	s.mux.HandleFunc("PUT /api/v1/workers", s.protected(func(w http.ResponseWriter, r *http.Request) {
		if s.Workers == nil {
			s.bad(w, 503, "workers_unavailable")
			return
		}
		var body workersBody
		if !s.decode(w, r, &body) {
			return
		}
		cfg := bot.WorkerConfig{Workers: []bot.WorkerEntry{}, Routes: body.Routes}
		tokens := map[string]string{}
		for _, e := range body.Workers {
			cfg.Workers = append(cfg.Workers, e.WorkerEntry)
			tokens[e.Name] = e.Token
		}
		if len(cfg.Routes) == 0 {
			cfg.Routes = nil
		}
		if err := s.Workers.Save(cfg, tokens); err != nil {
			s.json(w, 400, map[string]string{"error": "invalid_workers", "detail": err.Error()})
			return
		}
		s.workersView(w, r)
	}))
}

func (s *Server) workersView(w http.ResponseWriter, r *http.Request) {
	if s.Workers == nil {
		s.bad(w, 503, "workers_unavailable")
		return
	}
	cfg, err := s.Workers.Config()
	if err != nil {
		s.json(w, 500, map[string]string{"error": "workers_config_invalid", "detail": err.Error()})
		return
	}
	status, err := s.Workers.Status(r.Context())
	if err != nil {
		s.bad(w, 500, "workers_unavailable")
		return
	}
	if cfg.Workers == nil {
		cfg.Workers = []bot.WorkerEntry{}
	}
	s.json(w, 200, workersView{Config: cfg, Status: status, Local: s.Workers.Local != nil})
}
