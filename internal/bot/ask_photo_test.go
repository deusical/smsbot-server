package bot

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/deusical/smsbot-server/internal/media"
	"github.com/deusical/smsbot-server/internal/store"
	"github.com/deusical/smsbot-server/internal/transport"
)

// syntheticPNG starts with the PNG signature, so content sniffing sees a PNG.
var syntheticPNG = append([]byte("\x89PNG\r\n\x1a\n"), []byte("synthetic photo bytes")...)

func savePhoto(t *testing.T, e *Engine, wire, mime, text string, data []byte) {
	t.Helper()
	files := []media.File{{Info: media.Info{ID: "part-0", Name: "synthetic-photo", MIME: mime, Size: int64(len(data))}, Data: data, SizeUnverified: true}}
	if err := e.Store.SaveObservedFiles("demo-chat", wire, "", "incoming", text, "phone", "", time.Now(), files); err != nil {
		t.Fatal(err)
	}
}

func setModalities(t *testing.T, e *Engine, modalities ...string) {
	t.Helper()
	models, err := e.Models()
	if err != nil {
		t.Fatal(err)
	}
	for i := range models {
		models[i].InputModalities = modalities
	}
	if err = e.Store.SetPublicSetting("model_catalog", models); err != nil {
		t.Fatal(err)
	}
}

func TestAskAboutAQuotedPhotoSendsItOnce(t *testing.T) {
	e, _ := setup(t)
	enableInbox(t, e)
	arm(t, e)
	configureAI(t, e, true, false)
	setModalities(t, e, "text", "image")
	provider := &transcriptProvider{}
	e.Provider = provider
	savePhoto(t, e, "photo-wire", "image/png", "", syntheticPNG)

	ask := threadPrompt("ask-photo", "!ask what's in this?", "photo-wire")
	p := threadAccept(t, e, ask)
	want := "This question replies to a photo in the chat, sent by the person asking. The photo is attached.\n\nQuestion: what's in this?"
	if p.Image == nil || p.Input != want || p.Thread == nil {
		t.Fatalf("photo not planned: image=%v input=%q", p.Image, p.Input)
	}
	if err := e.Dispatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if stateOf(t, e, store.Hash("demo-chat\x00ask-photo")) != "submitted" {
		t.Fatal("photo question was not answered")
	}
	if len(provider.images) != 1 || provider.images[0] == nil || provider.images[0].MIME != "image/png" || !bytes.Equal(provider.images[0].Data, syntheticPNG) {
		t.Fatal("provider did not receive the photo")
	}
	// A follow-up keeps the conversation's text but not the photo.
	threadEcho(t, e, ask, "answer-photo")
	follow := threadAccept(t, e, threadPrompt("follow-photo", "What colour is it?", "answer-photo"))
	if follow.Image != nil || len(follow.Context) != 2 || follow.Context[0].Content != want {
		t.Fatalf("follow-up context wrong: image=%v context=%v", follow.Image, follow.Context)
	}
	if err := e.Dispatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(provider.images) != 2 || provider.images[1] != nil {
		t.Fatal("photo was sent again with the follow-up")
	}
}

func TestBareAskOnAQuotedPhotoWithText(t *testing.T) {
	e, _ := setup(t)
	enableInbox(t, e)
	arm(t, e)
	configureAI(t, e, false, false)
	savePhoto(t, e, "photo-wire", "image/png", "Synthetic caption", syntheticPNG)
	p := threadAccept(t, e, threadPrompt("ask-bare-photo", "!ask", "photo-wire"))
	want := "This question replies to a photo in the chat, sent by the person asking. The photo is attached, with this text:\n\"\"\"\nSynthetic caption\n\"\"\"\n\nNo question was given. Describe or respond to the photo."
	if p.Image == nil || p.Input != want || p.Model == "" || p.Reply != "" {
		t.Fatalf("input=%q reply=%q", p.Input, p.Reply)
	}
}

func TestAskOnAPhotoTheModelCannotRead(t *testing.T) {
	for _, tc := range []struct {
		name, mime string
		size       int
		modalities []string
		reply      string
	}{
		{"text_only_model", "image/png", len(syntheticPNG), []string{"text"}, "This model can't read images; pick one that can for this command."},
		{"unsupported_format", "image/heic", len(syntheticPNG), nil, "I can only read JPEG, PNG, GIF and WebP photos."},
		{"too_large", "image/png", maxPhotoMB<<20 + 1, nil, "That photo is too large for the AI; the limit is 25 MB."},
		{"webp_over_setting", "image/webp", defaultPhotoMB<<20 + 1, nil, "That WebP photo is over 5 MB and can't be shrunk; the limit is set in Settings."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, _ := setup(t)
			enableInbox(t, e)
			arm(t, e)
			configureAI(t, e, true, false)
			setModalities(t, e, tc.modalities...)
			provider := &transcriptProvider{}
			e.Provider = provider
			data := syntheticPNG
			if tc.size > len(data) {
				data = append(append([]byte{}, data...), make([]byte, tc.size-len(data))...)
			}
			savePhoto(t, e, "photo-wire", tc.mime, "", data)
			p := threadAccept(t, e, threadPrompt("ask-photo", "!ask what's this?", "photo-wire"))
			if p.Reply != tc.reply || p.Model != "" || p.Image != nil || p.Thread != nil {
				t.Fatalf("reply=%q model=%q", p.Reply, p.Model)
			}
			if err := e.Dispatch(context.Background()); err != nil {
				t.Fatal(err)
			}
			if stateOf(t, e, store.Hash("demo-chat\x00ask-photo")) != "submitted" || len(provider.images) != 0 {
				t.Fatal("explanation was not sent without the provider")
			}
		})
	}
}

