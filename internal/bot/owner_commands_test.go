package bot

import (
	"context"
	"testing"
	"time"

	"github.com/deusical/smsbot-server/internal/transport"
)

func ownerCommand(text string) transport.Incoming {
	return transport.Incoming{ID: "owner-" + text, ConversationID: "demo-chat", ParticipantID: "demo-self", Text: text, At: time.Now(), Outgoing: true, TransactionID: "phone-" + text, Status: "submitted"}
}

func allowOwnerCommands(t *testing.T, e *Engine, on bool) {
	t.Helper()
	c, err := e.Configuration()
	if err != nil {
		t.Fatal(err)
	}
	c.Settings.OwnerCommands = on
	c.Commands = []Command{{Name: "echo", Type: "fixed", Instructions: "!ping", Enabled: true}}
	if err = e.SetConfiguration(c); err != nil {
		t.Fatal(err)
	}
}

func TestOwnerCommandsAreOffByDefault(t *testing.T) {
	e, f := setup(t)
	arm(t, e)
	if ok, err := e.Accept(context.Background(), ownerCommand("!ping")); ok || err != nil {
		t.Fatalf("accepted=%t error=%v", ok, err)
	}
	if len(f.Sent) != 0 {
		t.Fatal("sent without the setting")
	}
}

func TestOwnerCommandRepliesInEnrolledConversation(t *testing.T) {
	e, f := setup(t)
	arm(t, e)
	allowOwnerCommands(t, e, true)
	if ok, err := e.Accept(context.Background(), ownerCommand("!ping")); !ok || err != nil {
		t.Fatalf("accepted=%t error=%v", ok, err)
	}
	if err := e.Dispatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(f.Sent) != 1 {
		t.Fatal("owner command not answered")
	}
	for _, text := range f.Sent {
		if text != "pong" {
			t.Fatal("unexpected reply")
		}
	}
	messages, _ := e.Store.Messages("demo-chat")
	for _, m := range messages {
		if m.Direction != "outgoing" {
			t.Fatal("owner command saved as incoming")
		}
	}
}

func TestOwnerCommandsStayInsideEnrolledConversationsAndGroups(t *testing.T) {
	for name, edit := range map[string]func(*transport.Incoming){
		"unenrolled": func(m *transport.Incoming) { m.ConversationID = "demo-sms" },
		"stale":      func(m *transport.Incoming) { m.At = time.Now().Add(-3 * time.Minute) },
		"historical": func(m *transport.Incoming) { m.Historical = true },
	} {
		t.Run(name, func(t *testing.T) {
			e, _ := setup(t)
			arm(t, e)
			allowOwnerCommands(t, e, true)
			m := ownerCommand("!ping")
			edit(&m)
			if ok, err := e.Accept(context.Background(), m); ok || err != nil {
				t.Fatalf("accepted=%t error=%v", ok, err)
			}
		})
	}
}

// A reply that itself looks like a command must not start another job,
// whether the phone's echo carries our transaction or not.
func TestBotNeverAnswersItsOwnReplies(t *testing.T) {
	e, f := setup(t)
	arm(t, e)
	allowOwnerCommands(t, e, true)
	if ok, err := e.Accept(context.Background(), ownerCommand("!echo")); !ok || err != nil {
		t.Fatalf("accepted=%t error=%v", ok, err)
	}
	if err := e.Dispatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	var txn string
	for id := range f.Sent {
		txn = id
	}
	echo := ownerCommand("!ping")
	echo.ID, echo.TransactionID = "echo-with-transaction", txn
	if ok, err := e.Accept(context.Background(), echo); ok || err != nil {
		t.Fatalf("answered own reply: accepted=%t error=%v", ok, err)
	}
	// A send the phone reports with different text, such as a shortened
	// reply, is still recognized by its transaction.
	echo.ID, echo.Text = "echo-other-text", "!help"
	if ok, err := e.Accept(context.Background(), echo); ok || err != nil {
		t.Fatalf("answered own send: accepted=%t error=%v", ok, err)
	}
	echo.Text = "!ping"
	echo.ID, echo.TransactionID = "echo-without-transaction", ""
	if ok, err := e.Accept(context.Background(), echo); ok || err != nil {
		t.Fatalf("answered own reply by text: accepted=%t error=%v", ok, err)
	}
	if len(f.Sent) != 1 {
		t.Fatal("reply loop")
	}
}

// The phone reports a sent message again at each status change; a command
// ignored at its first report (here while paused) stays ignored.
func TestOwnerCommandCountsOnlyItsFirstSighting(t *testing.T) {
	e, _ := setup(t)
	arm(t, e)
	allowOwnerCommands(t, e, true)
	if err := e.Pause(true); err != nil {
		t.Fatal(err)
	}
	m := ownerCommand("!ping")
	if ok, err := e.Receive(context.Background(), m); ok || err != nil {
		t.Fatalf("accepted while paused: %t %v", ok, err)
	}
	if err := e.Pause(false); err != nil {
		t.Fatal(err)
	}
	m.Status = "delivered"
	if ok, err := e.Receive(context.Background(), m); ok || err != nil {
		t.Fatalf("status update started the command: %t %v", ok, err)
	}
}

func TestTurningOwnerCommandsOffStopsQueuedReplies(t *testing.T) {
	e, f := setup(t)
	arm(t, e)
	allowOwnerCommands(t, e, true)
	if ok, err := e.Accept(context.Background(), ownerCommand("!ping")); !ok || err != nil {
		t.Fatalf("accepted=%t error=%v", ok, err)
	}
	allowOwnerCommands(t, e, false)
	if err := e.Dispatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	jobs, _ := e.Store.Jobs()
	if len(f.Sent) != 0 || len(jobs) != 1 || jobs[0].Code != "owner_commands_disabled" {
		t.Fatalf("sent after the setting was turned off: %+v", jobs)
	}
}

// A command typed in the dashboard runs once the phone reports it sent, like
// one typed on the phone, without a second archived copy; the bot's reply to
// it does not run again.
func TestDashboardSendRunsAsOwnerCommand(t *testing.T) {
	e, f := setup(t)
	arm(t, e)
	enableInbox(t, e)
	allowOwnerCommands(t, e, true)
	if _, err := e.ComposeInbox(context.Background(), "demo-chat", "!echo", "synthetic-request"); err != nil {
		t.Fatal(err)
	}
	if err := e.Dispatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	var txn string
	for id := range f.Sent {
		txn = id
	}
	echo := ownerCommand("!echo")
	echo.ID, echo.TransactionID = "phone-copy-of-dashboard-send", txn
	if ok, err := e.Receive(context.Background(), echo); !ok || err != nil {
		t.Fatalf("dashboard command not run: accepted=%t error=%v", ok, err)
	}
	if err := e.Dispatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	var reply string
	for id, text := range f.Sent {
		if id != txn {
			reply = id
			if text != "!ping" {
				t.Fatal("unexpected reply")
			}
		}
	}
	if reply == "" {
		t.Fatal("no reply to the dashboard command")
	}
	again := ownerCommand("!ping")
	again.ID, again.TransactionID = "phone-copy-of-reply", reply
	if ok, err := e.Receive(context.Background(), again); ok || err != nil {
		t.Fatalf("answered own reply: accepted=%t error=%v", ok, err)
	}
	page, err := e.Store.OwnerMessagePage("demo-chat", "", "", 50)
	if err != nil {
		t.Fatal(err)
	}
	commands := 0
	for _, m := range page.Messages {
		if m.Text == "!echo" {
			commands++
		}
	}
	if commands != 1 {
		t.Fatalf("dashboard command archived %d times", commands)
	}
}
