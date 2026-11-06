package bot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/deusical/smsbot-server/internal/store"
)

// taskWorker emulates a protocol-4 worker: each job is queued on its first
// poll and complete on the next, with a file output named after the task.
type taskWorker struct {
	t     *testing.T
	tasks map[string]int
	down  atomic.Bool
	mu    sync.Mutex
	posts map[string][]map[string]any
	order []string
}

func (w *taskWorker) serve(t *testing.T, tasks map[string]int) *httptest.Server {
	w.t, w.tasks = t, tasks
	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if w.down.Load() {
			if conn, _, err := rw.(http.Hijacker).Hijack(); err == nil {
				conn.Close()
			}
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+strings.Repeat("s", 40) {
			rw.WriteHeader(401)
			return
		}
		w.mu.Lock()
		defer w.mu.Unlock()
		parts := strings.Split(r.URL.Path, "/")
		switch {
		case strings.HasSuffix(r.URL.Path, "/capabilities"):
			json.NewEncoder(rw).Encode(map[string]any{"protocol": 2, "protocols": []int{1, 2, 3, 4}, "tasks": w.tasks})
		case r.Method == "GET" && len(parts) == 6 && parts[4] == "files":
			rw.Write([]byte("bytes-of-" + parts[5]))
		case r.Method == "POST":
			job := parts[3]
			var req map[string]any
			json.NewDecoder(r.Body).Decode(&req)
			if w.posts == nil {
				w.posts = map[string][]map[string]any{}
			}
			if len(w.posts[job]) == 0 {
				w.order = append(w.order, job)
			}
			w.posts[job] = append(w.posts[job], req)
			if len(w.posts[job]) == 1 {
				json.NewEncoder(rw).Encode(map[string]any{"stage": "queued", "protocol": 4})
				return
			}
			name := map[string]string{"video.download": "original", "video.compress": "video"}[req["task"].(string)]
			if name == "" {
				name = "out"
			}
			json.NewEncoder(rw).Encode(map[string]any{"stage": "complete", "protocol": 4,
				"outputs": map[string]any{name: map[string]any{"type": "video", "handle": job + "/" + name, "size": 9}}})
		default:
			rw.WriteHeader(404)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func (w *taskWorker) jobs() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.order...)
}

func (w *taskWorker) first(job string) map[string]any {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.posts[job]) == 0 {
		return nil
	}
	return w.posts[job][0]
}

var token = strings.Repeat("s", 40)

// taskRouter is a router whose "laptop" worker is the remote one and whose
// local worker is the host's, both fakes. Workers are reached at loopback
// addresses, standing in for Tailscale ones.
func taskRouter(t *testing.T, laptopTasks, localTasks map[string]int) (*Tasks, *taskWorker, *taskWorker) {
	t.Helper()
	laptop, local := &taskWorker{}, &taskWorker{}
	l, h := laptop.serve(t, laptopTasks), local.serve(t, localTasks)
	e, _ := setup(t)
	tasks := &Tasks{Path: filepath.Join(t.TempDir(), "workers.json"), Store: e.Store, Grace: time.Nanosecond, Probe: time.Second,
		Local: &HTTPDownloader{Endpoint: h.URL + "/download", Token: token}}
	cfg := WorkerConfig{Workers: []WorkerEntry{{Name: "laptop", Endpoint: l.URL + "/download"}}}
	if err := tasks.Save(cfg, map[string]string{"laptop": token}); err != nil {
		t.Fatal(err)
	}
	// Remote, as a Tailscale worker is: files arrive in the background.
	_, workers, err := tasks.current()
	if err != nil {
		t.Fatal(err)
	}
	workers["laptop"].Remote = true
	return tasks, laptop, local
}

func pollUntil(t *testing.T, tasks *Tasks, st *TaskState, id string, req TaskRequest, between func()) TaskStatus {
	t.Helper()
	for i := 0; i < 50; i++ {
		status, err := tasks.Poll(context.Background(), st, id, "f"+strings.Repeat("0", 63), time.Now().Add(time.Minute), req)
		if err != nil {
			t.Fatal(err)
		}
		if status.Stage == "complete" {
			return status
		}
		if between != nil {
			between()
		}
	}
	t.Fatal("task never completed")
	return TaskStatus{}
}

