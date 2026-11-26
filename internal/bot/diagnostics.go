package bot

import (
	"sync"
)

// DispatchDiagnostic contains timing fields for a single dispatch pass.
// Like SendDiagnostic, it deliberately excludes message bodies, recipient/transaction IDs, and raw errors.
type DispatchDiagnostic struct {
	At              string `json:"at"`
	DispatchWaitMs  int64  `json:"dispatch_wait_ms,omitempty"`
	DispatchHoldMs  int64  `json:"dispatch_hold_ms,omitempty"`
	ControlWaitMs   int64  `json:"control_wait_ms,omitempty"`
	ControlHoldMs   int64  `json:"control_hold_ms,omitempty"`
	LookupLatencyMs int64  `json:"lookup_latency_ms,omitempty"`
	DBWaitMs        int64  `json:"db_wait_ms,omitempty"`
	Result          string `json:"result,omitempty"`
}

// DispatchTimingStats accumulates timing totals across runs.
type DispatchTimingStats struct {
	TotalRuns         uint64 `json:"total_runs"`
	TotalDispatchWait int64  `json:"total_dispatch_wait_ms"`
	TotalDispatchHold int64  `json:"total_dispatch_hold_ms"`
	TotalControlWait  int64  `json:"total_control_wait_ms"`
	TotalControlHold  int64  `json:"total_control_hold_ms"`
	TotalLookupTime   int64  `json:"total_lookup_ms"`
	TotalDBWaitTime   int64  `json:"total_db_wait_ms"`
}

type dispatchMetrics struct {
	mu    sync.Mutex
	stats DispatchTimingStats
}

func (m *dispatchMetrics) record(d DispatchDiagnostic) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stats.TotalRuns++
	m.stats.TotalDispatchWait += d.DispatchWaitMs
	m.stats.TotalDispatchHold += d.DispatchHoldMs
	m.stats.TotalControlWait += d.ControlWaitMs
	m.stats.TotalControlHold += d.ControlHoldMs
	m.stats.TotalLookupTime += d.LookupLatencyMs
	m.stats.TotalDBWaitTime += d.DBWaitMs
}

func (m *dispatchMetrics) snapshot() DispatchTimingStats {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.stats
}
