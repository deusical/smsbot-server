package httpapi

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/deusical/smsbot-server/internal/notify"
	"github.com/deusical/smsbot-server/internal/store"
)

func TestNotificationDevicesHaveTheirOwnSettings(t *testing.T) {
	s := apiSetup(t)
	if w := ownerRequest(s, "GET", "/api/v1/notifications", ""); w.Code != 503 {
		t.Fatal("notifications answered before the service was wired", w.Code)
	}
	s.Notify = notify.New(s.Store, s.Transport, "https://example.test")
	tests := 0
	s.Notify.Send = func(ctx context.Context, sub store.PushSubscription, b []byte, urgent bool) (bool, error) {
		tests++
		return false, nil
	}

	register := func(endpoint, label string) device {
		t.Helper()
		w := ownerRequest(s, "POST", "/api/v1/notifications/devices", `{"endpoint":"`+endpoint+`","keys":{"p256dh":"synthetic-key","auth":"synthetic-auth"},"label":"`+label+`"}`)
		if w.Code != 200 {
			t.Fatal("register", w.Code, w.Body.String())
		}
		var d device
		json.Unmarshal(w.Body.Bytes(), &d)
		return d
	}
	desktop := register("https://fcm.googleapis.com/fcm/send/desktop", "Legion")
	phone := register("https://fcm.googleapis.com/fcm/send/phone", "Phone")
	if desktop.ID == phone.ID || desktop.Prefs.Messages != "all" || !desktop.Prefs.ShowText {
		t.Fatalf("defaults: %+v %+v", desktop, phone)
	}
	for _, bad := range []string{"http://fcm.googleapis.com/x", "https://127.0.0.1/x", "https://acer.tailddfcef.ts.net:8443/x"} {
		if w := ownerRequest(s, "POST", "/api/v1/notifications/devices", `{"endpoint":"`+bad+`","keys":{"p256dh":"k","auth":"a"},"label":""}`); w.Code != 400 {
			t.Fatal("accepted endpoint", bad, w.Code)
		}
	}

	if w := ownerRequest(s, "PUT", "/api/v1/notifications/devices/"+phone.ID, `{"label":"Phone","prefs":{"messages":"loud"}}`); w.Code != 400 {
		t.Fatal("accepted invalid settings", w.Code)
	}
	if w := ownerRequest(s, "PUT", "/api/v1/notifications/devices/"+phone.ID, `{"label":"SIM phone","prefs":{"messages":"off","show_text":false,"approvals":true,"problems":true,"connection":false}}`); w.Code != 200 {
		t.Fatal("update", w.Code, w.Body.String())
	}

	w := ownerRequest(s, "GET", "/api/v1/notifications", "")
	if w.Code != 200 || strings.Contains(w.Body.String(), "fcm.googleapis.com") || strings.Contains(w.Body.String(), "synthetic-key") {
		t.Fatal("device list exposes push endpoints or keys", w.Body.String())
	}
	var list struct {
		PublicKey string   `json:"public_key"`
		Devices   []device `json:"devices"`
	}
	json.Unmarshal(w.Body.Bytes(), &list)
	if list.PublicKey == "" || len(list.Devices) != 2 {
		t.Fatalf("list: %+v", list)
	}
	for _, d := range list.Devices {
		switch d.ID {
		case desktop.ID:
			if d.Prefs.Messages != "all" || !d.Prefs.Connection {
				t.Fatalf("desktop settings changed with the phone's: %+v", d)
			}
		case phone.ID:
			if d.Label != "SIM phone" || d.Prefs.Messages != "off" || d.Prefs.Connection || d.Prefs.Muted == nil {
				t.Fatalf("phone settings: %+v", d)
			}
		}
	}
	// The same key is reused rather than regenerated.
	again := ownerRequest(s, "GET", "/api/v1/notifications", "")
	if !strings.Contains(again.Body.String(), list.PublicKey) {
		t.Fatal("application server key changed")
	}

	if w := ownerRequest(s, "POST", "/api/v1/notifications/devices/"+phone.ID+"/test", ""); w.Code != 200 || tests != 1 {
		t.Fatal("test notification", w.Code, tests)
	}
	if w := ownerRequest(s, "DELETE", "/api/v1/notifications/devices/"+phone.ID, ""); w.Code != 200 {
		t.Fatal("remove", w.Code)
	}
	if w := ownerRequest(s, "DELETE", "/api/v1/notifications/devices/"+phone.ID, ""); w.Code != 404 {
		t.Fatal("removed twice", w.Code)
	}
	// A removed browser looking itself up is not registered again.
	if w := ownerRequest(s, "POST", "/api/v1/notifications/devices", `{"endpoint":"https://fcm.googleapis.com/fcm/send/phone","keys":{"p256dh":"k","auth":"a"},"label":"","existing_only":true}`); w.Code != 404 {
		t.Fatal("removed device re-registered by lookup", w.Code)
	}
	if w := ownerRequest(s, "POST", "/api/v1/notifications/devices", `{"endpoint":"https://fcm.googleapis.com/fcm/send/desktop","keys":{"p256dh":"k2","auth":"a2"},"label":"","existing_only":true}`); w.Code != 200 || !strings.Contains(w.Body.String(), "Legion") {
		t.Fatal("existing device lookup", w.Code, w.Body.String())
	}
}
