package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/deusical/smsbot-server/internal/bot"
)

func TestWorkersAreConfiguredFromTheDashboard(t *testing.T) {
	t.Parallel()
	s := apiSetup(t)
	if w := ownerRequest(s, "GET", "/api/v1/workers", ""); w.Code != 503 {
		t.Fatal("answered before workers were wired", w.Code)
	}
	path := filepath.Join(t.TempDir(), "workers.json")
	s.Workers = &bot.Tasks{Path: path, Store: s.Store}
	if w := req(s, "GET", "/api/v1/workers", "", "", ""); w.Code != 401 {
		t.Fatal("unauthenticated", w.Code)
	}
	w := ownerRequest(s, "GET", "/api/v1/workers", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"workers":[]`) {
		t.Fatal(w.Code, w.Body.String())
	}
	token := strings.Repeat("t", 40)
	body := `{"workers":[{"name":"laptop","endpoint":"http://100.92.5.20:8767/download","token":"` + token + `"}],"routes":{"video.compress":["laptop"]}}`
	w = ownerRequest(s, "PUT", "/api/v1/workers", body)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var view struct {
		Config bot.WorkerConfig   `json:"config"`
		Status []bot.WorkerStatus `json:"status"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	// The token is stored, never returned and never written to the file.
	if len(view.Status) != 1 || !view.Status[0].Token || view.Status[0].Online || strings.Contains(w.Body.String(), token) {
		t.Fatal(w.Body.String())
	}
	raw, _ := os.ReadFile(path)
	if strings.Contains(string(raw), token) || !strings.Contains(string(raw), `"video.compress"`) {
		t.Fatal(string(raw))
	}
	// Saving again without a token keeps the stored one.
	if w := ownerRequest(s, "PUT", "/api/v1/workers", strings.Replace(body, token, "", 1)); w.Code != 200 || !strings.Contains(w.Body.String(), `"token":true`) {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, bad := range []string{
		`{"workers":[{"name":"laptop","endpoint":"http://192.168.1.5:8767/download"}]}`,
		`{"workers":[],"routes":{"video.compress":["nobody"]}}`,
		`{"workers":[{"name":"local","endpoint":"http://100.92.5.20:8767/download"}]}`,
	} {
		if w := ownerRequest(s, "PUT", "/api/v1/workers", bad); w.Code != 400 {
			t.Fatal("accepted", bad, w.Code)
		}
	}
}

func TestWorkersStatusCacheAndRefreshParameter(t *testing.T) {
	t.Parallel()
	s := apiSetup(t)
	token := strings.Repeat("k", 40)
	var probes atomic.Int32
	var version atomic.Int32
	version.Store(1)
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			w.WriteHeader(401)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/capabilities") {
			probes.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"protocol":  4,
				"protocols": []int{4},
				"tasks":     map[string]int{"video.download": int(version.Load())},
			})
			return
		}
		w.WriteHeader(404)
	}))
	t.Cleanup(worker.Close)

	s.Workers = &bot.Tasks{Path: filepath.Join(t.TempDir(), "workers.json"), Store: s.Store}
	body := `{"workers":[{"name":"laptop","endpoint":"` + worker.URL + `/download","token":"` + token + `"}],"routes":{}}`
	w := ownerRequest(s, "PUT", "/api/v1/workers", body)
	if w.Code != 200 || probes.Load() != 1 || strings.Contains(w.Body.String(), token) || !strings.Contains(w.Body.String(), `"online":true`) {
		t.Fatalf("put: code=%d probes=%d body=%s", w.Code, probes.Load(), w.Body.String())
	}

	// Ordinary GETs (and refresh=0) serve the cached status without probing again.
	version.Store(2)
	for _, path := range []string{"/api/v1/workers", "/api/v1/workers?refresh=0"} {
		w = ownerRequest(s, "GET", path, "")
		if w.Code != 200 || probes.Load() != 1 || strings.Contains(w.Body.String(), token) || !strings.Contains(w.Body.String(), `"video.download":1`) {
			t.Fatalf("cached %s: code=%d probes=%d body=%s", path, w.Code, probes.Load(), w.Body.String())
		}
	}

	// ?refresh=1 forces a live probe and updates the cache.
	w = ownerRequest(s, "GET", "/api/v1/workers?refresh=1", "")
	if w.Code != 200 || probes.Load() != 2 || strings.Contains(w.Body.String(), token) || !strings.Contains(w.Body.String(), `"video.download":2`) {
		t.Fatalf("refresh=1: code=%d probes=%d body=%s", w.Code, probes.Load(), w.Body.String())
	}
	w = ownerRequest(s, "GET", "/api/v1/workers", "")
	if w.Code != 200 || probes.Load() != 2 || !strings.Contains(w.Body.String(), `"video.download":2`) {
		t.Fatalf("after refresh=1: code=%d probes=%d body=%s", w.Code, probes.Load(), w.Body.String())
	}

	// Saving workers invalidates the cache and probes fresh.
	version.Store(3)
	w = ownerRequest(s, "PUT", "/api/v1/workers", strings.Replace(body, token, "", 1))
	if w.Code != 200 || probes.Load() != 3 || strings.Contains(w.Body.String(), token) || !strings.Contains(w.Body.String(), `"video.download":3`) {
		t.Fatalf("put refresh: code=%d probes=%d body=%s", w.Code, probes.Load(), w.Body.String())
	}
}
