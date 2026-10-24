package bot

import (
	"context"
	"github.com/deusical/smsbot-server/internal/collection"
	"github.com/deusical/smsbot-server/internal/store"
	"github.com/deusical/smsbot-server/internal/transport"
	"sync"
	"testing"
	"time"
)

func enableInbox(t *testing.T, e *Engine) collection.Settings {
	t.Helper()
	s, _ := e.Collection.Settings()
	s.Enabled = true
	s.LoadOlder = true
	if err := e.Collection.SetSettings(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	s, _ = e.Collection.Settings()
	return s
}
func TestInboxCapturesWhilePausedWithoutExecuting(t *testing.T) {
	e, _ := setup(t)
	enableInbox(t, e)
	m := incoming()
	m.Text = "Synthetic ordinary message"
	accepted, err := e.Receive(context.Background(), m)
	if err != nil || accepted {
		t.Fatalf("receive=%v err=%v", accepted, err)
	}
	page, err := e.Collection.InboxPage(context.Background(), m.ConversationID, "", "", 50)
	if err != nil || len(page.Messages) != 1 {
		t.Fatal("paused capture missing", err)
	}
	legacy, _ := e.Store.MessagePage(m.ConversationID, "", "", 50)
	if len(legacy.Messages) != 0 {
		t.Fatal("private capture exposed to legacy clients")
	}
	jobs, _ := e.Store.Jobs()
	if len(jobs) != 0 {
		t.Fatal("ordinary message executed")
	}
	m.ID = "synthetic-command"
	m.Text = "!ping"
	e.Receive(context.Background(), m)
	jobs, _ = e.Store.Jobs()
	if len(jobs) != 0 {
		t.Fatal("paused command executed")
	}
}
func TestInboxSelectedIdentityAndDisableBoundaries(t *testing.T) {
	e, _ := setup(t)
	s, _ := e.Collection.Settings()
	s.Enabled = true
	s.Scope = "selected"
	s.Conversations = []collection.Binding{{Conversation: "demo-chat", Participant: "demo-peer"}}
	if err := e.Collection.SetSettings(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	for _, edit := range []func(*transport.Incoming){func(m *transport.Incoming) { m.ParticipantID = "wrong" }, func(m *transport.Incoming) { m.ConversationID = "demo-group" }, func(m *transport.Incoming) { m.At = time.Now().Add(-time.Hour) }} {
		m := incoming()
		edit(&m)
		if err := e.Collection.Observe(context.Background(), m); err != nil {
			t.Fatal(err)
		}
	}
	p, _ := e.Store.OwnerMessagePage("demo-chat", "", "", 50)
	if len(p.Messages) != 0 {
		t.Fatal("ineligible message retained")
	}
	s, _ = e.Collection.Settings()
	s.Enabled = false
	e.Collection.SetSettings(context.Background(), s)
	e.Collection.Observe(context.Background(), incoming())
	p, _ = e.Store.OwnerMessagePage("demo-chat", "", "", 50)
	if len(p.Messages) != 0 {
		t.Fatal("capture after disabling")
	}
}
func TestInboxManualSendDeduplicatesAndRechecksRevocation(t *testing.T) {
	e, f := setup(t)
	s := enableInbox(t, e)
	s.AutomaticReplies = false
	e.Collection.SetSettings(context.Background(), s)
	e.Pause(false)
	id, err := e.ComposeInbox(context.Background(), "demo-chat", "Synthetic manual reply", "synthetic-request-1")
	if err != nil {
		t.Fatal(err)
	}
	again, err := e.ComposeInbox(context.Background(), "demo-chat", "Synthetic manual reply", "synthetic-request-1")
	if err != nil || id != again {
		t.Fatal("double-click queued twice")
	}
	if err = e.Dispatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(f.Sent) != 1 {
		t.Fatal("manual reply unavailable without enrollment or automation")
	}
	legacy, _ := e.Store.MessagePage("demo-chat", "", "", 50)
	if len(legacy.Messages) != 0 {
		t.Fatal("manual inbox reply leaked")
	}
	e.ComposeInbox(context.Background(), "demo-chat", "Must be cancelled", "synthetic-request-2")
	s, _ = e.Collection.Settings()
	s.Enabled = false
	e.Collection.SetSettings(context.Background(), s)
	e.Dispatch(context.Background())
	if len(f.Sent) != 1 {
		t.Fatal("send after inbox revocation")
	}
}
func TestInboxHistoryNeverExecutesAndSettingIsIndependent(t *testing.T) {
	e, _ := setup(t)
	s := enableInbox(t, e)
	e.Pause(false)
	if _, err := e.Collection.ImportInboxHistory(context.Background(), "demo-chat", ""); err != nil {
		t.Fatal(err)
	}
	jobs, _ := e.Store.Jobs()
	if len(jobs) != 0 {
		t.Fatal("imported command executed")
	}
	p, _ := e.Store.OwnerMessagePage("demo-chat", "", "", 50)
	if len(p.Messages) != 2 || len(p.Messages[1].Attachments) != 1 {
		t.Fatal("history missing text or file")
	}
	s.LoadOlder = false
	e.Collection.SetSettings(context.Background(), s)
	if _, err := e.Collection.ImportInboxHistory(context.Background(), "demo-chat", ""); err == nil {
		t.Fatal("history fetch continued after disable")
	}
}
func TestInboxConcurrentSettingsSaveRejectsStaleRevision(t *testing.T) {
	e, _ := setup(t)
	s, _ := e.Collection.Settings()
	s.AutomaticReplies = false
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- e.Collection.SetSettings(context.Background(), s) }()
	}
	wg.Wait()
	close(errs)
	success := 0
	for err := range errs {
		if err == nil {
			success++
		}
	}
	if success != 1 {
		t.Fatal("stale concurrent settings overwrite")
	}
}
func TestOutgoingObservationDeduplicatesEchoAndHistory(t *testing.T) {
	e, _ := setup(t)
	enableInbox(t, e)
	m := incoming()
	m.Outgoing = true
	m.Text = "Synthetic phone reply"
	m.TransactionID = "synthetic-phone-transaction"
	m.Status = "delivered"
	if err := e.Collection.Observe(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	e.Collection.Observe(context.Background(), m)
	if err := e.Store.SaveObserved(m.ConversationID, m.ID, m.TransactionID, "outgoing", m.Text, "history", "read", m.At); err != nil {
		t.Fatal(err)
	}
	p, _ := e.Store.OwnerMessagePage(m.ConversationID, "", "", 50)
	if len(p.Messages) != 1 || p.Messages[0].Status != "read" {
		t.Fatal("duplicate outgoing echo or status lost")
	}
	// A recorded server transaction has a single canonical row as well.
	id := store.Hash("synthetic-job")
	e.Store.Enqueue(id, m.ConversationID, "manual", store.Plan{})
	e.Store.Submitting(id, "synthetic-server-transaction")
	e.Store.SaveMessage("synthetic-server-transaction", m.ConversationID, "outgoing", "Synthetic server reply")
	e.Store.SaveObserved(m.ConversationID, "synthetic-upstream-id", "synthetic-server-transaction", "outgoing", "Synthetic server reply", "phone", "delivered", m.At)
	p, _ = e.Store.OwnerMessagePage(m.ConversationID, "", "", 50)
	if len(p.Messages) != 2 {
		t.Fatal("server echo duplicated")
	}
}
