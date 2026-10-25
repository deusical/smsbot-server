package httpapi

import (
	"context"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/deusical/smsbot-server/internal/notify"
	"github.com/deusical/smsbot-server/internal/store"
)

// device is a registered browser as the dashboard sees it: never its push
// endpoint or keys.
type device struct {
	ID      string                  `json:"id"`
	Label   string                  `json:"label"`
	Created string                  `json:"created"`
	Prefs   store.NotificationPrefs `json:"prefs"`
}

func deviceView(p store.PushSubscription) device {
	return device{ID: p.ID, Label: p.Label, Created: p.Created, Prefs: p.Prefs}
}

func validLabel(s string) bool {
	return utf8.ValidString(s) && utf8.RuneCountInString(s) <= 60 && !strings.ContainsAny(s, "\r\n\t")
}

func (s *Server) notificationRoutes() {
	s.mux.HandleFunc("GET /api/v1/notifications", s.protected(func(w http.ResponseWriter, r *http.Request) {
		if s.Notify == nil {
			s.bad(w, 503, "notifications_unavailable")
			return
		}
		key, err := s.Notify.PublicKey()
		if err != nil {
			s.bad(w, 500, "notifications_unavailable")
			return
		}
		subs, err := s.Store.PushSubscriptions()
		if err != nil {
			s.bad(w, 500, "notifications_unavailable")
			return
		}
		devices := []device{}
		for _, p := range subs {
			devices = append(devices, deviceView(p))
		}
		s.json(w, 200, map[string]any{"public_key": key, "devices": devices})
	}))
	s.mux.HandleFunc("POST /api/v1/notifications/devices", s.protected(func(w http.ResponseWriter, r *http.Request) {
		var v struct {
			Endpoint string `json:"endpoint"`
			Keys     struct {
				P256dh string `json:"p256dh"`
				Auth   string `json:"auth"`
			} `json:"keys"`
			Label string `json:"label"`
			// ExistingOnly refreshes a device that is still registered and
			// never re-creates one that was removed.
			ExistingOnly bool `json:"existing_only"`
		}
		if !s.decode(w, r, &v) {
			return
		}
		if v.ExistingOnly {
			if _, ok, err := s.Store.PushSubscription(store.PushSubscriptionID(v.Endpoint)); err != nil || !ok {
				s.bad(w, 404, "device_not_found")
				return
			}
		}
		if !notify.ValidEndpoint(v.Endpoint) || v.Keys.P256dh == "" || v.Keys.Auth == "" || len(v.Keys.P256dh) > 200 || len(v.Keys.Auth) > 100 || !validLabel(v.Label) {
			s.bad(w, 400, "invalid_subscription")
			return
		}
		p, err := s.Store.SavePushSubscription(store.PushSubscription{Endpoint: v.Endpoint, P256dh: v.Keys.P256dh, Auth: v.Keys.Auth, Label: strings.TrimSpace(v.Label)})
		if err != nil {
			s.bad(w, 500, "subscription_not_saved")
			return
		}
		s.json(w, 200, deviceView(p))
	}))
	s.mux.HandleFunc("PUT /api/v1/notifications/devices/{id}", s.protected(func(w http.ResponseWriter, r *http.Request) {
		var v struct {
			Label string                  `json:"label"`
			Prefs store.NotificationPrefs `json:"prefs"`
		}
		if !s.decode(w, r, &v) {
			return
		}
		if !validLabel(v.Label) || !v.Prefs.Valid() {
			s.bad(w, 400, "invalid_notification_settings")
			return
		}
		p, ok, err := s.Store.UpdatePushSubscription(r.PathValue("id"), strings.TrimSpace(v.Label), v.Prefs)
		switch {
		case err != nil:
			s.bad(w, 500, "notification_settings_not_saved")
		case !ok:
			s.bad(w, 404, "device_not_found")
		default:
			s.json(w, 200, deviceView(p))
		}
	}))
	s.mux.HandleFunc("DELETE /api/v1/notifications/devices/{id}", s.protected(func(w http.ResponseWriter, r *http.Request) {
		ok, err := s.Store.DeletePushSubscription(r.PathValue("id"))
		switch {
		case err != nil:
			s.bad(w, 500, "device_not_removed")
		case !ok:
			s.bad(w, 404, "device_not_found")
		default:
			s.json(w, 200, map[string]bool{"removed": true})
		}
	}))
	s.mux.HandleFunc("POST /api/v1/notifications/devices/{id}/test", s.protected(func(w http.ResponseWriter, r *http.Request) {
		if s.Notify == nil {
			s.bad(w, 503, "notifications_unavailable")
			return
		}
		p, ok, err := s.Store.PushSubscription(r.PathValue("id"))
		if err != nil || !ok {
			s.bad(w, 404, "device_not_found")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		if err = s.Notify.Test(ctx, p); err != nil {
			code := "test_not_delivered"
			if err.Error() == "device_unsubscribed" {
				code = "device_unsubscribed"
			}
			s.bad(w, 502, code)
			return
		}
		s.json(w, 200, map[string]bool{"sent": true})
	}))
}
