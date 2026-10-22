package transport

import (
	"go.mau.fi/mautrix-gmessages/pkg/libgm/gmproto"
	gmutil "go.mau.fi/mautrix-gmessages/pkg/libgm/util"
	"go.mau.fi/util/pblite"
	"google.golang.org/protobuf/proto"
	"io"
	"net/http"
	"sync"
	"time"
)

// Relay metrics include libgm control RPCs and retries. Accepted means HTTP 2xx
// from Google, not phone execution or an SMS delivery receipt. Only the action
// enum is retained; envelopes, cookies and payloads are never logged or stored.
type RelayRequestCount struct {
	Attempts uint64 `json:"attempts"`
	Accepted uint64 `json:"accepted"`
	Failed   uint64 `json:"failed"`
	LastHour uint64 `json:"last_hour"`
}
type relayMetrics struct {
	mu     sync.Mutex
	counts map[string]RelayRequestCount
	recent phoneRequests
}

var relayKinds = []string{"list", "conversation", "messages", "full_size", "mark_read", "send", "react", "probe", "ping", "updates", "presence", "other"}

func (m *relayMetrics) record(kind string, accepted bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.counts == nil {
		m.counts = map[string]RelayRequestCount{}
	}
	c := m.counts[kind]
	c.Attempts++
	if accepted {
		c.Accepted++
	} else {
		c.Failed++
	}
	m.counts[kind] = c
	m.recent.add(kind, time.Now())
}
func (g *Google) PhoneRelayRequests() map[string]RelayRequestCount {
	m := &g.relayMetrics
	m.mu.Lock()
	defer m.mu.Unlock()
	recent := m.recent.snapshotKinds(time.Now(), relayKinds)
	out := map[string]RelayRequestCount{}
	for _, kind := range relayKinds {
		c := m.counts[kind]
		c.LastHour = recent[kind].LastHour
		out[kind] = c
	}
	return out
}

type PhoneRelayReporter interface {
	PhoneRelayRequests() map[string]RelayRequestCount
}
type relayTransport struct {
	next    http.RoundTripper
	metrics *relayMetrics
}

func (t *relayTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	target := req.URL.String() == gmutil.SendMessageURL || req.URL.String() == gmutil.SendMessageURLGoogle
	kind := "other"
	if target {
		kind = relayKind(req)
	}
	response, err := t.next.RoundTrip(req)
	if target {
		t.metrics.record(kind, err == nil && response != nil && response.StatusCode >= 200 && response.StatusCode < 300)
	}
	return response, err
}
func (t *relayTransport) CloseIdleConnections() {
	if c, ok := t.next.(interface{ CloseIdleConnections() }); ok {
		c.CloseIdleConnections()
	}
}
func relayKind(req *http.Request) string {
	if req.GetBody == nil {
		return "other"
	}
	body, err := req.GetBody()
	if err != nil {
		return "other"
	}
	defer body.Close()
	data, err := io.ReadAll(io.LimitReader(body, (1<<20)+1))
	if err != nil || len(data) > 1<<20 {
		return "other"
	}
	envelope := &gmproto.OutgoingRPCMessage{}
	if req.Header.Get("Content-Type") == "application/json+protobuf" {
		err = pblite.Unmarshal(data, envelope)
	} else {
		err = proto.Unmarshal(data, envelope)
	}
	if err != nil {
		return "other"
	}
	action := &gmproto.OutgoingRPCData{}
	if proto.Unmarshal(envelope.GetData().GetMessageData(), action) != nil {
		return "other"
	}
	switch action.GetAction() {
	case gmproto.ActionType_LIST_CONVERSATIONS:
		return "list"
	case gmproto.ActionType_GET_CONVERSATION:
		return "conversation"
	case gmproto.ActionType_LIST_MESSAGES:
		return "messages"
	case gmproto.ActionType_GET_FULL_SIZE_IMAGE:
		return "full_size"
	case gmproto.ActionType_MESSAGE_READ:
		return "mark_read"
	case gmproto.ActionType_SEND_MESSAGE:
		return "send"
	case gmproto.ActionType_SEND_REACTION:
		return "react"
	case gmproto.ActionType_IS_BUGLE_DEFAULT:
		return "probe"
	case gmproto.ActionType_NOTIFY_DITTO_ACTIVITY:
		return "ping"
	case gmproto.ActionType_GET_UPDATES:
		return "updates"
	case gmproto.ActionType_ACK_BROWSER_PRESENCE:
		return "presence"
	default:
		return "other"
	}
}
