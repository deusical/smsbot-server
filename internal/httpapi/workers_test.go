package httpapi

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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
