package transport

import (
	"errors"
	"go.mau.fi/mautrix-gmessages/pkg/libgm/events"
	"time"
)

// AuthDiagnostic deliberately has no free-text upstream fields or credentials.
type AuthDiagnostic struct {
	At         string `json:"at"`
	Event      string `json:"event"`
	HTTPStatus int    `json:"http_status,omitempty"`
	Category   int64  `json:"category,omitempty"`
	Success    bool   `json:"success"`
}

func (g *Google) diagnose(event string, err error) {
	if g.Diagnostic == nil {
		return
	}
	d := AuthDiagnostic{At: time.Now().UTC().Format(time.RFC3339Nano), Event: event, Success: err == nil}
	var req events.RequestError
	if errors.As(err, &req) {
		d.Category = req.Data.GetType()
		if req.HTTP != nil && req.HTTP.Resp != nil {
			d.HTTPStatus = req.HTTP.Resp.StatusCode
		}
	}
	var httpErr events.HTTPError
	if errors.As(err, &httpErr) && httpErr.Resp != nil {
		d.HTTPStatus = httpErr.Resp.StatusCode
	}
	g.Diagnostic(d)
}
func (g *Google) persistRefresh() error {
	err := g.Save()
	g.diagnose("token_refresh_saved", err)
	if err != nil {
		g.ready.Store(false)
	}
	return err
}
