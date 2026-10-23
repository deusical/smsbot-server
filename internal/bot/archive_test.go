package bot

import (
	"context"
	"errors"
	"github.com/deusical/smsbot-server/internal/history"
	"github.com/deusical/smsbot-server/internal/store"
	"github.com/deusical/smsbot-server/internal/transport"
	"testing"
	"time"
)

func TestArchiveConversationCreatesReusesAndRearchivesWithoutDeletingSavedHistory(t *testing.T) {
	e, f := setup(t)
	enableInbox(t, e)
	first, err := e.RequestArchive(context.Background(), "demo-chat", "", false)
	if err != nil || first.State != "queued" {
		t.Fatal("first archive", err)
	}
	first.State = "running"
	first.Cursor = "synthetic-checkpoint"
	first.Pages = 8
	first.Checked = 400
	if err = e.Store.SaveArchive(first, 0); err != nil {
		t.Fatal(err)
	}
	if err = e.Store.SaveObservedFiles("demo-chat", "saved-message", "", "incoming", "Synthetic saved text", "history", "", time.Now().Add(-time.Hour), nil); err != nil {
		t.Fatal(err)
	}
	type result struct {
		a   store.Archive
		err error
	}
	done := make(chan result, 2)
	for i := 0; i < 2; i++ {
		go func() {
			a, err := e.RequestArchive(context.Background(), "demo-chat", "", false)
			done <- result{a, err}
		}()
	}
	for i := 0; i < 2; i++ {
		r := <-done
		if r.err != nil || r.a.Generation != first.Generation || r.a.Cursor != first.Cursor || r.a.Pages != 8 || r.a.Checked != 400 {
			t.Fatal("repeat click reset active archive", r.err)
		}
	}
	first.State = "available_pages_exhausted"
	first.Cursor = ""
	if err = e.Store.SaveArchive(first, 0); err != nil {
		t.Fatal(err)
	}
	again, err := e.RequestArchive(context.Background(), "demo-chat", "", false)
	if err != nil || again.State != "queued" || again.Generation <= first.Generation || again.Pages != 0 {
		t.Fatal("completed archive did not rescan", err)
	}
	page, err := e.Store.OwnerMessagePage("demo-chat", "", "", 50)
	if err != nil || len(page.Messages) != 1 || len(f.Sent) != 0 {
		t.Fatal("rearchive deleted history or sent a message", err)
	}
}