// sentContains reports whether any message the fixture sent contains text.
func sentContains(f *transport.Fixture, text string) bool {
	for _, sent := range f.Sent {
		if strings.Contains(sent, text) {
			return true
		}
	}
	return false
}

func TestAskOnAPhotoThatIsNotAnImageIsExplained(t *testing.T) {
	e, fixture := setup(t)
	enableInbox(t, e)
	arm(t, e)
	configureAI(t, e, false, false)
	provider := &transcriptProvider{}
	e.Provider = provider
	// The saved type says PNG, but the bytes aren't an image.
	savePhoto(t, e, "photo-wire", "image/png", "", []byte("Synthetic text, not a photo"))
	threadAccept(t, e, threadPrompt("ask-photo", "!ask what's this?", "photo-wire"))
	if err := e.Dispatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	// The chat is told, instead of the request failing quietly.
	if state := stateOf(t, e, store.Hash("demo-chat\x00ask-photo")); state != "submitted" || len(provider.history) != 0 || !sentContains(fixture, "(image_unsupported)") {
		t.Fatalf("state=%s sent=%v", state, fixture.Sent)
	}
	_, unknown, err := e.Store.SpendSince(time.Now().UTC().Format("2006-01-02") + "T00:00:00Z")
	if err != nil || unknown != 0 {
		t.Fatalf("unknown=%d err=%v", unknown, err)
	}
}

func TestProvidersSendThePhotoWithThePrompt(t *testing.T) {
	var request struct {
		Messages []json.RawMessage `json:"messages"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		w.Write([]byte(`{"choices":[{"message":{"content":"Synthetic answer"},"finish_reason":"stop"}],"usage":{}}`))
	}))
	defer server.Close()
	generation, _ := json.Marshal(DefaultGeneration())
	plan := store.Plan{Model: "vendor/model", Generation: generation, Input: "Synthetic question"}
	history := []store.Turn{{Role: "user", Content: "Synthetic earlier"}, {Role: "assistant", Content: "Synthetic reply"}}
	image := &Image{MIME: "image/png", Data: syntheticPNG}
	openrouter := NewOpenRouter()
	openrouter.base = server.URL
	for name, provider := range map[string]Provider{"openrouter": openrouter, "compatible": NewOpenAICompatible(server.URL, "")} {
		if _, err := provider.Generate(context.Background(), "SYNTHETIC_KEY", plan, history, Model{}, image); err != nil {
			t.Fatal(name, err)
		}
		if len(request.Messages) != 4 || !strings.Contains(string(request.Messages[1]), `"content":"Synthetic earlier"`) {
			t.Fatalf("%s: history changed: %s", name, request.Messages)
		}
		var last struct {
			Role    string `json:"role"`
			Content []struct {
				Type     string `json:"type"`
				Text     string `json:"text"`
				ImageURL struct {
					URL string `json:"url"`
				} `json:"image_url"`
			} `json:"content"`
		}
		if err := json.Unmarshal(request.Messages[3], &last); err != nil {
			t.Fatal(name, err)
		}
		url := "data:image/png;base64," + base64.StdEncoding.EncodeToString(syntheticPNG)
		if last.Role != "user" || len(last.Content) != 2 || last.Content[0].Text != "Synthetic question" || last.Content[1].Type != "image_url" || last.Content[1].ImageURL.URL != url {
			t.Fatalf("%s: prompt without photo: %s", name, request.Messages[3])
		}
	}
}

func TestCatalogsRecordImageInput(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":[{"id":"vendor/vision","architecture":{"input_modalities":["text","image"],"output_modalities":["text"]},"pricing":{"prompt":"0","completion":"0"}},{"id":"vendor/text","architecture":{"input_modalities":["text"],"output_modalities":["text"]},"pricing":{"prompt":"0","completion":"0"}},{"id":"vendor/unknown","pricing":{"prompt":"0","completion":"0"}}]}`))
	}))
	defer server.Close()
	openrouter := NewOpenRouter()
	openrouter.base = server.URL
	for name, provider := range map[string]Provider{"openrouter": openrouter, "compatible": NewOpenAICompatible(server.URL, "")} {
		models, err := provider.Models(context.Background())
		if err != nil {
			t.Fatal(name, err)
		}
		reads := map[string]bool{}
		for _, m := range models {
			reads[m.RemoteID] = readsImages(m)
		}
		if !reads["vendor/vision"] || reads["vendor/text"] {
			t.Fatalf("%s: image input not recorded: %v", name, reads)
		}
		if _, listed := reads["vendor/unknown"]; listed && !reads["vendor/unknown"] {
			t.Fatalf("%s: a model without modalities was treated as text-only", name)
		}
	}
}

