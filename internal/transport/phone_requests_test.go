package transport

import (
	"testing"
	"time"
)

func TestPhoneRequestCountsTotalAndLastHour(t *testing.T) {
	var p phoneRequests
	start := time.Unix(1_800_000_000, 0)
	p.add(phoneList, start)
	p.add(phoneList, start.Add(30*time.Minute))
	p.add(phoneMessages, start.Add(59*time.Minute))
	got := p.snapshot(start.Add(59 * time.Minute))
	if got[phoneList] != (PhoneRequestCount{Total: 2, LastHour: 2}) || got[phoneMessages] != (PhoneRequestCount{Total: 1, LastHour: 1}) {
		t.Fatalf("counts %+v", got)
	}
	if len(got) != len(phoneKinds) || got[phoneSend] != (PhoneRequestCount{}) {
		t.Fatal("snapshot does not report every kind")
	}
	got = p.snapshot(start.Add(80 * time.Minute))
	if got[phoneList] != (PhoneRequestCount{Total: 2, LastHour: 1}) || got[phoneMessages].LastHour != 1 {
		t.Fatalf("expired minutes counted %+v", got)
	}
	// Reusing a minute slot an hour later replaces the old count.
	p.add(phoneList, start.Add(time.Hour))
	got = p.snapshot(start.Add(time.Hour))
	if got[phoneList] != (PhoneRequestCount{Total: 3, LastHour: 2}) {
		t.Fatalf("reused slot %+v", got)
	}
}

func TestPhoneRequestCountsIgnoreSlotsAheadOfABackwardClock(t *testing.T) {
	var p phoneRequests
	now := time.Unix(1_800_000_000, 0)
	p.add(phoneList, now.Add(10*time.Minute))
	if got := p.snapshot(now)[phoneList]; got != (PhoneRequestCount{Total: 1}) {
		t.Fatalf("future slot counted in the last hour %+v", got)
	}
}

func TestPhoneRequestCountsSurviveAClockBefore1970(t *testing.T) {
	var p phoneRequests
	before := time.Unix(-90, 0)
	p.add(phoneList, before)
	if got := p.snapshot(before)[phoneList]; got != (PhoneRequestCount{Total: 1, LastHour: 1}) {
		t.Fatalf("counts %+v", got)
	}
}
