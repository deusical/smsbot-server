package bot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/deusical/smsbot-server/internal/store"
)

// fakeWorker emulates the server-local worker's HTTP protocol offline.
type fakeWorker struct {
	t            *testing.T
	capabilities string // body of GET /capabilities; "" answers 404
	mu           sync.Mutex
	posts        map[string][]map[string]any // job id -> requests
	order        []string
}

func (f *fakeWorker) handler(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer SYNTHETIC" {
		f.t.Error("missing auth")
	}
	if strings.HasSuffix(r.URL.Path, "/capabilities") {
		if f.capabilities == "" {
			w.WriteHeader(404)
			return
		}
		w.Write([]byte(f.capabilities))
		return
	}
	parts := strings.Split(r.URL.Path, "/")
	if r.Method == "GET" {
		w.Header().Set("Content-Type", "video/mp4")
		w.Write([]byte("synthetic-video"))
		return
	}
	job := parts[len(parts)-1]
	var value map[string]any
	if err := json.NewDecoder(r.Body).Decode(&value); err != nil {
		f.t.Error(err)
	}
	if f.posts == nil {
		f.posts = map[string][]map[string]any{}
	}
	if len(f.posts[job]) == 0 {
		f.order = append(f.order, job)
	}
	f.posts[job] = append(f.posts[job], value)
	stage := "downloading"
	if len(f.posts[job]) > 1 {
		stage = "complete"
	}
	answer := map[string]any{"stage": stage}
	if p, ok := value["protocol"].(float64); ok && p >= 2 {
		answer["protocol"] = p
	}
	json.NewEncoder(w).Encode(answer)
}

func runVideoCommand(t *testing.T, worker *fakeWorker) (*Engine, string, []string) {
	t.Helper()
	e, _ := setup(t)
	arm(t, e)
	c := DefaultConfiguration()
	c.Commands = []Command{{Name: "download", Kind: "media.video", Enabled: true, Settings: json.RawMessage(`{"preset":"fast","passes":1,"execution_minutes":10}`)}}
	if err := e.SetConfiguration(c); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(worker.handler))
	t.Cleanup(server.Close)
	warnings := []string{}
	e.Downloader = &HTTPDownloader{Endpoint: server.URL + "/download", Token: "SYNTHETIC", Warning: func(code string) { warnings = append(warnings, code) }}
	m := incoming()
	m.Text = "!download https://v.douyin.com/synthetic/"
	if ok, err := e.Accept(context.Background(), m); !ok || err != nil {
		t.Fatal(ok, err)
	}
	id := store.Hash(m.ConversationID + "\x00" + m.ID)
	for i := 0; i < 8 && stateOf(t, e, id) == "queued"; i++ {
		if _, err := e.Store.DB.Exec("UPDATE job_work SET next_poll=0"); err != nil {
			t.Fatal(err)
		}
		if err := e.Dispatch(context.Background()); err != nil {
			t.Fatal(err)
		}
		finishDownloadUploads(t, e)
	}
	return e, id, warnings
}

func TestVideoProtocolV3DownloadsThenCompresses(t *testing.T) {
	worker := &fakeWorker{t: t, capabilities: `{"protocol":2,"protocols":[1,2,3]}`}
	e, id, warnings := runVideoCommand(t, worker)
	if got := stateOf(t, e, id); got != "submitted" || len(warnings) != 0 {
		t.Fatal("state", got, warnings)
	}
	j := store.Job{ID: id}
	download, compress := workerJob(j, "source"), workerJob(j, "video")
	if len(worker.order) != 2 || worker.order[0] != download || worker.order[1] != compress {
		t.Fatal("jobs", worker.order)
	}
	d := worker.posts[download][0]
	if d["op"] != "download" || d["scope"] != id || d["url"] != "https://v.douyin.com/synthetic/" || d["settings"] != nil || d["protocol"] != float64(3) {
		t.Fatalf("download request %v", d)
	}
	c := worker.posts[compress][0]
	settings, _ := c["settings"].(map[string]any)
	if c["op"] != "compress" || c["scope"] != id || c["source"] != download || c["url"] != nil || settings["preset"] != "fast" || settings["passes"] != float64(1) || settings["execution_minutes"] != float64(10) {
		t.Fatalf("compress request %v", c)
	}
}