func TestTasksRunOnTheFirstWorkerThatAdvertisesThem(t *testing.T) {
	t.Parallel()
	both := map[string]int{"video.download": 1, "text.count": 2}
	tasks, laptop, local := taskRouter(t, both, map[string]int{"video.download": 1})
	// The laptop is first in list order.
	st := TaskState{}
	status := pollUntil(t, tasks, &st, "a"+strings.Repeat("0", 63), TaskRequest{Task: "video.download", Version: 1}, nil)
	if st.Worker != "laptop" || status.Outputs["original"].Handle != "laptop/a"+strings.Repeat("0", 63)+"/original" {
		t.Fatal(st, status)
	}
	// A task only the laptop runs, at a version nobody runs, waits.
	st = TaskState{}
	status, err := tasks.Poll(context.Background(), &st, "b"+strings.Repeat("0", 63), "f"+strings.Repeat("0", 63), time.Now().Add(time.Minute), TaskRequest{Task: "text.count", Version: 3})
	if err != nil || status.Stage != "waiting_worker" || len(laptop.jobs()) != 1 {
		t.Fatal(status, err)
	}
	// A route overrides the order.
	cfg, _ := tasks.Config()
	cfg.Routes = map[string][]string{"video.download": {"local", "laptop"}}
	if err := tasks.Save(cfg, nil); err != nil {
		t.Fatal(err)
	}
	st = TaskState{}
	pollUntil(t, tasks, &st, "c"+strings.Repeat("0", 63), TaskRequest{Task: "video.download", Version: 1}, nil)
	if st.Worker != "local" || len(local.jobs()) != 1 {
		t.Fatal(st, local.jobs())
	}
}

func TestTasksMoveOnOnlyWhenTheyHaveNoInputFile(t *testing.T) {
	t.Parallel()
	tasks, laptop, local := taskRouter(t, map[string]int{"video.download": 1, "video.compress": 1}, map[string]int{"video.download": 1, "video.compress": 1})
	id := "d" + strings.Repeat("0", 63)
	st := TaskState{}
	// The laptop sleeps after taking the job: it is given up after Grace and
	// the job starts again on the local worker.
	pollUntil(t, tasks, &st, id, TaskRequest{Task: "video.download", Version: 1}, func() {
		if len(laptop.jobs()) == 1 {
			laptop.down.Store(true)
		}
	})
	if st.Worker != "local" || len(local.jobs()) != 1 || local.jobs()[0] != id {
		t.Fatal(st, local.jobs())
	}
	// A job reading a file the laptop holds can't move: it fails instead.
	st = TaskState{}
	_, err := tasks.Poll(context.Background(), &st, "e"+strings.Repeat("0", 63), "f"+strings.Repeat("0", 63), time.Now().Add(time.Minute),
		TaskRequest{Task: "video.compress", Version: 1, Inputs: map[string]TaskValue{"source": {Handle: "laptop/" + id + "/original"}}})
	if err != nil || st.Worker != "laptop" {
		t.Fatal(st, err)
	}
	_, err = tasks.Poll(context.Background(), &st, "e"+strings.Repeat("0", 63), "f"+strings.Repeat("0", 63), time.Now().Add(time.Minute),
		TaskRequest{Task: "video.compress", Version: 1, Inputs: map[string]TaskValue{"source": {Handle: "laptop/" + id + "/original"}}})
	var failed taskFailed
	if !errorsAs(err, &failed) || failed.code != "worker_unavailable" {
		t.Fatal(err)
	}
}

func errorsAs(err error, target *taskFailed) bool {
	f, ok := err.(taskFailed)
	if ok {
		*target = f
	}
	return ok
}

