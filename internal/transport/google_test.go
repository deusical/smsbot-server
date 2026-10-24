package transport

import (
	"testing"
	"time"

	"go.mau.fi/mautrix-gmessages/pkg/libgm"
	"go.mau.fi/mautrix-gmessages/pkg/libgm/gmproto"
)

func TestCookieParserNeverExecutesInput(t *testing.T) {
	valid := `{"SID":"fixture","HSID":"fixture","SSID":"fixture","OSID":"fixture","APISID":"fixture","SAPISID":"fixture","unrelated":"discard"}`
	c, e := ParseCookies(valid)
	if e != nil || len(c) != 6 {
		t.Fatal("valid synthetic cookies rejected")
	}
	curl := `curl https://example.com -H 'cookie: SID=fixture; HSID=fixture; SSID=fixture; OSID=fixture; APISID=fixture; SAPISID=fixture'`
	if _, e = ParseCookies(curl); e != nil {
		t.Fatal("cURL parser failed")
	}
	if _, e = ParseCookies(`echo arbitrary command`); e == nil {
		t.Fatal("invalid input accepted")
	}
}
func TestIncomingNormalization(t *testing.T) {
	now := time.Now()
	m := &libgm.WrappedMessage{Message: &gmproto.Message{MessageID: "fixture-id", ConversationID: "fixture-chat", ParticipantID: "fixture-peer", Timestamp: now.UnixMicro(), MessageStatus: &gmproto.MessageStatus{Status: gmproto.MessageStatusType_INCOMING_COMPLETE}, MessageInfo: []*gmproto.MessageInfo{{Data: &gmproto.MessageInfo_MessageContent{MessageContent: &gmproto.MessageContent{Content: "!ping"}}}}}}
	if in, ok := normalize(m, now.Add(-time.Second)); !ok || in.Text != "!ping" {
		t.Fatal("incoming text rejected")
	}
	m.IsOld = true
	if _, ok := normalize(m, now.Add(-time.Second)); ok {
		t.Fatal("old update accepted")
	}
	m.IsOld = false
	m.MessageStatus.Status = gmproto.MessageStatusType_OUTGOING_COMPLETE
	if out, ok := normalize(m, now.Add(-time.Second)); !ok || !out.Outgoing {
		t.Fatal("outgoing echo was not marked as display-only outgoing")
	}
	m.MessageStatus.Status = gmproto.MessageStatusType_INCOMING_COMPLETE
	if _, ok := normalize(m, now.Add(time.Second)); ok {
		t.Fatal("startup replay accepted")
	}
	m.MessageInfo[0] = &gmproto.MessageInfo{}
	if _, ok := normalize(m, now.Add(-time.Second)); ok {
		t.Fatal("nontext accepted")
	}
}
