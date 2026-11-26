package bot

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/deusical/smsbot-server/internal/collection"
	"github.com/deusical/smsbot-server/internal/store"
	"github.com/deusical/smsbot-server/internal/transport"
	"github.com/deusical/smsbot-server/internal/vault"
)

func TestDispatchTimingAndDiagnostics(t *testing.T) {
	t.Parallel()
	v, _ := vault.New(make([]byte, 32))
	s, err := store.Open(filepath.Join(t.TempDir(), "test.db"), v)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.DB.Close() })

	f := transport.NewFixture()
	control := new(sync.Mutex)
	c := collection.New(s, f, control)
	e := New(s, f, c, control)

	var recorded []DispatchDiagnostic
	e.DispatchLog = func(d DispatchDiagnostic) {
		recorded = append(recorded, d)
	}

	if err := e.Dispatch(context.Background()); err != nil {
		t.Fatal(err)
	}

	if len(recorded) != 1 {
		t.Fatalf("expected 1 dispatch diagnostic, got %d", len(recorded))
	}

	d := recorded[0]
	if _, err := time.Parse(time.RFC3339Nano, d.At); err != nil {
		t.Fatalf("invalid timestamp: %v", err)
	}
	if d.DispatchWaitMs < 0 || d.DispatchHoldMs < 0 || d.ControlWaitMs < 0 || d.ControlHoldMs < 0 {
		t.Fatalf("negative duration: %+v", d)
	}

	// Verify no message text or private data in JSON serialization
	b, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "message") || strings.Contains(string(b), "body") {
		t.Fatalf("diagnostic exposed sensitive field: %s", string(b))
	}

	stats := e.DispatchTiming()
	if stats.TotalRuns != 1 {
		t.Fatalf("expected TotalRuns=1, got %+v", stats)
	}
}