type photoTransport struct {
	*transport.Fixture
	downloads int
}

func (f *photoTransport) DownloadFile(ctx context.Context, file media.File) ([]byte, error) {
	f.downloads++
	if file.RemoteID != "synthetic-photo-original" {
		return nil, errors.New("attachment_unavailable")
	}
	return syntheticPNG, nil
}

func TestAskDownloadsAPhotoOnlyFromACollectedConversation(t *testing.T) {
	for _, collected := range []bool{true, false} {
		e, fixture := setup(t)
		s := enableInbox(t, e)
		phone := &photoTransport{Fixture: fixture}
		useTransport(e, phone)
		arm(t, e)
		configureAI(t, e, false, false)
		provider := &transcriptProvider{}
		e.Provider = provider
		files := []media.File{{Info: media.Info{ID: "part-0", Name: "synthetic-photo", MIME: "image/png", Size: int64(len(syntheticPNG))}, RemoteID: "synthetic-photo-original", MessageID: "photo-wire"}}
		if err := e.Store.SaveObservedFiles("demo-chat", "photo-wire", "", "incoming", "", "phone", "", time.Now(), files); err != nil {
			t.Fatal(err)
		}
		threadAccept(t, e, threadPrompt("ask-photo", "!ask what's this?", "photo-wire"))
		if !collected {
			// Turning off access for the conversation stops the download.
			s.Enabled = false
			if err := e.Collection.SetSettings(context.Background(), s); err != nil {
				t.Fatal(err)
			}
		}
		if err := e.Dispatch(context.Background()); err != nil {
			t.Fatal(err)
		}
		state := stateOf(t, e, store.Hash("demo-chat\x00ask-photo"))
		if collected && (state != "submitted" || len(provider.images) != 1 || provider.images[0] == nil || phone.downloads != 1) {
			t.Fatalf("collected: state=%s downloads=%d", state, phone.downloads)
		}
		if !collected && (state != "submitted" || len(provider.history) != 0 || phone.downloads != 0 || !sentContains(fixture, "(inbox_access_disabled)")) {
			t.Fatalf("not collected: state=%s downloads=%d", state, phone.downloads)
		}
	}
}

// noisePNG is incompressible, so it is about 5.7 MB: over the default limit
// and well under the 25 MB maximum.
func noisePNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 1600, 1200))
	rand.New(rand.NewSource(1)).Read(img.Pix)
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestPhotoSizeSettingDecidesWhatIsShrunk(t *testing.T) {
	big := noisePNG(t)
	if len(big) <= defaultPhotoMB<<20 || len(big) >= maxPhotoMB<<20 {
		t.Fatalf("test photo is %d bytes", len(big))
	}
	for _, tc := range []struct {
		name   string
		limit  int // 0 keeps what older saved settings had
		mime   string
		failed bool
	}{
		{"default_shrinks", 0, "image/jpeg", false},
		{"raised_sends_original", 25, "image/png", false},
		{"unshrinkable_is_explained", 1, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, fixture := setup(t)
			enableInbox(t, e)
			arm(t, e)
			cfg := configureAI(t, e, false, false)
			cfg.Settings.AIPhotoMaxMB = tc.limit
			if err := e.SetConfiguration(cfg); err != nil {
				t.Fatal(err)
			}
			if saved, _ := e.Configuration(); tc.limit == 0 && saved.Settings.AIPhotoMaxMB != defaultPhotoMB {
				t.Fatalf("saved limit = %d", saved.Settings.AIPhotoMaxMB)
			}
			provider := &transcriptProvider{}
			e.Provider = provider
			savePhoto(t, e, "photo-wire", "image/png", "", big)
			threadAccept(t, e, threadPrompt("ask-photo", "!ask what's this?", "photo-wire"))
			if err := e.Dispatch(context.Background()); err != nil {
				t.Fatal(err)
			}
			state := stateOf(t, e, store.Hash("demo-chat\x00ask-photo"))
			if tc.failed {
				if state != "submitted" || len(provider.history) != 0 || !sentContains(fixture, "(image_too_large)") {
					t.Fatalf("state=%s sent=%v", state, fixture.Sent)
				}
				return
			}
			if state != "submitted" || len(provider.images) != 1 || provider.images[0].MIME != tc.mime {
				t.Fatalf("state=%s images=%v", state, provider.images)
			}
			if tc.mime == "image/jpeg" && len(provider.images[0].Data) >= defaultPhotoMB<<20 {
				t.Fatalf("shrunk photo is still %d bytes", len(provider.images[0].Data))
			}
		})
	}
}

func TestPhotoSizeSettingIsValidated(t *testing.T) {
	for limit, ok := range map[int]bool{-1: false, 0: true, 1: true, maxPhotoMB: true, maxPhotoMB + 1: false} {
		c := DefaultConfiguration()
		c.Settings.AIPhotoMaxMB = limit
		if (c.Validate() == nil) != ok {
			t.Errorf("limit %d: valid=%t want %t", limit, !ok, ok)
		}
	}
}
