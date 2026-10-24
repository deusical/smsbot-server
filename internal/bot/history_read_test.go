package bot

import (
	"context"
	"github.com/deusical/smsbot-server/internal/store"
	"github.com/deusical/smsbot-server/internal/transport"
	"testing"
	"time"
)

// racingHistory saves a newer live observation while the page is in flight.
type racingHistory struct {
	*transport.Fixture
	during func()
}

func (r *racingHistory) FetchHistory(ctx context.Context, conv, peer, cursor string) (transport.HistoryPage, error) {
	if r.during != nil {
		r.during()
	}
	return r.Fixture.FetchHistory(ctx, conv, peer, cursor)
}

func historyJobs(t *testing.T, e *Engine) int {
	t.Helper()
	jobs, err := e.Store.Jobs()
	if err != nil {
		t.Fatal(err)
	}
	return len(jobs)
}

// The read sequence is shared, but each route keeps its own grant: enrolled
// text history needs enrollment only, collected inbox history needs collection
// consent only. Neither routes commands or changes Live/Pause.
func TestHistoryEntryPointsKeepTheirOwnGrants(t *testing.T) {
	t.Run("enrolled without collection", func(t *testing.T) {
		e, _ := setup(t)
		if err := e.Store.Enroll("demo-chat", "demo-peer"); err != nil {
			t.Fatal(err)
		}
		paused, _ := e.Store.Paused()
		got, err := e.ImportHistory(context.Background(), "demo-chat", "")
		if err != nil || got["imported"] != 1 || got["text_only"] != true {
			t.Fatal("enrolled text import failed", got, err)
		}
		if _, err = e.ImportInboxHistory(context.Background(), "demo-chat", ""); err == nil {
			t.Fatal("enrollment granted collected inbox history")
		}
		if after, _ := e.Store.Paused(); after != paused || historyJobs(t, e) != 0 {
			t.Fatal("history import changed Live/Pause or routed a command")
		}
	})
	t.Run("collection without enrollment", func(t *testing.T) {
		e, _ := setup(t)
		enableInbox(t, e)
		if _, err := e.ImportHistory(context.Background(), "demo-chat", ""); err == nil {
			t.Fatal("collection consent granted enrolled bot history")
		}
		got, err := e.ImportInboxHistory(context.Background(), "demo-chat", "")
		if err != nil || got["imported"] != 2 || got["text_only"] != false {
			t.Fatal("collected import failed", got, err)
		}
		if historyJobs(t, e) != 0 {
			t.Fatal("history page routed a command")
		}
		again, err := e.ImportInboxHistory(context.Background(), "demo-chat", "")
		if err != nil || again["imported"] != 0 || again["duplicates"] != 2 {
			t.Fatal("repeated page counted as new", again, err)
		}
	})
	t.Run("newer observation during the fetch", func(t *testing.T) {
		for _, route := range []string{"inbox", "enrolled"} {
			e, f := setup(t)
			enableInbox(t, e)
			if err := e.Store.Enroll("demo-chat", "demo-peer"); err != nil {
				t.Fatal(err)
			}
			at := time.Now().Add(-time.Hour)
			e.Transport = &racingHistory{Fixture: f, during: func() {
				if err := e.Store.SaveObservedFiles("demo-chat", "fixture-history", "", "incoming", "Newer observed content", "phone", "", at, nil); err != nil {
					t.Fatal(err)
				}
			}}
			var err error
			if route == "inbox" {
				var got map[string]any
				got, err = e.ImportInboxHistory(context.Background(), "demo-chat", "")
				if err == nil && got["skipped"] != 1 {
					t.Fatal("concurrent newer message not skipped", got)
				}
			} else {
				_, err = e.ImportHistory(context.Background(), "demo-chat", "")
			}
			if err != nil {
				t.Fatal(route, err)
			}
			page, err := e.Store.OwnerMessagePage("demo-chat", "", "", 50)
			if err != nil {
				t.Fatal(err)
			}
			for _, m := range page.Messages {
				if m.ID == store.Hash("demo-chat\x00fixture-history") && m.Text != "Newer observed content" {
					t.Fatal(route, "history page replaced a newer observation")
				}
			}
		}
	})
}
