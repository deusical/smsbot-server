package bot

import (
	"context"
	"encoding/json"
	"errors"
	"smsbot-server/internal/history"
	"smsbot-server/internal/store"
	"smsbot-server/internal/transport"
	"strings"
	"testing"
	"time"
)

type gapTransport struct {
	*transport.Fixture
	at          time.Time
	beforeFetch func()
	fail        bool
	calls       int
}

func (f *gapTransport) HistoryCursorBefore(time.Time) string { return "range-anchor" }
func (f *gapTransport) FetchHistory(context.Context, string, string, string) (transport.HistoryPage, error) {
	f.calls++
	if f.beforeFetch != nil {
		f.beforeFetch()
	}
	if f.fail {
		return transport.HistoryPage{}, errors.New("synthetic upstream details")
	}
	return transport.HistoryPage{Messages: []transport.HistoricalMessage{{ID: "synthetic-gap-message", Direction: "incoming", Text: "!ping", At: f.at}}}, nil
}
func seedGap(t *testing.T, e *Engine, at time.Time) {
	t.Helper()
	consent := e.captureConsent.Load()
	e.noteCaptureGap(transport.Incoming{ConversationID: "demo-chat", At: at, Text: "synthetic private body"}, consent.epoch, "capture_check_failed")
	e.flushCaptureGaps()
}
func TestCaptureGapPersistsRepairsAndNeverRunsCommands(t *testing.T) {
	e, f := setup(t)
	enableInbox(t, e)
	arm(t, e)
	at := time.Now().Add(-time.Minute)
	e.Transport = &gapTransport{Fixture: f, at: at}
	seedGap(t, e, at)
	if !e.InboxCaptureGap() {
		t.Fatal("pending gap missing")
	}
	restarted := New(e.Store, e.Transport)
	if !restarted.InboxCaptureGap() {
		t.Fatal("gap lost on restart")
	}
	if err := (archiveHost{e}).RepairCaptureGap(context.Background()); err != nil {
		t.Fatal(err)
	}
	gaps, unknown := e.CaptureGapStatus()
	if unknown || len(gaps) != 1 || gaps[0].State != "checked_available" || gaps[0].CheckedAt == "" || e.InboxCaptureGap() {
		t.Fatal("rechecked range did not resolve warning")
	}
	page, err := e.Store.OwnerMessagePage("demo-chat", "", "", 50)
	if err != nil || len(page.Messages) != 1 {
		t.Fatal("missing message not retained")
	}
	jobs, _ := e.Store.Jobs()
	paused, _ := e.Store.Paused()
	if len(jobs) != 0 || paused || len(f.Sent) != 0 {
		t.Fatal("repair affected routing or Live choice")
	}
	var raw string
	if err = e.Store.DB.QueryRow("SELECT value FROM settings WHERE key=?", "capture_gap:"+store.Hash("demo-chat")).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(raw, "demo-chat") || strings.Contains(raw, "capture_check_failed") || strings.Contains(raw, "private body") {
		t.Fatal("gap metadata persisted in plaintext")
	}
	b, _ := json.Marshal(gaps[0])
	if strings.Contains(string(b), "cursor") || strings.Contains(string(b), "generation") {
		t.Fatal("private checkpoint exposed")
	}
}
func TestNewCaptureFailureCannotBeClearedByOlderRepair(t *testing.T) {
	e, f := setup(t)
	enableInbox(t, e)
	at := time.Now().Add(-time.Minute)
	source := &gapTransport{Fixture: f, at: at}
	e.Transport = source
	seedGap(t, e, at)
	source.beforeFetch = func() { seedGap(t, e, at.Add(-time.Hour)) }
	if err := (archiveHost{e}).RepairCaptureGap(context.Background()); err != nil {
		t.Fatal(err)
	}
	gaps, _ := e.CaptureGapStatus()
	if len(gaps) != 1 || gaps[0].State == "checked_available" || !e.InboxCaptureGap() {
		t.Fatal("stale repair cleared newer gap")
	}
}
func TestCaptureRepairHonorsConsentRevocationAndRetries(t *testing.T) {
	for _, revoke := range []bool{false, true} {
		e, f := setup(t)
		enableInbox(t, e)
		at := time.Now().Add(-time.Minute)
		source := &gapTransport{Fixture: f, at: at, fail: !revoke}
		e.Transport = source
		seedGap(t, e, at)
		if revoke {
			source.beforeFetch = func() {
				cfg, _ := e.InboxSettings()
				cfg.Enabled = false
				if err := e.SetInboxSettings(context.Background(), cfg); err != nil {
					t.Fatal(err)
				}
			}
		}
		_ = (archiveHost{e}).RepairCaptureGap(context.Background())
		gaps, _ := e.CaptureGapStatus()
		if len(gaps) != 1 || gaps[0].State != "retrying" || gaps[0].RetryAt == "" {
			t.Fatal("failed/revoked check marked complete")
		}
		page, _ := e.Store.OwnerMessagePage("demo-chat", "", "", 50)
		if len(page.Messages) != 0 {
			t.Fatal("unverified phone data published")
		}
		_ = (archiveHost{e}).RepairCaptureGap(context.Background())
		if source.calls != 1 {
			t.Fatal("repair ignored backoff or revoked consent")
		}
	}
}
func TestCaptureGapAggregationIsBoundedAndUnknownScopeIsExplicit(t *testing.T) {
	e, _ := setup(t)
	enableInbox(t, e)
	epoch := e.captureConsent.Load().epoch
	for i := 0; i < 520; i++ {
		e.noteCaptureGap(transport.Incoming{ConversationID: store.ID(), At: time.Now()}, epoch, "capture_queue_full")
	}
	if len(e.pendingGaps) > 513 {
		t.Fatal("unbounded metadata queue")
	}
	_, unknown := e.CaptureGapStatus()
	if !unknown {
		t.Fatal("unknown scope hidden")
	}
}
func TestDeepProgressCountsCheckedNewAndUpdatedSeparately(t *testing.T) {
	e, _ := setup(t)
	cfg := enableInbox(t, e)
	at := time.Now().Add(-time.Minute)
	for _, id := range []string{"edited", "same"} {
		if err := e.Store.SaveObservedFiles("demo-chat", id, "", "incoming", "synthetic old", "history", "", at, nil); err != nil {
			t.Fatal(err)
		}
	}
	a := store.Archive{Conversation: transport.Conversation{ID: "demo-chat", ParticipantID: "demo-peer", Writable: true}, Generation: 1, State: "running"}
	page := transport.HistoryPage{Messages: []transport.HistoricalMessage{{ID: "edited", Direction: "incoming", Text: "synthetic new", At: at}, {ID: "same", Direction: "incoming", Text: "synthetic old", At: at}, {ID: "new", Direction: "incoming", Text: "synthetic added", At: at}}}
	if err := (archiveHost{e}).Commit(context.Background(), a, history.Policy{Revision: cfg.Revision, Deep: true}, page, false); err != nil {
		t.Fatal(err)
	}
	saved, _ := e.Store.Archive("demo-chat")
	if saved.Checked != 3 || saved.Imported != 1 || saved.Updated != 1 || saved.Duplicates != 1 {
		t.Fatalf("incorrect progress: checked=%d new=%d updated=%d unchanged=%d", saved.Checked, saved.Imported, saved.Updated, saved.Duplicates)
	}
	if err := (archiveHost{e}).Commit(context.Background(), saved, history.Policy{Revision: cfg.Revision}, page, false); err != nil {
		t.Fatal(err)
	}
	again, _ := e.Store.Archive("demo-chat")
	if again.Checked != saved.Checked || again.Imported != saved.Imported || again.Updated != saved.Updated {
		t.Fatal("view/recent page changed deep-import progress")
	}
}