func TestTaskInputHandlesReachTheWorkerWithoutItsName(t *testing.T) {
	t.Parallel()
	tasks, laptop, _ := taskRouter(t, map[string]int{"video.compress": 1}, map[string]int{})
	id, source := "1"+strings.Repeat("0", 63), "2"+strings.Repeat("0", 63)
	st := TaskState{}
	status := pollUntil(t, tasks, &st, id, TaskRequest{Task: "video.compress", Version: 1,
		Inputs: map[string]TaskValue{"source": {Handle: "laptop/" + source + "/original"}}, Settings: map[string]any{"max_bytes": 100000}}, nil)
	inputs := laptop.first(id)["inputs"].(map[string]any)
	if inputs["source"].(map[string]any)["handle"] != source+"/original" {
		t.Fatal(inputs)
	}
	// Its output file arrives in the background from a remote worker.
	var data []byte
	for i := 0; i < 200; i++ {
		var stage string
		var err error
		data, stage, err = tasks.File(context.Background(), status.Outputs["video"].Handle, 1000)
		if err != nil {
			t.Fatal(err)
		}
		if stage == "complete" {
			break
		}
		if stage != "receiving" {
			t.Fatal(stage)
		}
		time.Sleep(time.Millisecond)
	}
	if string(data) != "bytes-of-video" {
		t.Fatal(string(data))
	}
}

func TestWorkerConfigFileTokensAndValidation(t *testing.T) {
	t.Parallel()
	e, _ := setup(t)
	dir := t.TempDir()
	tasks := &Tasks{Path: filepath.Join(dir, "workers.json"), Store: e.Store}
	if cfg, err := tasks.Config(); err != nil || len(cfg.Workers) != 0 {
		t.Fatal("missing file", cfg, err)
	}
	cfg := WorkerConfig{Workers: []WorkerEntry{{Name: "laptop", Endpoint: "http://100.92.5.20:8767/download"}},
		Routes: map[string][]string{"video.compress": {"laptop", "local"}}}
	if err := tasks.Save(cfg, map[string]string{"laptop": token}); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(tasks.Path)
	if strings.Contains(string(raw), token) || !tasks.HasToken(cfg.Workers[0]) {
		t.Fatal("token in the file, or not stored", string(raw))
	}
	// A hand edit applies without a restart.
	hand := `{"workers":[{"name":"laptop","endpoint":"http://laptop.tailddfcef.ts.net:8767/download"},{"name":"desk","endpoint":"http://100.64.0.9:8767/download","token_file":"` +
		filepath.ToSlash(filepath.Join(dir, "desk-token")) + `"}]}`
	os.WriteFile(filepath.Join(dir, "desk-token"), []byte(token), 0o600)
	os.WriteFile(tasks.Path, []byte(hand), 0o600)
	future := time.Now().Add(time.Hour)
	os.Chtimes(tasks.Path, future, future)
	got, err := tasks.Config()
	if err != nil || len(got.Workers) != 2 || got.Workers[0].Endpoint != "http://laptop.tailddfcef.ts.net:8767/download" {
		t.Fatal(got, err)
	}
	if _, workers, _ := tasks.current(); workers["desk"] == nil || workers["laptop"] == nil || !workers["desk"].Remote {
		t.Fatal("workers", workers)
	}
	// Removing a worker deletes its stored token.
	if err := tasks.Save(WorkerConfig{Workers: []WorkerEntry{}}, nil); err != nil {
		t.Fatal(err)
	}
	if tasks.HasToken(WorkerEntry{Name: "laptop"}) {
		t.Fatal("token kept")
	}
	for _, bad := range []WorkerConfig{
		{Workers: []WorkerEntry{{Name: "local", Endpoint: "http://100.92.5.20/download"}}},
		{Workers: []WorkerEntry{{Name: "a", Endpoint: "http://192.168.1.2/download"}}},
		{Workers: []WorkerEntry{{Name: "a", Endpoint: "https://100.92.5.20/download"}}},
		{Workers: []WorkerEntry{{Name: "a", Endpoint: "http://100.92.5.20/download"}, {Name: "a", Endpoint: "http://100.92.5.21/download"}}},
		{Routes: map[string][]string{"video.compress": {"nobody"}}},
		{Routes: map[string][]string{"Bad Task": {"local"}}},
		{Routes: map[string][]string{"video.compress": {"local", "local"}}},
	} {
		if err := tasks.Save(bad, nil); err == nil {
			t.Fatal("accepted", bad)
		}
	}
	if err := tasks.Save(WorkerConfig{Workers: []WorkerEntry{{Name: "a", Endpoint: "http://100.92.5.20/download"}}}, map[string]string{"a": "short"}); err == nil {
		t.Fatal("accepted a short token")
	}
}

