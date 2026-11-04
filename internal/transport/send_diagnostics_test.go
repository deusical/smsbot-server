package transport

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestSendRefusalDiagnosticsExcludePayloadAndIdentities(t *testing.T) {
	t.Parallel()
	var logs []SendDiagnostic
	g := &Google{SendLog: func(d SendDiagnostic) { logs = append(logs, d) }}
	_, err := g.SendReply(context.Background(), "synthetic-private-conversation", "synthetic-private-transaction", "synthetic-private-body", "synthetic-private-target", nil)
	if !errors.Is(err, ErrNotSubmitted) {
		t.Fatal("offline reply was not refused", err)
	}
	_, err = g.Send(context.Background(), "synthetic-private-conversation", "synthetic-private-transaction", "synthetic-private-body")
	if !errors.Is(err, ErrNotSubmitted) {
		t.Fatal("offline plain send was not refused", err)
	}
	if len(logs) != 2 || !logs[0].Reply || logs[1].Reply {
		t.Fatal("cannot distinguish reply refusal from plain fallback")
	}
	for _, d := range logs {
		if d.Result != "not_ready" {
			t.Fatal("missing refusal reason")
		}
		if _, err := time.Parse(time.RFC3339Nano, d.At); err != nil {
			t.Fatal("missing diagnostic timestamp", err)
		}
		b, err := json.Marshal(d)
		if err != nil || strings.Contains(string(b), "synthetic-private") {
			t.Fatal("diagnostic exposed sensitive data", err)
		}
	}
}
