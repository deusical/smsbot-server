package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestPhonePairingDrainsRequestsAndCancels(t *testing.T) {
	t.Parallel()
	s := apiSetup(t)
	entered := make(chan struct{})
	p := &PhonePairing{Context: context.Background()}
	p.Replace = func(ctx context.Context, input string, emoji func(string)) (bool, error) {
		close(entered)
		emoji("synthetic-emoji")
		<-ctx.Done()
		return true, ctx.Err()
	}
	s.PhonePairing = p
	t.Cleanup(p.Stop)
	body, _ := json.Marshal(map[string]string{"cookies": `{"SID":"synthetic","HSID":"synthetic","SSID":"synthetic","OSID":"synthetic","APISID":"synthetic","SAPISID":"synthetic"}`})
	if w := req(s, "POST", "/api/v1/phone-pairing", string(body), "", ""); w.Code != 401 {
		t.Fatal(w.Code)
	}
	if w := req(s, "POST", "/api/v1/phone-pairing", string(body), "fixture-client", "https://evil.example"); w.Code != 403 {
		t.Fatal(w.Code)
	}
	if w := req(s, "POST", "/api/v1/phone-pairing", `{"cookies":"invalid"}`, "fixture-client", ""); w.Code != 400 {
		t.Fatal(w.Code)
	}
	p.gate.RLock() // an existing ordinary request
	w := req(s, "POST", "/api/v1/phone-pairing", string(body), "fixture-client", "")
	if w.Code != 202 {
		p.gate.RUnlock()
		t.Fatal(w.Code)
	}
	select {
	case <-entered:
		p.gate.RUnlock()
		t.Fatal("pairing overlapped a request")
	default:
	}
	p.gate.RUnlock()
	<-entered
	if w := req(s, "POST", "/api/v1/phone-pairing", string(body), "fixture-client", ""); w.Code != 409 {
		t.Fatal(w.Code)
	}
	if w := req(s, "POST", "/api/v1/pause", `{"paused":false}`, "fixture-client", ""); w.Code != 503 {
		t.Fatal("unpause accepted during pairing", w.Code)
	}
	if w := req(s, "GET", "/api/v1/phone-pairing", "", "fixture-client", ""); w.Code != 200 || strings.Contains(w.Body.String(), "SID") {
		t.Fatal("status leaked input or failed")
	}
	if w := req(s, "DELETE", "/api/v1/phone-pairing", "", "fixture-client", ""); w.Code != 200 {
		t.Fatal(w.Code)
	}
	p.wg.Wait()
	w = req(s, "GET", "/api/v1/phone-pairing", "", "fixture-client", "")
	if !strings.Contains(w.Body.String(), `"state":"failed"`) || !strings.Contains(w.Body.String(), `"maintenance":false`) || strings.Contains(w.Body.String(), "synthetic") {
		t.Fatal(w.Body.String())
	}
	if w := req(s, "GET", "/api/v1/state", "", "fixture-client", ""); w.Code != 200 {
		t.Fatal(w.Code)
	}
}

func TestPhonePairingReconnectFailureStaysInMaintenance(t *testing.T) {
	t.Parallel()
	s := apiSetup(t)
	p := &PhonePairing{Context: context.Background(), Replace: func(context.Context, string, func(string)) (bool, error) {
		return false, errors.New("synthetic-secret upstream error")
	}}
	s.PhonePairing = p
	defer p.Stop()
	body := `{"cookies":"{\"SID\":\"s\",\"HSID\":\"s\",\"SSID\":\"s\",\"OSID\":\"s\",\"APISID\":\"s\",\"SAPISID\":\"s\"}"}`
	if w := req(s, "POST", "/api/v1/phone-pairing", body, "fixture-client", ""); w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
	p.wg.Wait()
	if w := req(s, "GET", "/api/v1/phone-pairing", "", "fixture-client", ""); !strings.Contains(w.Body.String(), `"maintenance":true`) || strings.Contains(w.Body.String(), "secret") {
		t.Fatal(w.Body.String())
	}
	if w := req(s, "GET", "/api/v1/state", "", "", ""); w.Code != 401 {
		t.Fatal("maintenance state bypassed authentication", w.Code)
	}
	if w := req(s, "GET", "/api/v1/state", "", "fixture-client", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"phone_pairing_maintenance":true`) {
		t.Fatal(w.Code, w.Body.String())
	}
}