func TestImportedSendAliasDoesNotCountAsNewSavedMessage(t *testing.T) {
	e, _ := setup(t)
	cfg := enableInbox(t, e)
	arm(t, e)
	if ok, err := e.Accept(context.Background(), incoming()); err != nil || !ok {
		t.Fatal("fixture command not accepted")
	}
	if err := e.Dispatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	var txn string
	if err := e.Store.DB.QueryRow("SELECT txn FROM jobs WHERE txn!='' LIMIT 1").Scan(&txn); err != nil {
		t.Fatal(err)
	}
	a, err := e.Store.Archive("demo-chat")
	if err != nil {
		a = store.Archive{Conversation: transport.Conversation{ID: "demo-chat", ParticipantID: "demo-peer", Writable: true}, State: "running", Generation: 1}
	}
	page := transport.HistoryPage{Messages: []transport.HistoricalMessage{{ID: "new-upstream-alias", TransactionID: txn, Direction: "outgoing", Text: "pong", At: time.Now(), Status: "submitted"}}}
	if err := (archiveHost{e}).Commit(context.Background(), a, history.Policy{Revision: cfg.Revision, Deep: true}, page, false); err != nil {
		t.Fatal(err)
	}
	saved, _ := e.Store.Archive("demo-chat")
	if saved.Imported != 0 || saved.Checked != 1 {
		t.Fatal("existing submitted intent counted as a new saved message")
	}
}
func TestRepairDoesNotFetchExcludedGroupsOrChangedParticipants(t *testing.T) {
	for _, group := range []bool{false, true} {
		e, f := setup(t)
		enableInbox(t, e)
		source := &gapTransport{Fixture: f, at: time.Now()}
		e.Transport = source
		if group {
			e.noteCaptureGap(transport.Incoming{ConversationID: "demo-group", At: time.Now()}, e.captureConsent.Load().epoch, "capture_check_failed")
			e.flushCaptureGaps()
		} else {
			seedGap(t, e, time.Now())
			if err := e.Store.SaveArchive(store.Archive{Conversation: transport.Conversation{ID: "demo-chat", ParticipantID: "old-peer", Writable: true}, State: "queued", Generation: 1}, 0); err != nil {
				t.Fatal(err)
			}
		}
		_ = (archiveHost{e}).RepairCaptureGap(context.Background())
		if source.calls != 0 {
			t.Fatal("repair fetched excluded group or changed participant")
		}
	}
}

