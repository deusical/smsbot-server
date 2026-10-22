package transport

import (
	"bytes"
	"context"
	"errors"
	"go.mau.fi/mautrix-gmessages/pkg/libgm/gmproto"
	gmutil "go.mau.fi/mautrix-gmessages/pkg/libgm/util"
	"go.mau.fi/util/pblite"
	"google.golang.org/protobuf/proto"
	"io"
	"net/http"
	"testing"
)

type metricsRoundTrip func(*http.Request) (*http.Response, error)

func (f metricsRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestRelayMetricsCountInternalCallsAndRetriesWithoutChangingRequests(t *testing.T) {
	for _, jsonEncoding := range []bool{false, true} {
		action, _ := proto.Marshal(&gmproto.OutgoingRPCData{Action: gmproto.ActionType_NOTIFY_DITTO_ACTIVITY, EncryptedProtoData: []byte("synthetic private bytes")})
		envelope := &gmproto.OutgoingRPCMessage{Data: &gmproto.OutgoingRPCMessage_Data{MessageData: action}}
		data, _ := proto.Marshal(envelope)
		mime := "application/x-protobuf"
		if jsonEncoding {
			data, _ = pblite.Marshal(envelope)
			mime = "application/json+protobuf"
		}
		var g Google
		attempt := 0
		tr := &relayTransport{metrics: &g.relayMetrics, next: metricsRoundTrip(func(r *http.Request) (*http.Response, error) {
			body, _ := io.ReadAll(r.Body)
			if !bytes.Equal(body, data) {
				t.Fatal("metrics changed body")
			}
			attempt++
			if attempt == 1 {
				return nil, errors.New("synthetic error")
			}
			return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(nil))}, nil
		})}
		for i := 0; i < 2; i++ {
			req, _ := http.NewRequest("POST", gmutil.SendMessageURLGoogle, bytes.NewReader(data))
			req.Header.Set("Content-Type", mime)
			_, _ = tr.RoundTrip(req)
		}
		c := g.PhoneRelayRequests()["ping"]
		if c.Attempts != 2 || c.Failed != 1 || c.Accepted != 1 {
			t.Fatalf("lost internal attempts: %+v", c)
		}
	}
}
func TestPurposeLabelsAreBoundedAndDoNotReplaceOperationCounts(t *testing.T) {
	var g Google
	g.countPhone(WithPhonePurpose(context.Background(), "backfill"), phoneMessages)
	g.countPhone(WithPhonePurpose(context.Background(), "synthetic-private-identifier"), phoneLookup)
	p := g.PhoneRequestPurposes()
	if len(p) != len(phonePurposes) || p["backfill"].Total != 1 || p["other"].Total != 1 {
		t.Fatal("unbounded or missing purpose")
	}
	if g.PhoneRequests()[phoneMessages].Total != 1 {
		t.Fatal("legacy counter changed")
	}
}
