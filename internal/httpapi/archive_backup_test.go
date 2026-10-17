package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"smsbot-server/internal/media"
	"smsbot-server/internal/store"
	"smsbot-server/internal/transport"
	"testing"
	"time"
)

func TestSavedOriginalReadOfflineRangeAndCorruption(t *testing.T) {
	s := apiSetup(t)
	settings, _ := s.Engine.InboxSettings()
	settings.Enabled = true
	if err := s.Engine.SetInboxSettings(context.Background(), settings); err != nil {
		t.Fatal(err)
	}
	files, _ := media.Prepare([]media.Upload{{Name: "synthetic.txt", MIME: "text/plain", Data: []byte("Synthetic saved original")}})
	if err := s.Engine.Observe(context.Background(), transport.Incoming{ID: "original", ConversationID: "demo-chat", ParticipantID: "demo-peer", At: time.Now(), Attachments: files}); err != nil {
		t.Fatal(err)
	}
	s.Engine.Archive.Tick(context.Background())
	message := store.Hash("demo-chat\x00original")
	file, err := s.Engine.Store.Attachment("demo-chat", message, files[0].ID)
	if err != nil || file.BlobID == "" {
		t.Fatal("file not archived", err)
	}
	settings, _ = s.Engine.InboxSettings()
	settings.Enabled = false
	s.Engine.SetInboxSettings(context.Background(), settings)
	s.Engine.Transport = nil // any phone lookup would panic; local reads must not use it
	url := "/api/v1/inbox/conversations/demo-chat/messages/" + message + "/attachments/" + file.ID
	if w := ownerRequest(s, "GET", url, ""); w.Code != 200 || !bytes.Equal(w.Body.Bytes(), files[0].Data) {
		t.Fatal("offline archive file unavailable", w.Code)
	}
	r := httptest.NewRequest("GET", "http://localhost:8787"+url, nil)
	r.AddCookie(&http.Cookie{Name: "smsbot_session", Value: "synthetic-session"})
	r.Header.Set("Range", "bytes=10-14")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 206 || w.Body.String() != string(files[0].Data[10:15]) {
		t.Fatal("encrypted local seek range failed", w.Code)
	}
	path := filepath.Join(s.Engine.Archive.Root, "blobs", file.BlobID+".enc")
	f, err := os.OpenFile(path, os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.WriteAt([]byte{0}, 0)
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	if w := ownerRequest(s, "GET", url, ""); w.Code != 409 {
		t.Fatal("corrupt original silently served", w.Code)
	}
}
func TestArchiveExportOwnerFormatAndDeletion(t *testing.T) {
	s := apiSetup(t)
	defer s.Engine.Archive.Close()
	if w := ownerRequest(s, "PUT", "/api/v1/inbox/archive/files/policy", `{"enabled":true,"max_file_mb":-1,"quota_mb":0}`); w.Code != 400 {
		t.Fatal("invalid policy accepted")
	}
	for _, plaintext := range []bool{false, true} {
		body := "{}"
		if plaintext {
			body = `{"plaintext":true}`
		}
		w := ownerRequest(s, "POST", "/api/v1/inbox/archive/backup", body)
		if w.Code != 202 {
			t.Fatal("job creation failed", w.Code)
		}
		deadline := time.Now().Add(5 * time.Second)
		for {
			job, err := s.Engine.Archive.Job()
			if err != nil {
				t.Fatal(err)
			}
			if job.State == "complete" {
				w = ownerRequest(s, "GET", "/api/v1/inbox/archive/backup/"+job.ID+"/file", "")
				if w.Code != 200 {
					t.Fatal("complete package unavailable", w.Code)
				}
				if plaintext && !bytes.Contains(w.Body.Bytes(), []byte("manifest.json")) {
					t.Fatal("plaintext selection encrypted")
				}
				if !plaintext && !bytes.HasPrefix(w.Body.Bytes(), []byte("SMA1")) {
					t.Fatal("encrypted selection plaintext")
				}
				break
			}
			if job.State != "running" || time.Now().After(deadline) {
				b, _ := json.Marshal(job)
				t.Fatalf("export failed: %s", b)
			}
			time.Sleep(5 * time.Millisecond)
		}
		if w = ownerRequest(s, "DELETE", "/api/v1/inbox/archive/backup", ""); w.Code != 200 {
			t.Fatal("cannot delete export")
		}
	}
}

func TestArchiveStatsOwnerOnly(t *testing.T) {
	s := apiSetup(t)
	w := ownerRequest(s, "GET", "/api/v1/inbox/archive/stats", "")
	var value struct {
		DatabaseBytes int64  `json:"database_bytes"`
		MeasuredAt    string `json:"measured_at"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &value) != nil || value.DatabaseBytes <= 0 || value.MeasuredAt == "" {
		t.Fatal("owner metrics unavailable", w.Code)
	}
}
