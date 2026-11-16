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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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

func TestRunPeriodicSaveSkipsUnchangedSessionAndWritesOnChange(t *testing.T) {
	t.Parallel()
	g, original := cookieUpdateFixture(t, syntheticSession)
	plain, err := g.vault.Open("google-session", string(original))
	if err != nil {
		t.Fatal(err)
	}
	var auth libgm.AuthData
	if err := json.Unmarshal(plain, &auth); err != nil {
		t.Fatal(err)
	}
	g.attach(&auth)
	g.saveInterval = 10 * time.Millisecond
	g.recoveryInterval = time.Hour
	g.connected = func() bool { return true }
	g.ready.Store(true)

	if err := g.Save(); err != nil {
		t.Fatal(err)
	}
	firstCiphertext, err := os.ReadFile(g.path)
	if err != nil {
		t.Fatal(err)
	}

	runTicks := func(d time.Duration) {
		ctx, cancel := context.WithTimeout(context.Background(), d)
		defer cancel()
		g.Run(ctx, func(Incoming) {}, func() {}, func(string, string, string, string) {})
	}

	// Unchanged periodic ticks must not re-seal or rewrite the session file.
	runTicks(65 * time.Millisecond)
	afterIdle, err := os.ReadFile(g.path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(firstCiphertext, afterIdle) {
		t.Fatal("unchanged session was rewritten on periodic tick")
	}

	// Mutating session cookies causes the next periodic tick to persist once.
	auth.CookiesLock.Lock()
	if auth.Cookies == nil {
		auth.Cookies = map[string]string{}
	}
	auth.Cookies["SID"] = "SYNTHETIC_ROTATED_SID"
	auth.CookiesLock.Unlock()

	deadline := time.Now().Add(2 * time.Second)
	var afterChange []byte
	for time.Now().Before(deadline) {
		runTicks(25 * time.Millisecond)
		afterChange, err = os.ReadFile(g.path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(firstCiphertext, afterChange) {
			break
		}
	}
	if bytes.Equal(firstCiphertext, afterChange) {
		t.Fatal("changed session was not saved on periodic tick")
	}
	opened, err := g.vault.Open("google-session", string(afterChange))
	if err != nil {
		t.Fatal(err)
	}
	var persisted libgm.AuthData
	if err := json.Unmarshal(opened, &persisted); err != nil {
		t.Fatal(err)
	}
	if persisted.Cookies["SID"] != "SYNTHETIC_ROTATED_SID" {
		t.Fatalf("persisted cookie = %q", persisted.Cookies["SID"])
	}

	// Further ticks with no new change keep the file untouched again.
	runTicks(65 * time.Millisecond)
	afterSecondIdle, err := os.ReadFile(g.path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(afterChange, afterSecondIdle) {
		t.Fatal("unchanged session rewritten after saving update")
	}
}
