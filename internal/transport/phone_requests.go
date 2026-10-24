package transport

import (
	"context"
	"sync"
	"time"
)

// Phone request kinds. Counts hold no identifiers or content; they show how
// often the service wakes the paired phone, for battery diagnostics.
const (
	phoneList     = "list"
	phoneLookup   = "conversation"
	phoneMessages = "messages"
	phoneFullSize = "full_size"
	phoneMarkRead = "mark_read"
	phoneSend     = "send"
	phoneReact    = "react"
	phoneProbe    = "probe"
)

var phoneKinds = []string{phoneList, phoneLookup, phoneMessages, phoneFullSize, phoneMarkRead, phoneSend, phoneReact, phoneProbe}

// PhoneRequestCount is one request kind's total since start and in the last hour.
type PhoneRequestCount struct {
	Total    uint64 `json:"total"`
	LastHour uint64 `json:"last_hour"`
}

// PhoneRequestReporter exposes in-memory request counts; they reset on restart.
type PhoneRequestReporter interface {
	PhoneRequests() map[string]PhoneRequestCount
}

type phoneMinute struct {
	minute int64
	count  uint64
}
type phoneCounter struct {
	total   uint64
	minutes [60]phoneMinute
}
type phoneRequests struct {
	mu    sync.Mutex
	kinds map[string]*phoneCounter
}

func (p *phoneRequests) add(kind string, now time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.kinds == nil {
		p.kinds = map[string]*phoneCounter{}
	}
	c := p.kinds[kind]
	if c == nil {
		c = &phoneCounter{}
		p.kinds[kind] = c
	}
	c.total++
	minute := now.Unix() / 60
	slot := &c.minutes[(minute%60+60)%60] // a clock before 1970 gives negative minutes
	if slot.minute != minute {
		*slot = phoneMinute{minute: minute}
	}
	slot.count++
}

func (p *phoneRequests) snapshot(now time.Time) map[string]PhoneRequestCount {
	return p.snapshotKinds(now, phoneKinds)
}
func (p *phoneRequests) snapshotKinds(now time.Time, kinds []string) map[string]PhoneRequestCount {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make(map[string]PhoneRequestCount, len(kinds))
	current := now.Unix() / 60
	for _, kind := range kinds {
		var count PhoneRequestCount
		if c := p.kinds[kind]; c != nil {
			count.Total = c.total
			for _, slot := range c.minutes {
				if age := current - slot.minute; slot.count > 0 && age >= 0 && age < 60 {
					count.LastHour += slot.count
				}
			}
		}
		out[kind] = count
	}
	return out
}

// PhoneRequestLog is one adapter request to the phone, as written to the
// service log. It holds only bounded labels: never conversation or message
// identifiers, content, credentials or upstream errors.
type PhoneRequestLog struct {
	At      string `json:"at"`
	Kind    string `json:"kind"`
	Purpose string `json:"purpose"`
}

func (g *Google) countPhone(ctx context.Context, kind string) {
	now := time.Now()
	purpose := phonePurpose(ctx)
	g.phoneRequests.add(kind, now)
	g.phonePurposes.add(purpose, now)
	if g.RequestLog != nil {
		g.RequestLog(PhoneRequestLog{At: now.UTC().Format(time.RFC3339Nano), Kind: kind, Purpose: purpose})
	}
}

func (g *Google) PhoneRequests() map[string]PhoneRequestCount {
	return g.phoneRequests.snapshot(time.Now())
}

type phonePurposeKey struct{}

var phonePurposes = []string{"other", "discovery", "capture", "recent", "backfill", "view", "gap", "original", "readiness", "send", "reaction", "read"}

func WithPhonePurpose(ctx context.Context, purpose string) context.Context {
	for _, allowed := range phonePurposes {
		if purpose == allowed {
			return context.WithValue(ctx, phonePurposeKey{}, purpose)
		}
	}
	return context.WithValue(ctx, phonePurposeKey{}, "other")
}
func phonePurpose(ctx context.Context) string {
	if p, ok := ctx.Value(phonePurposeKey{}).(string); ok {
		return p
	}
	return "other"
}

// Purpose counts are adapter RPC attempts, not radio wakes or libgm's internal
// control RPCs. Existing operation counters remain backward compatible.
type PhonePurposeReporter interface {
	PhoneRequestPurposes() map[string]PhoneRequestCount
}

func (g *Google) PhoneRequestPurposes() map[string]PhoneRequestCount {
	return g.phonePurposes.snapshotKinds(time.Now(), phonePurposes)
}
