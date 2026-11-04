// Package racetest lets a few payload-heavy tests skip under the race detector.
// They move tens of megabytes through the pure-Go SQLite driver and image codecs,
// which race instrumentation slows 5-10 times (16-29 s each), without exercising
// any concurrency. They still run with a plain `go test ./...`.
package racetest

import "testing"

// SkipHeavy skips t when the race detector is on.
func SkipHeavy(t testing.TB) {
	t.Helper()
	if Enabled {
		t.Skip("payload-heavy test; runs without -race")
	}
}