func TestLiveCaptureDuringFetchWinsOverStaleSnapshot(t *testing.T) {
	e, f := setup(t)
	cfg := enableInbox(t, e)
	cfg.Scope = "selected"
	cfg.Conversations = []InboxBinding{{"demo-chat", "demo-peer"}}
	if err := e.SetInboxSettings(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	c := transport.Conversation{ID: "demo-chat", ParticipantID: "demo-peer", Writable: true}
	if err := e.Store.SaveArchive(store.Archive{Conversation: c, Generation: 1, State: "available_pages_exhausted"}, 0); err != nil {
		t.Fatal(err)
	}
	held := &heldArchiveTransport{Fixture: f, started: make(chan struct{}, 1), release: make(chan struct{})}
	e.Transport = held
	done := make(chan struct{})
	go func() { defer close(done); e.History.Tick(context.Background()) }()
	<-held.started
	captured := make(chan error, 1)
	go func() {
		captured <- e.Observe(context.Background(), transport.Incoming{ID: "fixture-history", ConversationID: "demo-chat", ParticipantID: "demo-peer", Text: "Newer observed content", At: time.Now().Add(-time.Hour), Historical: true})
	}()
	select {
	case err := <-captured:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		close(held.release)
		<-done
		t.Fatal("remote fetch blocked live capture")
	}
	close(held.release)
	<-done
	p, err := e.Store.OwnerMessagePage("demo-chat", "", "", 50)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range p.Messages {
		if m.ID == store.Hash("demo-chat\x00fixture-history") {
			found = true
			if m.Text != "Newer observed content" {
				t.Fatal("stale fetch replaced newer capture")
			}
		}
	}
	if !found {
		t.Fatal("live row missing")
	}
	a, err := e.Store.Archive("demo-chat")
	if err != nil || a.Skipped != 1 {
		t.Fatalf("watermark skip not counted: %d %v", a.Skipped, err)
	}
}

func TestArchiveOldestTimestampAndHeadlessRetention(t *testing.T) {
	e, _ := setup(t)
	cfg := enableInbox(t, e)
	c := transport.Conversation{ID: "demo-chat", ParticipantID: "demo-peer", Writable: true}
	a := store.Archive{Conversation: c, Generation: 1, State: "cancelled"}
	base := time.Now().AddDate(0, 0, -10).Truncate(time.Second)
	page := transport.HistoryPage{Messages: []transport.HistoricalMessage{{ID: "second", Direction: "incoming", Text: "Synthetic second", At: base}, {ID: "subsecond", Direction: "incoming", Text: "Synthetic fraction", At: base.Add(500 * time.Millisecond)}}}
	if err := (archiveHost{e}).Commit(context.Background(), a, history.Policy{Revision: cfg.Revision}, page, false); err != nil {
		t.Fatal(err)
	}
	a, _ = e.Store.Archive(c.ID)
	oldest, err := time.Parse(time.RFC3339Nano, a.Oldest)
	if err != nil || !oldest.Equal(base) {
		t.Fatal("fractional timestamp order incorrect", err)
	}
	cfg, _ = e.InboxSettings()
	cfg.ArchiveRetentionDays = 1
	cfg.Enabled = false
	if err = e.SetInboxSettings(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	e.History.Tick(context.Background())
	p, err := e.Store.OwnerMessagePage(c.ID, "", "", 50)
	if err != nil || len(p.Messages) != 0 {
		t.Fatal("headless retention did not expire archive", err)
	}
}

type expiredDiscoveryTransport struct{ *transport.Fixture }

func (t expiredDiscoveryTransport) DiscoverConversations(ctx context.Context, cursor string) (transport.ConversationPage, error) {
	if cursor != "" {
		return transport.ConversationPage{}, errors.New("invalid_conversation_cursor")
	}
	return t.Fixture.DiscoverConversations(ctx, cursor)
}
func TestArchiveDiscoveryRecoversExpiredCursor(t *testing.T) {
	e, f := setup(t)
	enableInbox(t, e)
	e.Transport = expiredDiscoveryTransport{f}
	if err := e.Store.SetSecretSetting("archive_discovery", "archive-discovery", []byte("expired")); err != nil {
		t.Fatal(err)
	}
	e.History.Tick(context.Background())
	cursor, err := e.Store.GetSecretSetting("archive_discovery", "archive-discovery")
	if err != nil || len(cursor) != 0 {
		t.Fatal("bad discovery cursor retained", err)
	}
	// A new worker retries the reset cursor; the running worker backs off failed discovery.
	e.History = history.New(e.Store, archiveHost{e})
	e.History.Tick(context.Background())
	if _, err = e.Store.Archive("demo-chat"); err != nil {
		t.Fatal("discovery did not recover", err)
	}
}

type heldArchiveTransport struct {
	*transport.Fixture
	started chan struct{}
	release chan struct{}
}

func (t *heldArchiveTransport) FetchHistory(ctx context.Context, conv, peer, cursor string) (transport.HistoryPage, error) {
	select {
	case t.started <- struct{}{}:
	default:
	}
	select {
	case <-ctx.Done():
		return transport.HistoryPage{}, ctx.Err()
	case <-t.release:
	}
	return t.Fixture.FetchHistory(ctx, conv, peer, cursor)
}
func TestArchiveCancelAndRevokeDuringFetch(t *testing.T) {
	for _, revoke := range []bool{false, true} {
		t.Run(map[bool]string{false: "cancel", true: "revoke"}[revoke], func(t *testing.T) {
			e, f := setup(t)
			cfg := enableInbox(t, e)
			cfg.Scope = "selected"
			cfg.Conversations = []InboxBinding{{"demo-chat", "demo-peer"}}
			if err := e.SetInboxSettings(context.Background(), cfg); err != nil {
				t.Fatal(err)
			}
			held := &heldArchiveTransport{Fixture: f, started: make(chan struct{}, 1), release: make(chan struct{})}
			e.Transport = held
			done := make(chan struct{})
			go func() { defer close(done); e.History.Tick(context.Background()) }()
			select {
			case <-held.started:
			case <-time.After(time.Second):
				t.Fatal("fetch did not start")
			}
			if revoke {
				cfg, _ = e.InboxSettings()
				cfg.Enabled = false
				if err := e.SetInboxSettings(context.Background(), cfg); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := e.RequestArchive(context.Background(), "demo-chat", "", true); err != nil {
					t.Fatal(err)
				}
			}
			close(held.release)
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("job did not finish")
			}
			p, err := e.Store.OwnerMessagePage("demo-chat", "", "", 50)
			if err != nil || len(p.Messages) != 0 {
				t.Fatal("obsolete fetch persisted content", err)
			}
		})
	}
}

func TestArchiveSyncRunsPausedAndNeverRoutesHistory(t *testing.T) {
	e, _ := setup(t)
	enableInbox(t, e)
	e.History.Tick(context.Background())
	a, err := e.Store.Archive("demo-chat")
	if err != nil || a.State != "available_pages_exhausted" {
		t.Fatal("durable import did not finish", err)
	}
	p, err := e.Store.OwnerMessagePage("demo-chat", "", "", 50)
	if err != nil || len(p.Messages) != 2 {
		t.Fatal("history missing", err)
	}
	jobs, _ := e.Store.Jobs()
	if len(jobs) != 0 {
		t.Fatal("history executed commands")
	}
	e.History.Tick(context.Background())
	p, _ = e.Store.OwnerMessagePage("demo-chat", "", "", 50)
	if len(p.Messages) != 2 {
		t.Fatal("duplicate import")
	}
	cancelled, err := e.RequestArchive(context.Background(), "demo-chat", "", true)
	if err != nil {
		t.Fatal(err)
	}
	old := a
	old.Generation = cancelled.Generation - 1
	settings, _ := e.InboxSettings()
	if err = (archiveHost{e}).Commit(context.Background(), old, history.Policy{Revision: settings.Revision}, transport.HistoryPage{}, true); err == nil {
		t.Fatal("cancelled fetch committed")
	}
}
func TestOldArchiveUpdateNeverRunsCommand(t *testing.T) {
	e, _ := setup(t)
	enableInbox(t, e)
	arm(t, e)
	m := incoming()
	m.ID = "old-message"
	m.At = time.Now().AddDate(0, -2, 0)
	m.Historical = true
	m.Text = "original"
	if _, err := e.Receive(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	m.Text = "!ping"
	if _, err := e.Receive(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	jobs, _ := e.Store.Jobs()
	if len(jobs) != 0 {
		t.Fatal("edited history executed")
	}
	p, _ := e.Store.OwnerMessagePage("demo-chat", "", "", 50)
	if len(p.Messages) != 1 || p.Messages[0].Text != "!ping" {
		t.Fatal("old edit missing")
	}
}

func TestEditOfOrdinaryFreshMessageCannotBecomeCommand(t *testing.T) {
	e, _ := setup(t)
	enableInbox(t, e)
	arm(t, e)
	m := incoming()
	m.Text = "ordinary original"
	if accepted, err := e.Receive(context.Background(), m); err != nil || accepted {
		t.Fatal("ordinary event routed", err)
	}
	m.Text = "!ping"
	if accepted, err := e.Receive(context.Background(), m); err != nil || accepted {
		t.Fatal("edit routed as command", err)
	}
	jobs, _ := e.Store.Jobs()
	if len(jobs) != 0 {
		t.Fatal("edited command queued")
	}
}
