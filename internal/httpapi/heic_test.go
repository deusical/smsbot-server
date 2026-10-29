package httpapi

import (
	"bytes"
	"context"
	"image"
	_ "image/jpeg"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/deusical/smsbot-server/internal/media"
	"github.com/deusical/smsbot-server/internal/transport"
)

func TestAnInlineHEICPhotoIsShownAsAJPEGAndTheOriginalStaysADownload(t *testing.T) {
	s := apiSetup(t)
	settings, _ := s.Collection.Settings()
	settings.Enabled = true
	if err := s.Collection.SetSettings(context.Background(), settings); err != nil {
		t.Fatal(err)
	}
	heic, err := os.ReadFile("../media/testdata/sample.heic")
	if err != nil {
		t.Fatal(err)
	}
	// A real photo, a damaged one, and a thumbnail that is already a JPEG.
	cases := map[string][]byte{"IMG_1.heic": heic, "IMG_2.heic": heic[:len(heic)/3], "IMG_3.heic": []byte("not a photo at all")}
	var files []media.File
	for name, data := range cases {
		files = append(files, media.File{Info: media.Info{ID: "part-" + name, Name: name, MIME: "image/heic", Size: int64(len(data))}, Data: data, SizeUnverified: true})
	}
	if err := s.Collection.Observe(context.Background(), transport.Incoming{ID: "synthetic-heic-message", ConversationID: "demo-chat", ParticipantID: "demo-peer", At: time.Now(), Attachments: files}); err != nil {
		t.Fatal(err)
	}
	p, _ := s.Collection.InboxPage(context.Background(), "demo-chat", "", "", 50)
	url := func(name, query string) string {
		return "/api/v1/inbox/conversations/demo-chat/messages/" + p.Messages[0].ID + "/attachments/part-" + name + query
	}

	w := ownerRequest(s, "GET", url("IMG_1.heic", "?inline=1"), "")
	if cfg, format, err := image.DecodeConfig(bytes.NewReader(w.Body.Bytes())); w.Code != 200 || err != nil || format != "jpeg" || cfg.Width < 1 ||
		w.Header().Get("Content-Type") != "image/jpeg" || !strings.HasPrefix(w.Header().Get("Content-Disposition"), "inline;") || !strings.Contains(w.Header().Get("Content-Disposition"), "IMG_1.jpg") {
		t.Fatalf("inline: %d %q %q %v", w.Code, w.Header().Get("Content-Type"), w.Header().Get("Content-Disposition"), err)
	}
	// The download is the original, untouched.
	if w = ownerRequest(s, "GET", url("IMG_1.heic", ""), ""); w.Code != 200 || !bytes.Equal(w.Body.Bytes(), heic) || w.Header().Get("Content-Type") != "image/heic" || !strings.HasPrefix(w.Header().Get("Content-Disposition"), "attachment;") {
		t.Fatalf("download: %d %q", w.Code, w.Header().Get("Content-Type"))
	}
	for _, name := range []string{"IMG_2.heic", "IMG_3.heic"} {
		if w = ownerRequest(s, "GET", url(name, "?inline=1"), ""); w.Code != 415 {
			t.Errorf("%s: %d", name, w.Code)
		}
	}
}
