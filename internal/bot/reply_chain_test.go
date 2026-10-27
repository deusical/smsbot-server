package bot

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/deusical/smsbot-server/internal/transport"
)

// saveChained saves a message that natively replies to another (empty for none).
func saveChained(t *testing.T, e *Engine, wire, replyTo, direction, origin, text string) {
	t.Helper()
	var v []*transport.MessageInteraction
	if replyTo != "" {
		v = append(v, &transport.MessageInteraction{UpstreamID: wire, Protocol: "RCS", ReplyTo: replyTo, ObservedAt: time.Now()})
	}
	if err := e.Store.SaveObservedFiles("demo-chat", wire, "", direction, text, origin, "", time.Now(), nil, v...); err != nil {
		t.Fatal(err)
	}
}

func chainAsk(t *testing.T, e *Engine, text, replyTo string) string {
	t.Helper()
	enableChainAsk(t, e)
	return threadAccept(t, e, threadPrompt("ask-"+replyTo+text, text, replyTo)).Input
}

func enableChainAsk(t *testing.T, e *Engine) {
	t.Helper()
	arm(t, e)
	configureAI(t, e, true, false)
}

func TestAskFollowsTheWholeReplyChain(t *testing.T) {
	e, _ := setup(t)
	saveChained(t, e, "m1", "", "outgoing", "phone", "Want to get dinner Friday?")
	saveChained(t, e, "m2", "m1", "incoming", "phone", "Sure, where?")
	saveChained(t, e, "m3", "m2", "outgoing", "bot", "How about the new ramen place?")
	saveChained(t, e, "m4", "m3", "incoming", "phone", "Is that the one on Pine?")
	got := chainAsk(t, e, "!ask is this a good plan", "m4")
	want := "This question replies to a chain of messages in the chat, oldest first. It replies to the last one.\n\n" +
		"1. Sent by the other person in this chat:\n\"\"\"\nWant to get dinner Friday?\n\"\"\"\n\n" +
		"2. Sent by the person asking:\n\"\"\"\nSure, where?\n\"\"\"\n\n" +
		"3. Sent by the AI assistant in this chat:\n\"\"\"\nHow about the new ramen place?\n\"\"\"\n\n" +
		"4. Sent by the person asking:\n\"\"\"\nIs that the one on Pine?\n\"\"\"\n\n" +
		"Question: is this a good plan"
	if got != want {
		t.Fatalf("got:\n%s", got)
	}
	// Replying to the middle of the chain sees only what came before it.
	if mid := chainAsk(t, e, "!ask summarize", "m2"); !strings.Contains(mid, "1. Sent by the other person in this chat:\n\"\"\"\nWant to get dinner") || !strings.Contains(mid, "2. Sent by the person asking:\n\"\"\"\nSure, where?") || strings.Contains(mid, "ramen") {
		t.Fatalf("middle of the chain:\n%s", mid)
	}
}

func TestReplyChainStopsAtLoopsGapsAndTheDepthLimit(t *testing.T) {
	e, _ := setup(t)
	// A message and its reply that point at each other.
	saveChained(t, e, "loop-a", "loop-b", "incoming", "phone", "Synthetic A")
	saveChained(t, e, "loop-b", "loop-a", "incoming", "phone", "Synthetic B")
	if got := chainAsk(t, e, "!ask what", "loop-b"); strings.Count(got, "Synthetic A") != 1 || strings.Count(got, "Synthetic B") != 1 || !strings.HasPrefix(got, "This question replies to a chain") {
		t.Fatalf("loop:\n%s", got)
	}
	// The chain ends at a message that was never saved.
	saveChained(t, e, "gap-b", "gap-missing", "incoming", "phone", "Synthetic after the gap")
	if got := chainAsk(t, e, "!ask what", "gap-b"); !strings.Contains(got, "an earlier message in the chat") || strings.Contains(got, "(earlier ones") {
		t.Fatalf("gap:\n%s", got)
	}
	// A long chain keeps the newest messages.
	prev := ""
	for i := 1; i <= chainDepth+3; i++ {
		saveChained(t, e, fmt.Sprintf("long-%d", i), prev, "incoming", "phone", fmt.Sprintf("Synthetic message %d", i))
		prev = fmt.Sprintf("long-%d", i)
	}
	got := chainAsk(t, e, "!ask what", prev)
	if !strings.Contains(got, "(earlier ones are left out)") || !strings.Contains(got, fmt.Sprintf("Synthetic message %d", chainDepth+3)) || !strings.Contains(got, "Synthetic message 4\"") && !strings.Contains(got, "Synthetic message 4\n") || strings.Contains(got, "Synthetic message 3\n") {
		t.Fatalf("depth limit:\n%s", got)
	}
}

func TestReplyChainTextIsBounded(t *testing.T) {
	e, _ := setup(t)
	saveChained(t, e, "big-1", "", "incoming", "phone", "Synthetic oldest "+strings.Repeat("a", 1900))
	prev := "big-1"
	for i := 2; i <= 6; i++ {
		wire := fmt.Sprintf("big-%d", i)
		saveChained(t, e, wire, prev, "incoming", "phone", fmt.Sprintf("Synthetic %d ", i)+strings.Repeat("b", 1900))
		prev = wire
	}
	got := chainAsk(t, e, "!ask what", prev)
	if strings.Contains(got, "Synthetic oldest") || !strings.Contains(got, "(earlier ones are left out)") || !strings.Contains(got, "Synthetic 6 ") {
		t.Fatalf("budget not applied (%d chars)", len(got))
	}
	if len(got) > chainChars+3000 {
		t.Fatalf("prompt is %d characters", len(got))
	}
}

func TestReplyChainPhotoIsOnlyTheOneRepliedToDirectly(t *testing.T) {
	e, _ := setup(t)
	enableInbox(t, e)
	savePhoto(t, e, "chain-photo", "image/png", "Synthetic caption", syntheticPNG)
	saveChained(t, e, "chain-text", "chain-photo", "incoming", "phone", "Synthetic comment on the photo")
	enableChainAsk(t, e)
	p := threadAccept(t, e, threadPrompt("ask-direct", "!ask what", "chain-photo"))
	if p.File == nil {
		t.Fatal("a directly quoted photo was not attached")
	}
	p = threadAccept(t, e, threadPrompt("ask-indirect", "!ask what", "chain-text"))
	if p.File != nil || !strings.Contains(p.Input, "1. Sent by the person asking, a photo (not shown):\n\"\"\"\nSynthetic caption") {
		t.Fatalf("image=%v input:\n%s", p.File, p.Input)
	}
}