func TestVideoWithoutProtocolV3RunsOneCombinedJob(t *testing.T) {
	for _, tc := range []struct{ name, capabilities, want string }{
		{"v2", `{"protocol":2}`, "download_worker_v3_unavailable_using_v2"},
		{"v1", "", "download_worker_v3_unavailable_using_v2,download_worker_outdated_using_defaults"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			worker := &fakeWorker{t: t, capabilities: tc.capabilities}
			e, id, warnings := runVideoCommand(t, worker)
			if got := stateOf(t, e, id); got != "submitted" || strings.Join(warnings, ",") != tc.want {
				t.Fatal("state", got, warnings)
			}
			if len(worker.order) != 1 || worker.order[0] != id {
				t.Fatal("combined job not used", worker.order)
			}
			request := worker.posts[id][0]
			if request["url"] != "https://v.douyin.com/synthetic/" || request["op"] != nil {
				t.Fatalf("request %v", request)
			}
			if tc.name == "v2" && (request["protocol"] != float64(2) || request["settings"].(map[string]any)["preset"] != "fast") {
				t.Fatal("v2 lost the compress settings")
			}
			if tc.name == "v1" && (request["protocol"] != nil || request["settings"] != nil) {
				t.Fatal("v1 worker given settings")
			}
		})
	}
}

// A saved pipeline that pinned the combined video.download@1 keeps working.
func TestCombinedVideoStepUpgrades(t *testing.T) {
	p := Pipeline{Steps: []PipelineStep{
		step("link", "text.extract_url", map[string]string{"text": "trigger.args"}, ""),
		{ID: "video", Module: "video.download", Version: 1, Inputs: map[string]string{"url": "link.url"}, Settings: json.RawMessage(`{"preset":"fast"}`)},
		reply(map[string]string{"file": "video.video"}),
	}}
	if err := validatePipeline(&p, pipelineEnv{}); err != nil {
		t.Fatal(err)
	}
	// Long ids are shortened at the end, keeping a valid id; an unpinned step
	// with compression settings or a ".video" reader is the old kind too.
	for _, id := range []string{"douyin_video_downloader", "x"} {
		for _, version := range []int{1, 0} {
			long := Pipeline{Steps: []PipelineStep{
				step("link", "text.extract_url", map[string]string{"text": "trigger.args"}, ""),
				{ID: id, Module: "video.download", Version: version, Inputs: map[string]string{"url": "link.url"}},
				reply(map[string]string{"file": id + ".video"}),
			}}
			if err := validatePipeline(&long, pipelineEnv{}); err != nil || long.Steps[2].Module != "video.compress" {
				t.Fatal(id, version, err)
			}
		}
	}
	if len(p.Steps) != 4 || p.Steps[1].Module != "video.download" || p.Steps[1].Version != 2 || p.Steps[2].ID != "video" || p.Steps[2].Module != "video.compress" || !strings.Contains(string(p.Steps[2].Settings), `"fast"`) {
		t.Fatalf("not upgraded: %+v", p.Steps)
	}
}

// An AI answer with no link ends the command with how to use it, without
// asking the worker.
func TestMissingLinkAtDispatchRepliesWithoutDownloading(t *testing.T) {
	e, fixture := setup(t)
	arm(t, e)
	c := configureAI(t, e, false, false)
	c.Commands = append(c.Commands, customCommand("find", Pipeline{Steps: []PipelineStep{
		step("answer", "ai.chat", map[string]string{"prompt": "trigger.args"}, ""),
		step("link", "text.extract_url", map[string]string{"text": "answer.text"}, ""),
		step("source", "video.download", map[string]string{"url": "link.url"}, ""),
		step("video", "video.compress", map[string]string{"source": "source.source"}, ""),
		reply(map[string]string{"file": "video.video"}),
	}}))
	if err := e.SetConfiguration(c); err != nil {
		t.Fatal(err)
	}
	e.Provider = &transcriptProvider{}
	e.Downloader = downloadFunc(func(context.Context, string, int64) ([]byte, error) {
		t.Error("worker asked without a link")
		return nil, nil
	})
	m := incoming()
	m.Text = "!find nothing to see"
	if ok, err := e.Accept(context.Background(), m); !ok || err != nil {
		t.Fatal(ok, err)
	}
	if err := e.Dispatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	id := store.Hash(m.ConversationID + "\x00" + m.ID)
	if got := stateOf(t, e, id); got != "submitted" {
		t.Fatal("state", got)
	}
	for _, text := range fixture.Sent {
		if text != noVideoLink {
			t.Fatalf("sent %q", text)
		}
	}
}
