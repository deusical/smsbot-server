package notify

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/deusical/smsbot-server/internal/store"
	"github.com/deusical/smsbot-server/internal/transport"
	"github.com/deusical/smsbot-server/internal/vault"
)

type sent struct {
	device  string
	payload Payload
}

type readiness struct {
	*transport.Fixture
	ready bool
}

func (r *readiness) Ready() bool { return r.ready }

func setup(t *testing.T) (*Service, *readiness, *[]sent, *time.Time) {
	t.Helper()
	v, err := vault.New(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	s, err := store.Open(filepath.Join(t.TempDir(), "state.db"), v)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.DB.Close() })
	tr := &readiness{Fixture: transport.NewFixture(), ready: true}
	n := New(s, tr, "https://example.test")
	now := time.Now()
	n.Now = func() time.Time { return now }
	var mu sync.Mutex
	out := &[]sent{}
	n.Send = func(ctx context.Context, sub store.PushSubscription, b []byte, urgent bool) (bool, error) {
		var p Payload
		if err := json.Unmarshal(b, &p); err != nil {
			t.Fatal(err)
		}
		mu.Lock()
		*out = append(*out, sent{sub.Label, p})
		mu.Unlock()
		return sub.Label == "gone", nil
	}
	return n, tr, out, &now
}

