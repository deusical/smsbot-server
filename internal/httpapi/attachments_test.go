package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/deusical/smsbot-server/internal/media"
	"github.com/deusical/smsbot-server/internal/store"
	"github.com/deusical/smsbot-server/internal/transport"
)

func TestUnlimitedAttachmentsRemoveOldBodyAndDownloadCaps(t *testing.T) {
	s := apiSetup(t)
	settings, _ := s.Collection.Settings()
	settings.Enabled = true
	settings.AttachmentLimits = &media.Limits{}
	if err := s.Collection.SetSettings(context.Background(), settings); err != nil {
		t.Fatal(err)
	}
	if err := s.Engine.Pause(false); err != nil {
		t.Fatal(err)
	}
	data := make([]byte, 11<<20)
	uploads := []media.Upload{{Name: "synthetic-large-a.bin", Data: data}, {Name: "synthetic-large-b.bin", Data: data}, {Data: []byte{1}}, {Data: []byte{2}}, {Data: []byte{3}}}
	body, _ := json.Marshal(map[string]any{"text": "", "request_id": "synthetic-unlimited-request", "attachments": uploads})
	if len(body) <= 28<<20 {
		t.Fatal("fixture must exceed old HTTP ceiling")
	}
	w := ownerRequest(s, "POST", "/api/v1/inbox/conversations/demo-chat/compose", string(body))
	if w.Code != 202 {
		t.Fatal("unlimited upload rejected", w.Code)
	}
	if err := s.Engine.Dispatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	page, err := s.Store.OwnerMessagePage("demo-chat", "", "", 50)
	if err != nil || len(page.Messages) != 1 || len(page.Messages[0].Attachments) != 5 {
		t.Fatal("unlimited files not retained", err)
	}
	message := page.Messages[0]
	url := "/api/v1/inbox/conversations/demo-chat/messages/" + message.ID + "/attachments/" + message.Attachments[0].ID
	w = ownerRequest(s, "GET", url, "")
	if w.Code != 200 || !bytes.Equal(w.Body.Bytes(), data) {
		t.Fatal("unlimited download retained old 10 MiB cap", w.Code)
	}
}

func TestAttachmentOwnerAccessDownloadHeadersAndRevocation(t *testing.T) {
	s := apiSetup(t)
	settings, _ := s.Collection.Settings()
	settings.Enabled = true
	if err := s.Collection.SetSettings(context.Background(), settings); err != nil {
		t.Fatal(err)
	}
	files, _ := media.Prepare([]media.Upload{{Name: "../synthetic.html", MIME: "text/html", Data: []byte("<script>synthetic</script>")}})
	if err := s.Collection.Observe(context.Background(), transport.Incoming{ID: "synthetic-file-message", ConversationID: "demo-chat", ParticipantID: "demo-peer", At: time.Now(), Attachments: files}); err != nil {
		t.Fatal(err)
	}
	p, _ := s.Collection.InboxPage(context.Background(), "demo-chat", "", "", 50)
	url := "/api/v1/inbox/conversations/demo-chat/messages/" + p.Messages[0].ID + "/attachments/" + files[0].ID + "?inline=1"
	if w := req(s, "GET", url, "", "", ""); w.Code != 401 {
		t.Fatal("anonymous file access permitted", w.Code)
	}
	w := ownerRequest(s, "GET", url, "")
	if w.Code != 200 || w.Body.String() != "<script>synthetic</script>" || !strings.HasPrefix(w.Header().Get("Content-Disposition"), "attachment;") || w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("X-Content-Type-Options") != "nosniff" || !strings.Contains(w.Header().Get("Content-Security-Policy"), "sandbox") {
		t.Fatal("unsafe file download headers", w.Code)
	}
	r := httptest.NewRequest("GET", "http://localhost:8787"+url, nil)
	r.AddCookie(&http.Cookie{Name: "smsbot_session", Value: "synthetic-session"})
	r.Header.Set("Range", "bytes=0-6")
	w = httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 206 || w.Body.String() != "<script" {
		t.Fatal("media range request failed", w.Code)
	}
	settings, _ = s.Collection.Settings()
	settings.Enabled = false
	s.Collection.SetSettings(context.Background(), settings)
	if w = ownerRequest(s, "GET", url, ""); w.Code != 200 || w.Body.String() != "<script>synthetic</script>" {
		t.Fatal("local original unavailable after collection stopped")
	}
}

func TestFileOnlyComposeAPIAndPayloadValidation(t *testing.T) {
	s := apiSetup(t)
	settings, _ := s.Collection.Settings()
	settings.Enabled = true
	s.Collection.SetSettings(context.Background(), settings)
	s.Engine.Pause(false)
	body, _ := json.Marshal(map[string]any{"text": "", "request_id": "synthetic-file-compose", "attachments": []media.Upload{{Name: "synthetic.txt", MIME: "text/plain", Data: []byte("Synthetic file")}}})
	if w := ownerRequest(s, "POST", "/api/v1/inbox/conversations/demo-chat/compose", string(body)); w.Code != 202 {
		t.Fatal("file-only compose rejected", w.Code)
	}
	if w := ownerRequest(s, "POST", "/api/v1/inbox/conversations/demo-chat/compose", `{"text":"","request_id":"synthetic-bad-upload","attachments":[{"name":"synthetic","data":"eA==","remote_id":"forged"}]}`); w.Code != 400 {
		t.Fatal("client supplied remote descriptor accepted", w.Code)
	}
	if err := s.Engine.Dispatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	w := ownerRequest(s, "GET", "/api/v1/inbox/conversations/demo-chat/history", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "synthetic.txt") || strings.Contains(w.Body.String(), `"data"`) {
		t.Fatal("sent file metadata missing or data leaked")
	}
}

func TestLegacyHistoryNeverContainsAttachmentMetadata(t *testing.T) {
	s := apiSetup(t)
	s.Store.Enroll("demo-chat", "demo-peer")
	files, _ := media.Prepare([]media.Upload{{Name: "synthetic-private-file.txt", Data: []byte("Synthetic file")}})
	// Even a public legacy message row cannot expose file metadata on this API.
	s.Store.SaveMessage(store.Hash("demo-chat\x00synthetic-public-upstream"), "demo-chat", "incoming", "Synthetic public text")
	s.Store.SaveObservedFiles("demo-chat", "synthetic-public-upstream", "", "incoming", "", "phone", "", time.Now(), files)
	settings, _ := s.Collection.Settings()
	if settings.Enabled {
		t.Fatal("fixture inbox should be disabled")
	}
	for _, owner := range []bool{true, false} {
		var w *httptest.ResponseRecorder
		if owner {
			w = ownerRequest(s, "GET", "/api/v1/conversations/demo-chat/history", "")
		} else {
			w = req(s, "GET", "/api/v1/conversations/demo-chat/history", "", "fixture-client", "")
		}
		if w.Code != 200 || strings.Contains(w.Body.String(), "attachments") || strings.Contains(w.Body.String(), "synthetic-private-file") {
			t.Fatal("legacy API exposed attachment metadata")
		}
	}
}
