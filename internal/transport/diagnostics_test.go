package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"go.mau.fi/mautrix-gmessages/pkg/libgm"
	"go.mau.fi/mautrix-gmessages/pkg/libgm/events"
	"go.mau.fi/mautrix-gmessages/pkg/libgm/gmproto"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

func TestAccountSnapshotDoesNotInvalidateReadiness(t *testing.T) {
	for _, tc := range []struct {
		name       string
		event      any
		diagnostic string
		available  bool
	}{
		{"snapshot", &events.AccountChange{IsFake: true, AccountChangeOrSomethingEvent: &gmproto.AccountChangeOrSomethingEvent{Account: "SYNTHETIC_PRIVATE_ACCOUNT"}}, "account_snapshot", true},
		{"change", &events.AccountChange{AccountChangeOrSomethingEvent: &gmproto.AccountChangeOrSomethingEvent{Account: "SYNTHETIC_PRIVATE_ACCOUNT"}}, "account_change", false},
		{"logout", &events.GaiaLoggedOut{}, "google_logged_out", false},
		{"phone", &events.PhoneNotResponding{}, "phone_not_responding", false},
		{"data", &events.NoDataReceived{}, "no_data_received", false},
		{"active", &events.HackySetActiveMayFail{}, "active_session_warning", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			var emitted AuthDiagnostic
			g := &Google{updates: make(chan any, 1), Diagnostic: func(d AuthDiagnostic) { emitted = d; cancel() }}
			g.ready.Store(true)
			g.updates <- tc.event
			pauses := 0
			g.Run(ctx, func(Incoming) { t.Fatal("availability event routed as a message") }, func() { pauses++ }, func(string, string, string, string) { t.Fatal("availability event routed as a receipt") })
			if g.ready.Load() != tc.available || (pauses == 0) != tc.available || emitted.Success != tc.available || emitted.Event != tc.diagnostic {
				t.Fatalf("incorrect availability transition: ready=%v pauses=%d diagnostic=%+v", g.ready.Load(), pauses, emitted)
			}
			encoded, _ := json.Marshal(emitted)
			if strings.Contains(string(encoded), "SYNTHETIC_PRIVATE_ACCOUNT") {
				t.Fatal("account data leaked into diagnostic")
			}
		})
	}
}

func TestDiagnosticNeverIncludesUpstreamData(t *testing.T) {
	var emitted AuthDiagnostic
	g := &Google{Diagnostic: func(d AuthDiagnostic) { emitted = d }}
	err := events.RequestError{Data: &gmproto.ErrorResponse{Type: 16, Message: "SYNTHETIC_COOKIE_DETAIL"}, HTTP: &events.HTTPError{Resp: &http.Response{StatusCode: 401}, Body: []byte("SYNTHETIC_COOKIE_DETAIL")}}
	g.diagnose("listen_fatal", err)
	b, _ := json.Marshal(emitted)
	if emitted.HTTPStatus != 401 || emitted.Category != 16 || emitted.Success || strings.Contains(string(b), "SYNTHETIC_COOKIE_DETAIL") {
		t.Fatal("diagnostic leaked data or lost numeric status")
	}
}
func TestRefreshPersistenceCreatesEncryptedUpdatedState(t *testing.T) {
	g, original := cookieUpdateFixture(t, syntheticSession)
	plain, err := g.vault.Open("google-session", string(original))
	if err != nil {
		t.Fatal(err)
	}
	var auth libgm.AuthData
	if json.Unmarshal(plain, &auth) != nil {
		t.Fatal("synthetic auth invalid")
	}
	auth.TachyonAuthToken = []byte("SYNTHETIC_NEW_TOKEN")
	g.attach(&auth)
	var emitted AuthDiagnostic
	g.Diagnostic = func(d AuthDiagnostic) { emitted = d }
	if err = g.persistRefresh(); err != nil {
		t.Fatal(err)
	}
	if !emitted.Success || emitted.Event != "token_refresh_saved" {
		t.Fatal("save result not recorded")
	}
	current, _ := os.ReadFile(g.path)
	if bytes.Contains(current, auth.TachyonAuthToken) {
		t.Fatal("plaintext token persisted")
	}
	plain, err = g.vault.Open("google-session", string(current))
	if err != nil {
		t.Fatal(err)
	}
	var saved libgm.AuthData
	json.Unmarshal(plain, &saved)
	if !bytes.Equal(saved.TachyonAuthToken, auth.TachyonAuthToken) {
		t.Fatal("refreshed token lost")
	}
}