func TestCaptureGapRemainsVisibleWhileWaitingToPersist(t *testing.T) {
	e, _ := setup(t)
	enableInbox(t, e)
	e.noteCaptureGap(transport.Incoming{ConversationID: "demo-chat", At: time.Now()}, e.captureConsent.Load().epoch, "capture_delayed")
	e.mu.Lock()
	done := make(chan struct{})
	go func() { defer close(done); e.flushCaptureGaps() }()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		e.gapMu.Lock()
		flushing := len(e.flushingGaps) > 0
		e.gapMu.Unlock()
		if flushing {
			break
		}
		time.Sleep(time.Millisecond)
	}
	gaps, _ := e.CaptureGapStatus()
	e.mu.Unlock()
	<-done
	if len(gaps) != 1 || gaps[0].State != "queued" {
		t.Fatal("pending warning disappeared during persistence")
	}
}

func TestSameRangeOwnerRequestPromotesAutomaticJob(t *testing.T) {
	e, _ := setup(t)
	enableInbox(t, e)
	a := store.Archive{Conversation: transport.Conversation{ID: "demo-chat", ParticipantID: "demo-peer", Writable: true}, State: "running", Generation: 3, Cursor: "existing-deep-page", Checked: 100}
	if err := e.Store.SaveArchive(a, 0); err != nil {
		t.Fatal(err)
	}
	requested, err := e.RequestArchive(context.Background(), "demo-chat", "", false)
	if err != nil || requested.RequestedAt == "" || requested.Cursor != a.Cursor || requested.Checked != 100 || requested.Generation <= a.Generation {
		t.Fatal("owner click did not promote automatic job safely")
	}
	repeat, err := e.RequestArchive(context.Background(), "demo-chat", "", false)
	if err != nil || repeat.Generation != requested.Generation {
		t.Fatal("repeat owner request restarted the active job")
	}
}
