package transport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"go.mau.fi/mautrix-gmessages/pkg/libgm/gmproto"
)

func TestRefusalCarriesFixedReasonAndMatchesNotSubmitted(t *testing.T) {
	err := refuse("sim_missing")
	if !errors.Is(err, ErrNotSubmitted) || errors.Is(err, ErrUnknown) {
		t.Fatal("refusal must be a definite non-submission")
	}
	if RefusalReason(err) != "sim_missing" || RefusalReason(fmt.Errorf("wrapped: %w", err)) != "sim_missing" {
		t.Fatal("reason lost")
	}
	if RefusalReason(ErrNotSubmitted) != "" || RefusalReason(ErrUnknown) != "" || RefusalReason(nil) != "" {
		t.Fatal("reason invented")
	}
	f := NewFixture()
	f.Online = false
	if _, err = f.Send(context.Background(), "demo-chat", "txn", "synthetic"); RefusalReason(err) != "not_ready" {
		t.Fatal("offline fixture refusal has no reason", err)
	}
	g := &Google{}
	if _, err = g.Send(context.Background(), "synthetic-conversation", "txn", "synthetic"); RefusalReason(err) != "not_ready" {
		t.Fatal("offline Google refusal has no reason", err)
	}
	if err = g.React(context.Background(), "synthetic-conversation", "peer", "target", "👍", "add"); RefusalReason(err) != "not_ready" {
		t.Fatal("offline reaction refusal has no reason", err)
	}
}

func TestSIMsReportOnlySlotCarrierAndNumberEnding(t *testing.T) {
	g := &Google{}
	if _, ok := g.SIMs(); ok {
		t.Fatal("SIMs reported before the phone sent settings")
	}
	g.settings = &gmproto.Settings{SIMCards: []*gmproto.SIMCard{
		{SIMData: &gmproto.SIMData{SIMPayload: &gmproto.SIMPayload{SIMNumber: 1}, CarrierName: "Synthetic Mobile", FormattedPhoneNumber: "(555) 010-4321"}},
		{SIMData: &gmproto.SIMData{SIMPayload: &gmproto.SIMPayload{SIMNumber: 2}, InternationalPhoneNumber: "+1 555 010 9876"}},
		{SIMData: &gmproto.SIMData{SIMPayload: &gmproto.SIMPayload{SIMNumber: 3}, FormattedPhoneNumber: "12"}},
	}}
	sims, ok := g.SIMs()
	if !ok || len(sims) != 3 {
		t.Fatal("SIMs missing", sims)
	}
	want := []SIM{{Slot: 1, Carrier: "Synthetic Mobile", NumberEnding: "4321"}, {Slot: 2, NumberEnding: "9876"}, {Slot: 3}}
	for i := range want {
		if sims[i] != want[i] {
			t.Fatalf("SIM %d: got %+v want %+v", i, sims[i], want[i])
		}
	}
	b, _ := json.Marshal(sims)
	if strings.Contains(string(b), "010") || strings.Contains(string(b), "555") {
		t.Fatal("whole number exposed", string(b))
	}
	g.settings = &gmproto.Settings{}
	if sims, ok = g.SIMs(); !ok || len(sims) != 0 || sims == nil {
		t.Fatal("no SIMs must be reported as an empty list", sims)
	}
}
