package bot

import (
	"context"
	"errors"
	"github.com/deusical/smsbot-server/internal/store"
	"github.com/deusical/smsbot-server/internal/transport"
	"sync"
	"testing"
	"time"
)

type lookupTransport struct {
	*transport.Fixture
	lookup func(context.Context, string) (transport.Conversation, error)
}

func (f *lookupTransport) LookupConversation(ctx context.Context, id string) (transport.Conversation, error) {
	return f.lookup(ctx, id)
}
func fixtureConversation(ctx context.Context, id string) (transport.Conversation, error) {
	f := transport.NewFixture()
	list, err := f.Conversations(ctx)
	if err != nil {
		return transport.Conversation{}, err
	}
	for _, c := range list {
		if c.ID == id {
			return c, nil
		}
	}
	return transport.Conversation{}, errors.New("synthetic_missing")
}
func assertPauseResponsive(t *testing.T, e *Engine) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- e.Pause(true) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("remote inbox operation blocked pause")
	}
}

func TestSettingsValidationDoesNotBlockPauseOrOverwriteNewRevision(t *testing.T) {
	e, f := setup(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	e.Transport = &lookupTransport{f, func(ctx context.Context, id string) (transport.Conversation, error) {
		once.Do(func() { close(entered) })
		select {
		case <-ctx.Done():
			return transport.Conversation{}, ctx.Err()
		case <-release:
			return fixtureConversation(ctx, id)
		}
	}}
	s, _ := e.InboxSettings()
	s.Enabled = true
	s.Scope = "selected"
	s.Conversations = []InboxBinding{{"demo-chat", "demo-peer"}}
	done := make(chan error, 1)
	go func() { done <- e.SetInboxSettings(context.Background(), s) }()
	<-entered
	assertPauseResponsive(t, e)
	newer, _ := e.InboxSettings()
	newer.Enabled = false
	if err := e.SetInboxSettings(context.Background(), newer); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-done; err == nil {
		t.Fatal("slow validation overwrote a newer settings revision")
	}
}
func TestSettingsValidationHasOneOverallDeadline(t *testing.T) {
	e, f := setup(t)
	var deadlines []time.Time
	e.Transport = &lookupTransport{f, func(ctx context.Context, id string) (transport.Conversation, error) {
		d, ok := ctx.Deadline()
		if !ok {
			t.Fatal("unbounded lookup")
		}
		deadlines = append(deadlines, d)
		time.Sleep(30 * time.Millisecond)
		return fixtureConversation(ctx, id)
	}}
	s, _ := e.InboxSettings()
	s.Enabled = true
	s.Scope = "selected"
	s.Conversations = []InboxBinding{{"demo-chat", "demo-peer"}, {"demo-sms", "demo-sms-peer"}}
	if err := e.SetInboxSettings(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	if len(deadlines) != 2 || !deadlines[0].Equal(deadlines[1]) {
		t.Fatal("lookup deadlines extend the total validation window")
	}
}
func TestAsyncCaptureReturnsBeforeLookupAndRechecksConsent(t *testing.T) {
	e, f := setup(t)
	s := enableInbox(t, e)
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	e.Transport = &lookupTransport{f, func(ctx context.Context, id string) (transport.Conversation, error) {
		once.Do(func() { close(entered) })
		select {
		case <-ctx.Done():
			return transport.Conversation{}, ctx.Err()
		case <-release:
			return fixtureConversation(ctx, id)
		}
	}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); e.RunCapture(ctx) }()
	m := incoming()
	m.Text = "Synthetic ordinary async text"
	received := make(chan error, 1)
	go func() { _, err := e.Ingest(ctx, m); received <- err }()
	select {
	case err := <-received:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("transport event consumer blocked on capture lookup")
	}
	<-entered
	assertPauseResponsive(t, e)
	s.Enabled = false
	if err := e.SetInboxSettings(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	close(release)
	cancel()
	<-done
	p, _ := e.Store.OwnerMessagePage("demo-chat", "", "", 50)
	if len(p.Messages) != 0 {
		t.Fatal("capture committed after consent revocation")
	}
}
func TestCaptureFailureReportsGapWithoutPausingAutomation(t *testing.T) {
	e, f := setup(t)
	enableInbox(t, e)
	e.Pause(false)
	e.Transport = &lookupTransport{f, func(context.Context, string) (transport.Conversation, error) {
		return transport.Conversation{}, errors.New("synthetic_transient")
	}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); e.RunCapture(ctx) }()
	defer func() { cancel(); <-done }()
	m := incoming()
	m.Text = "Synthetic ordinary text"
	if _, err := e.Ingest(ctx, m); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for !e.InboxCaptureGap() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	paused, err := e.Store.Paused()
	if err != nil || paused || !e.InboxCaptureGap() {
		t.Fatal("optional failure paused automation or failed to report a capture gap")
	}
}

type slowHistoryTransport struct {
	*transport.Fixture
	entered, release chan struct{}
}

func (f *slowHistoryTransport) FetchHistory(ctx context.Context, conv, peer, cursor string) (transport.HistoryPage, error) {
	close(f.entered)
	select {
	case <-ctx.Done():
		return transport.HistoryPage{}, ctx.Err()
	case <-f.release:
		return f.Fixture.FetchHistory(ctx, conv, peer, cursor)
	}
}
func TestHistoryFetchDoesNotBlockPauseAndRechecksRevocation(t *testing.T) {
	e, f := setup(t)
	s := enableInbox(t, e)
	source := &slowHistoryTransport{f, make(chan struct{}), make(chan struct{})}
	e.Transport = source
	done := make(chan error, 1)
	go func() { _, err := e.ImportInboxHistory(context.Background(), "demo-chat", ""); done <- err }()
	<-source.entered
	assertPauseResponsive(t, e)
	s.LoadOlder = false
	if err := e.SetInboxSettings(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	close(source.release)
	if err := <-done; err == nil {
		t.Fatal("history committed after disabling imports")
	}
	p, _ := e.Store.OwnerMessagePage("demo-chat", "", "", 50)
	if len(p.Messages) != 0 {
		t.Fatal("revoked history persisted")
	}
}
func TestComposeRetrySurvivesPauseAndRejectsChangedPayload(t *testing.T) {
	e, _ := setup(t)
	enableInbox(t, e)
	e.Pause(false)
	id, err := e.ComposeInbox(context.Background(), "demo-chat", "Synthetic reply", "synthetic-retry-request")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.ComposeInbox(context.Background(), "demo-chat", "Changed reply", "synthetic-retry-request"); err == nil {
		t.Fatal("request ID reused with changed message")
	}
	e.Pause(true)
	again, err := e.ComposeInbox(context.Background(), "demo-chat", "Synthetic reply", "synthetic-retry-request")
	if err != nil || again != id {
		t.Fatal("lost acknowledgement could not be recovered while paused", err)
	}
	jobs, _ := e.Store.Jobs()
	if len(jobs) != 1 {
		t.Fatal("retry queued a second job")
	}
}
func TestDelayedReceiptAdvancesChatStatusWithoutDowngrade(t *testing.T) {
	e, _ := setup(t)
	enableInbox(t, e)
	e.Pause(false)
	id, err := e.ComposeInbox(context.Background(), "demo-chat", "Synthetic reply", "synthetic-receipt-request")
	if err != nil {
		t.Fatal(err)
	}
	if err = e.Dispatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	j, _, _ := e.Store.JobPlan(id)
	for _, state := range []string{"delivered", "read", "submitted"} {
		if err = e.Reconcile("demo-chat", j.TransactionID, state, "synthetic"); err != nil {
			t.Fatal(err)
		}
	}
	p, _ := e.Store.OwnerMessagePage("demo-chat", "", "", 50)
	if len(p.Messages) != 1 || p.Messages[0].Status != "read" {
		t.Fatal("chat receipt was stale or downgraded")
	}
}

type sentHistoryTransport struct {
	*transport.Fixture
	txn string
}

func (f *sentHistoryTransport) FetchHistory(ctx context.Context, conv, peer, cursor string) (transport.HistoryPage, error) {
	return transport.HistoryPage{Messages: []transport.HistoricalMessage{{ID: "synthetic-sent-upstream", TransactionID: f.txn, Direction: "outgoing", Text: "Synthetic sent reply", At: time.Now(), Status: "delivered"}}}, nil
}
func TestLegacySendThenRepeatedHistoryImportHasOneRowAndOneSend(t *testing.T) {
	e, f := setup(t)
	e.Store.Enroll("demo-chat", "demo-peer")
	e.Pause(false)
	id, err := e.Compose(context.Background(), "demo-chat", "Synthetic sent reply")
	if err != nil {
		t.Fatal(err)
	}
	if err = e.Dispatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	j, _, _ := e.Store.JobPlan(id)
	e.Transport = &sentHistoryTransport{f, j.TransactionID}
	for i := 0; i < 2; i++ {
		if _, err = e.ImportHistory(context.Background(), "demo-chat", ""); err != nil {
			t.Fatal(err)
		}
	}
	p, err := e.Store.MessagePage("demo-chat", "", "", 50)
	if err != nil || len(p.Messages) != 1 || p.Messages[0].ID != j.TransactionID {
		t.Fatal("send/import produced duplicate rows", err)
	}
	jobs, _ := e.Store.Jobs()
	if len(jobs) != 1 {
		t.Fatal("history created another send job")
	}
}

type earlyEchoTransport struct {
	*transport.Fixture
	store *store.Store
}

func (f *earlyEchoTransport) Send(ctx context.Context, conv, txn, text string) (transport.Receipt, error) {
	if err := f.store.SaveObserved(conv, "synthetic-early-echo", txn, "outgoing", text, "phone", "read", time.Now()); err != nil {
		return transport.Receipt{}, err
	}
	return f.Fixture.Send(ctx, conv, txn, text)
}
func TestEarlyEchoStatusSurvivesSubmissionPersistence(t *testing.T) {
	e, f := setup(t)
	enableInbox(t, e)
	e.Transport = &earlyEchoTransport{f, e.Store}
	e.Pause(false)
	if _, err := e.ComposeInbox(context.Background(), "demo-chat", "Synthetic early echo", "synthetic-early-request"); err != nil {
		t.Fatal(err)
	}
	if err := e.Dispatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	p, err := e.Store.OwnerMessagePage("demo-chat", "", "", 50)
	if err != nil || len(p.Messages) != 1 || p.Messages[0].Status != "read" || p.Messages[0].Origin != "web" {
		t.Fatal("early echo status/provenance lost", err)
	}
}