func device(t *testing.T, s *store.Store, label string, prefs store.NotificationPrefs) store.PushSubscription {
	t.Helper()
	p, err := s.SavePushSubscription(store.PushSubscription{Endpoint: "https://push.example.com/" + label, P256dh: "key", Auth: "auth", Label: label})
	if err != nil {
		t.Fatal(err)
	}
	p, _, err = s.UpdatePushSubscription(p.ID, label, prefs)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func incoming(t *testing.T, s *store.Store, id, conv, text string, at time.Time) {
	t.Helper()
	if err := s.SaveMessageAt(id, conv, "incoming", text, at); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec("INSERT INTO message_changes(conversation,message) VALUES(?,?)", conv, id); err != nil {
		t.Fatal(err)
	}
}

func TestMessagesFollowEachDevicesOwnSettings(t *testing.T) {
	n, _, out, now := setup(t)
	all := store.DefaultNotificationPrefs()
	botOn := store.DefaultNotificationPrefs()
	botOn.Messages = "bot_on"
	chosen := store.DefaultNotificationPrefs()
	chosen.Messages, chosen.Conversations, chosen.ShowText = "chosen", []string{"chat-b"}, false
	off := store.DefaultNotificationPrefs()
	off.Messages = "off"
	muted := store.DefaultNotificationPrefs()
	muted.Muted = []string{"chat-a"}
	for label, p := range map[string]store.NotificationPrefs{"all": all, "bot-on": botOn, "chosen": chosen, "off": off, "muted": muted} {
		device(t, n.Store, label, p)
	}
	if err := n.Store.Enroll("chat-a", "peer-a"); err != nil {
		t.Fatal(err)
	}
	incoming(t, n.Store, "old", "chat-a", "Before notifications started", *now)
	if err := n.Tick(context.Background()); err != nil || len(*out) != 0 {
		t.Fatalf("first tick must only record the position: err=%v sent=%v", err, *out)
	}

	incoming(t, n.Store, "m1", "chat-a", "Synthetic hello", *now)
	incoming(t, n.Store, "m2", "chat-b", "Synthetic second", *now)
	incoming(t, n.Store, "stale", "chat-b", "Catch-up after an outage", now.Add(-time.Hour))
	if err := n.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := map[string][]string{}
	for _, s := range *out {
		got[s.device] = append(got[s.device], s.payload.Body)
	}
	want := map[string][]string{
		"all":    {"Synthetic hello", "Synthetic second"},
		"bot-on": {"Synthetic hello"},
		"chosen": {"New message"},
		"muted":  {"Synthetic second"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for k, v := range want {
		if len(got[k]) != len(v) {
			t.Fatalf("%s got %v want %v", k, got[k], v)
		}
		for i := range v {
			if got[k][i] != v[i] {
				t.Fatalf("%s got %v want %v", k, got[k], v)
			}
		}
	}

	// A later change to the same message does not notify again.
	*out = nil
	if _, err := n.Store.DB.Exec("INSERT INTO message_changes(conversation,message) VALUES('chat-a','m1')"); err != nil {
		t.Fatal(err)
	}
	if err := n.Tick(context.Background()); err != nil || len(*out) != 0 {
		t.Fatalf("revision notified again: %v", *out)
	}
}

func TestAlertsAndOutagesRespectDeviceSettings(t *testing.T) {
	n, tr, out, now := setup(t)
	quiet := store.DefaultNotificationPrefs()
	quiet.Approvals, quiet.Problems, quiet.Connection = false, false, false
	device(t, n.Store, "alerts", store.DefaultNotificationPrefs())
	device(t, n.Store, "quiet", quiet)
	ctx := context.Background()
	if _, err := n.Store.DB.Exec("INSERT INTO jobs VALUES('existing','chat-a','!ask','failed','2026-01-01T00:00:00Z','','x','')"); err != nil {
		t.Fatal(err)
	}
	if err := n.Tick(ctx); err != nil || len(*out) != 0 {
		t.Fatalf("existing problems must not notify at start: %v", *out)
	}
	if _, err := n.Store.DB.Exec("INSERT INTO jobs VALUES('j1','chat-a','!ask','awaiting_approval','2026-01-01T00:00:01Z','','','')"); err != nil {
		t.Fatal(err)
	}
	n.Tick(ctx)
	n.Tick(ctx)
	if len(*out) != 1 || (*out)[0].device != "alerts" || (*out)[0].payload.Title != "Approval needed" {
		t.Fatalf("approval: %v", *out)
	}
	*out = nil
	if _, err := n.Store.DB.Exec("UPDATE jobs SET state='unknown_submission' WHERE id='j1'"); err != nil {
		t.Fatal(err)
	}
	n.Tick(ctx)
	if len(*out) != 1 || (*out)[0].payload.Title != "Send outcome uncertain" {
		t.Fatalf("uncertain: %v", *out)
	}

	*out = nil
	tr.ready = false
	n.Tick(ctx)
	*now = now.Add(time.Minute)
	n.Tick(ctx)
	if len(*out) != 0 {
		t.Fatalf("short outage notified: %v", *out)
	}
	*now = now.Add(2 * time.Minute)
	n.Tick(ctx)
	n.Tick(ctx)
	tr.ready = true
	n.Tick(ctx)
	if len(*out) != 2 || (*out)[0].payload.Title != "Phone disconnected" || (*out)[1].payload.Title != "Phone reconnected" {
		t.Fatalf("outage: %v", *out)
	}
}

func TestGoneDevicesAreForgotten(t *testing.T) {
	n, _, _, now := setup(t)
	device(t, n.Store, "gone", store.DefaultNotificationPrefs())
	n.Tick(context.Background())
	incoming(t, n.Store, "m1", "chat-a", "Synthetic", *now)
	n.Tick(context.Background())
	if subs, _ := n.Store.PushSubscriptions(); len(subs) != 0 {
		t.Fatal("a device the push service no longer knows was kept")
	}
}

func TestRegisteringAgainKeepsSettings(t *testing.T) {
	n, _, _, _ := setup(t)
	prefs := store.DefaultNotificationPrefs()
	prefs.Messages = "off"
	p := device(t, n.Store, "phone", prefs)
	again, err := n.Store.SavePushSubscription(store.PushSubscription{Endpoint: "https://push.example.com/phone", P256dh: "new-key", Auth: "new-auth"})
	if err != nil || again.ID != p.ID || again.Prefs.Messages != "off" || again.Label != "phone" || again.P256dh != "new-key" {
		t.Fatalf("again=%+v err=%v", again, err)
	}
}

func TestEndpointValidation(t *testing.T) {
	for raw, ok := range map[string]bool{
		"https://fcm.googleapis.com/fcm/send/abc":   true,
		"https://updates.push.services.mozilla.com": true,
		"http://fcm.googleapis.com/fcm/send/abc":    false,
		"https://127.0.0.1/x":                       false,
		"https://[::1]/x":                           false,
		"https://localhost/x":                       false,
		"https://router.local/x":                    false,
		"https://acer.tailddfcef.ts.net:8443/api":   false,
		"https://user:pass@fcm.googleapis.com/x":    false,
	} {
		if ValidEndpoint(raw) != ok {
			t.Errorf("%s: want %v", raw, ok)
		}
	}
}