// runTaskVideo runs one video command through tasks until it leaves the queue.
func runTaskVideo(t *testing.T, tasks *Tasks, between func()) (*Engine, int, string) {
	t.Helper()
	e, f := setup(t)
	arm(t, e)
	c := DefaultConfiguration()
	c.Commands = []Command{{Name: "download", Kind: "media.video", Enabled: true, Settings: json.RawMessage(`{"preset":"fast","passes":1,"execution_minutes":10}`)}}
	if err := e.SetConfiguration(c); err != nil {
		t.Fatal(err)
	}
	e.Tasks = tasks
	m := incoming()
	m.Text = "!download https://v.douyin.com/synthetic/"
	if ok, err := e.Accept(context.Background(), m); !ok || err != nil {
		t.Fatal(ok, err)
	}
	id := store.Hash(m.ConversationID + "\x00" + m.ID)
	for i := 0; i < 400 && stateOf(t, e, id) == "queued"; i++ {
		if _, err := e.Store.DB.Exec("UPDATE job_work SET next_poll=0"); err != nil {
			t.Fatal(err)
		}
		if err := e.Dispatch(context.Background()); err != nil {
			t.Fatal(err)
		}
		finishDownloadUploads(t, e)
		if between != nil {
			between()
		}
		time.Sleep(2 * time.Millisecond)
	}
	return e, len(f.Sent), id
}

func TestVideoCommandRunsAsTasksOnTheLaptop(t *testing.T) {
	t.Parallel()
	video := map[string]int{"video.download": 1, "video.compress": 1}
	tasks, laptop, local := taskRouter(t, video, video)
	e, sent, id := runTaskVideo(t, tasks, nil)
	if got := stateOf(t, e, id); got != "submitted" || sent != 1 {
		t.Fatal(got, sent)
	}
	j := store.Job{ID: id}
	download, compress := workerJob(j, "source"), workerJob(j, "video")
	if got := laptop.jobs(); len(got) != 2 || got[0] != download || got[1] != compress || len(local.jobs()) != 0 {
		t.Fatal(got, local.jobs())
	}
	d, c := laptop.first(download), laptop.first(compress)
	if d["task"] != "video.download" || d["scope"] != id || d["inputs"].(map[string]any)["url"].(map[string]any)["text"] != "https://v.douyin.com/synthetic/" {
		t.Fatalf("download %v", d)
	}
	settings := c["settings"].(map[string]any)
	if c["task"] != "video.compress" || c["inputs"].(map[string]any)["source"].(map[string]any)["handle"] != download+"/original" ||
		settings["preset"] != "fast" || settings["max_bytes"] == nil {
		t.Fatalf("compress %v", c)
	}
}

func TestVideoCommandFailsWhenTheLaptopLeavesWithTheOriginal(t *testing.T) {
	t.Parallel()
	video := map[string]int{"video.download": 1, "video.compress": 1}
	tasks, laptop, local := taskRouter(t, video, video)
	e, sent, id := runTaskVideo(t, tasks, func() {
		if len(laptop.jobs()) == 2 {
			laptop.down.Store(true)
		}
	})
	var code string
	e.Store.DB.QueryRow("SELECT code FROM jobs WHERE id=?", id).Scan(&code)
	if got := stateOf(t, e, id); got != "failed" || code != "worker_unavailable" || sent != 0 || len(local.jobs()) != 0 {
		t.Fatal(got, code, sent, local.jobs())
	}
}
