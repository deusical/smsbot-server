package bot

import (
	"context"
	"testing"

	"github.com/deusical/smsbot-server/internal/media"
	"github.com/deusical/smsbot-server/internal/store"
	"github.com/deusical/smsbot-server/internal/transport"
)

// withPhoto makes a message arrive carrying a file, and saves the file as
// archiving a conversation would, under the message's own upstream ID.
func withPhoto(t *testing.T, e *Engine, m transport.Incoming, mime string, save bool) transport.Incoming {
	t.Helper()
	m.Attachments = []media.File{{Info: media.Info{ID: "part-0", Name: "synthetic", MIME: mime, Size: int64(len(syntheticPNG))}}}
	if save {
		savePhoto(t, e, m.ID, mime, "", syntheticPNG)
	}
	return m
}

func photoSetup(t *testing.T, memory bool) (*Engine, *transcriptProvider) {
	t.Helper()
	e, _ := setup(t)
	enableInbox(t, e)
	arm(t, e)
	configureAI(t, e, memory, false)
	setModalities(t, e, "text", "image")
	provider := &transcriptProvider{}
	e.Provider = provider
	return e, provider
}

func TestPhotoOnlyReplyToAnAnswerContinuesTheThread(t *testing.T) {
	e, provider := photoSetup(t, true)
	root := threadPrompt("root", "!ask What year is it?", "")
	threadAccept(t, e, root)
	if err := e.Dispatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	threadEcho(t, e, root, "answer-root")
	// A reply that is only a photo, as the dashboard sends one.
	reply := withPhoto(t, e, threadPrompt("photo-reply", "", "answer-root"), "image/png", true)
	p := threadAccept(t, e, reply)
	if p.Thread == nil || p.Thread.Parent != store.Hash("demo-chat\x00root") || len(p.Context) != 2 || p.Image == nil || p.Image.Wire != "photo-reply" {
		t.Fatalf("not a threaded follow-up with its photo: %+v", p)
	}
	if p.Input != ownPhotoPrompt("") {
		t.Fatalf("input %q", p.Input)
	}
	if err := e.Dispatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if stateOf(t, e, store.Hash("demo-chat\x00photo-reply")) != "submitted" || len(provider.images) != 2 || provider.images[1] == nil || provider.images[0] != nil {
		t.Fatal("the photo did not reach the AI with the follow-up")
	}
	// Words with the photo are kept.
	again := withPhoto(t, e, threadPrompt("photo-words", "Is this a cat?", "answer-root"), "image/png", true)
	if p = threadAccept(t, e, again); p.Input != ownPhotoPrompt("Is this a cat?") || p.Image == nil {
		t.Fatalf("input %q", p.Input)
	}
}

func TestAskWithItsOwnPhoto(t *testing.T) {
	e, provider := photoSetup(t, false)
	m := withPhoto(t, e, threadPrompt("ask-own", "!ask what is this?", ""), "image/png", true)
	p := threadAccept(t, e, m)
	if p.Image == nil || p.Input != "(A photo is attached.) what is this?" {
		t.Fatalf("image=%v input=%q", p.Image, p.Input)
	}
	if err := e.Dispatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(provider.images) != 1 || provider.images[0] == nil || provider.images[0].MIME != "image/png" {
		t.Fatal("the photo did not reach the AI")
	}
	// Its own photo wins over the one it quotes.
	savePhoto(t, e, "quoted-photo", "image/png", "", syntheticPNG)
	both := withPhoto(t, e, threadPrompt("ask-both", "!ask compare", "quoted-photo"), "image/png", true)
	if p = threadAccept(t, e, both); p.Image == nil || p.Image.Wire != "ask-both" || p.Image.Message != "" {
		t.Fatalf("image=%+v", p.Image)
	}
}

func TestFilesThatAreNotCommandsStayIgnored(t *testing.T) {
	e, _ := photoSetup(t, true)
	root := threadPrompt("root", "!ask hi", "")
	threadAccept(t, e, root)
	if err := e.Dispatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	threadEcho(t, e, root, "answer-root")
	for name, m := range map[string]transport.Incoming{
		"photo alone":                    withPhoto(t, e, threadPrompt("a", "", ""), "image/png", true),
		"photo replying to a person":     withPhoto(t, e, threadPrompt("b", "", "unknown-wire"), "image/png", true),
		"photo with chat, not a command": withPhoto(t, e, threadPrompt("c", "look at this", ""), "image/png", true),
		"document with a command":        withPhoto(t, e, threadPrompt("d", "!ask summarize", ""), "application/pdf", false),
		"document replying to an answer": withPhoto(t, e, threadPrompt("e", "", "answer-root"), "application/pdf", false),
	} {
		if ok, err := e.Accept(context.Background(), m); err != nil || ok {
			t.Errorf("%s: accepted=%t err=%v", name, ok, err)
		}
	}
}

func TestPhotoCommandsExplainWhatTheyCannotDo(t *testing.T) {
	// Photos reach the AI only from saved conversations.
	e, _ := setup(t)
	arm(t, e)
	configureAI(t, e, false, false)
	setModalities(t, e, "text", "image")
	p := threadAccept(t, e, withPhoto(t, e, threadPrompt("ask-unsaved", "!ask what is this?", ""), "image/png", false))
	if p.Model != "" || p.Image != nil || p.Reply == "" {
		t.Fatalf("unsaved conversation: model=%q reply=%q", p.Model, p.Reply)
	}
	// And only from models that read images.
	e, provider := photoSetup(t, false)
	setModalities(t, e, "text")
	p = threadAccept(t, e, withPhoto(t, e, threadPrompt("ask-text-model", "!ask what is this?", ""), "image/png", true))
	if p.Model != "" || p.Reply != "This model can't read images; pick one that can for this command." {
		t.Fatalf("text-only model: %q", p.Reply)
	}
	if err := e.Dispatch(context.Background()); err != nil || len(provider.images) != 0 {
		t.Fatal("explanation went through the provider", err)
	}
}
