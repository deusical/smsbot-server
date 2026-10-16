package httpapi

import (
	"encoding/json"
	"smsbot-server/internal/transport"
	"testing"
)

type recoveringFixture struct{ *transport.Fixture }

func (*recoveringFixture) ConnectionStatus() transport.ConnectionStatus {
	return transport.ConnectionStatus{State: "reconnecting", RetryAt: "2026-10-16T12:00:00Z"}
}
func TestStateReportsRecoveryWithoutChangingLiveChoice(t *testing.T) {
	s := apiSetup(t)
	fixture := transport.NewFixture()
	fixture.Online = false
	s.Engine.Transport = &recoveringFixture{fixture}
	if err := s.Engine.Pause(false); err != nil {
		t.Fatal(err)
	}
	w := req(s, "GET", "/api/v1/state", "", "fixture-client", "")
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	var value struct {
		Paused bool   `json:"paused"`
		Ready  bool   `json:"transport_ready"`
		State  string `json:"connection_state"`
		Retry  string `json:"connection_retry_at"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &value); err != nil {
		t.Fatal(err)
	}
	if value.Paused || value.Ready || value.State != "reconnecting" || value.Retry == "" {
		t.Fatal("connection health changed Live choice or lost recovery status")
	}
}
