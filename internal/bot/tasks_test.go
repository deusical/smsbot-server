package bot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
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
	t            *testing.T
	tasks        map[string]int
	down         atomic.Bool
	cancelStatus atomic.Int32
	mu           sync.Mutex
	caps         int
	posts        map[string][]map[string]any
	order        []string
	cancels      []string
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
			w.caps++
			json.NewEncoder(rw).Encode(map[string]any{"protocol": 4, "protocols": []int{4}, "tasks": w.tasks})
		case r.Method == "POST" && len(parts) == 5 && parts[2] == "scopes" && parts[4] == "cancel":
			w.cancels = append(w.cancels, parts[3])
			if code := int(w.cancelStatus.Load()); code != 0 {
				rw.WriteHeader(code)
				return
			}
			json.NewEncoder(rw).Encode(map[string]any{"protocol": 4, "cancelled": 1})
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

func (w *taskWorker) capabilitiesCount() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.caps
}

func (w *taskWorker) setTasks(tasks map[string]int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.tasks = tasks
}

func (w *taskWorker) cancelledScopes() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.cancels...)
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

func TestWorkerStatusCacheRefreshAndInvalidation(t *testing.T) {
	t.Parallel()
	tasks, laptop, local := taskRouter(t, map[string]int{"video.download": 1}, map[string]int{"video.compress": 1})
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	var clockMu sync.Mutex
	tasks.now = func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		return now
	}
	advance := func(d time.Duration) {
		clockMu.Lock()
		now = now.Add(d)
		clockMu.Unlock()
	}

	ctx := context.Background()
	first, err := tasks.Status(ctx)
	if err != nil || len(first) != 2 || !first[0].Online || !first[1].Online || first[1].Tasks["video.download"] != 1 {
		t.Fatalf("first status: %+v err=%v", first, err)
	}
	if local.capabilitiesCount() != 1 || laptop.capabilitiesCount() != 1 {
		t.Fatalf("expected 1 probe each, got local=%d laptop=%d", local.capabilitiesCount(), laptop.capabilitiesCount())
	}

	// Mutating the returned slice or map never mutates the cached copy.
	first[0].Online = false
	first[1].Tasks["mutated"] = 99
	laptop.setTasks(map[string]int{"video.download": 2})
	advance(29 * time.Second)

	cached, err := tasks.Status(ctx)
	if err != nil || !cached[0].Online || cached[1].Tasks["video.download"] != 1 || cached[1].Tasks["mutated"] != 0 {
		t.Fatalf("cached status: %+v err=%v", cached, err)
	}
	if local.capabilitiesCount() != 1 || laptop.capabilitiesCount() != 1 {
		t.Fatalf("cache miss within TTL: local=%d laptop=%d", local.capabilitiesCount(), laptop.capabilitiesCount())
	}

	// RefreshStatus bypasses the cache immediately and updates the cached value.
	refreshed, err := tasks.RefreshStatus(ctx)
	if err != nil || refreshed[1].Tasks["video.download"] != 2 {
		t.Fatalf("refreshed status: %+v err=%v", refreshed, err)
	}
	if local.capabilitiesCount() != 2 || laptop.capabilitiesCount() != 2 {
		t.Fatalf("expected 2 probes after RefreshStatus: local=%d laptop=%d", local.capabilitiesCount(), laptop.capabilitiesCount())
	}
	afterRefresh, err := tasks.Status(ctx)
	if err != nil || afterRefresh[1].Tasks["video.download"] != 2 || laptop.capabilitiesCount() != 2 {
		t.Fatalf("status after refresh: %+v probes=%d err=%v", afterRefresh, laptop.capabilitiesCount(), err)
	}

	// After the 30 s TTL expires, Status probes again.
	laptop.setTasks(map[string]int{"video.download": 3})
	advance(30 * time.Second)
	expired, err := tasks.Status(ctx)
	if err != nil || expired[1].Tasks["video.download"] != 3 || laptop.capabilitiesCount() != 3 {
		t.Fatalf("status after TTL: %+v probes=%d err=%v", expired, laptop.capabilitiesCount(), err)
	}

	// Saving workers invalidates the cache immediately.
	laptop.setTasks(map[string]int{"video.download": 4})
	cfg, err := tasks.Config()
	if err != nil {
		t.Fatal(err)
	}
	if err := tasks.Save(cfg, nil); err != nil {
		t.Fatal(err)
	}
	afterSave, err := tasks.Status(ctx)
	if err != nil || afterSave[1].Tasks["video.download"] != 4 || laptop.capabilitiesCount() != 4 {
		t.Fatalf("status after save: %+v probes=%d err=%v", afterSave, laptop.capabilitiesCount(), err)
	}
}

func TestWorkerCancelSweepDeliversRetriesAndIgnoresNonWorkerStages(t *testing.T) {
	t.Parallel()
	video := map[string]int{"video.download": 1, "video.compress": 1}
	tasks, laptop, local := taskRouter(t, video, video)
	e, _ := setup(t)
	arm(t, e)
	c := DefaultConfiguration()
	c.Commands = []Command{{Name: "download", Kind: "media.video", Enabled: true, Settings: json.RawMessage(`{"preset":"fast","passes":1,"execution_minutes":10}`)}}
	if err := e.SetConfiguration(c); err != nil {
		t.Fatal(err)
	}
	e.Tasks = tasks
	e.WorkerCancelInterval = 15 * time.Millisecond

	// Accept two download jobs and dispatch each once so both have live worker stage ("queued").
	m1 := incoming()
	m1.ID = "msg-cancel"
	m1.Text = "!download https://v.douyin.com/synthetic/"
	if ok, err := e.Accept(context.Background(), m1); !ok || err != nil {
		t.Fatal(ok, err)
	}
	cancelledID := store.Hash(m1.ConversationID + "\x00" + m1.ID)
	if err := e.Dispatch(context.Background()); err != nil {
		t.Fatal(err)
	}

	m2 := incoming()
	m2.ID = "msg-pause"
	m2.Text = "!download https://xhslink.com/synthetic"
	if ok, err := e.Accept(context.Background(), m2); !ok || err != nil {
		t.Fatal(ok, err)
	}
	pausedID := store.Hash(m2.ConversationID + "\x00" + m2.ID)
	if err := e.Dispatch(context.Background()); err != nil {
		t.Fatal(err)
	}

	if err := e.Cancel(cancelledID); err != nil {
		t.Fatal(err)
	}
	if err := e.Pause(true); err != nil {
		t.Fatal(err)
	}

	// Seed active jobs and ended bot-side-stage jobs that must NOT be sent to workers.
	seed := func(id, state, stage string) {
		if _, err := e.Store.Enqueue(id, "demo-chat", "!download", store.Plan{Participant: "demo-peer"}); err != nil {
			t.Fatal(err)
		}
		if err := e.Store.DeferWork(id, stage, time.Now().Add(time.Minute).UnixMilli()); err != nil {
			t.Fatal(err)
		}
		if state != "queued" {
			if err := e.Store.Complete(id, state, "paused"); err != nil {
				t.Fatal(err)
			}
		}
	}
	activeID := store.Hash("active-worker-job")
	seed(activeID, "queued", "downloading")
	for _, stage := range []string{"complete", "receiving", "waiting_upload", "uploading", "ready"} {
		seed(store.Hash("bot-stage-"+stage), "cancelled", stage)
	}

	// Simulate local worker down initially; start Engine.Run so the sweep runs.
	local.down.Store(true)
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		e.Run(ctx, func(err error) { t.Errorf("unexpected engine error: %v", err) })
	}()
	defer func() {
		stop()
		<-done
	}()

	// Laptop receives cancels immediately, while local is unreachable so cancel_sent remains 0.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		got := laptop.cancelledScopes()
		if slices.Contains(got, cancelledID) && slices.Contains(got, pausedID) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got := laptop.cancelledScopes(); !slices.Contains(got, cancelledID) || !slices.Contains(got, pausedID) {
		t.Fatalf("laptop cancels=%v, want %s and %s", got, cancelledID, pausedID)
	}
	var sentCount int
	if err := e.Store.DB.QueryRow("SELECT count(*) FROM job_work WHERE job IN (?,?) AND cancel_sent=1", cancelledID, pausedID).Scan(&sentCount); err != nil || sentCount != 0 {
		t.Fatalf("marked delivered while local worker was down: count=%d err=%v", sentCount, err)
	}

	// Bring local worker back online as an older 404 worker for one sweep, or 200: test 404 compatibility!
	local.cancelStatus.Store(http.StatusNotFound)
	local.down.Store(false)
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if err := e.Store.DB.QueryRow("SELECT count(*) FROM job_work WHERE job IN (?,?) AND cancel_sent=1", cancelledID, pausedID).Scan(&sentCount); err == nil && sentCount == 2 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if sentCount != 2 {
		t.Fatalf("sweep did not retry and mark delivered once local recovered: count=%d local=%v", sentCount, local.cancelledScopes())
	}

	// Active and bot-side-stage jobs were never sent to either worker.
	for _, seen := range append(laptop.cancelledScopes(), local.cancelledScopes()...) {
		if seen != cancelledID && seen != pausedID {
			t.Fatalf("unexpected scope cancelled: %s", seen)
		}
	}

	// No further retries happen after delivery is recorded.
	laptopCount, localCount := len(laptop.cancelledScopes()), len(local.cancelledScopes())
	time.Sleep(50 * time.Millisecond)
	if len(laptop.cancelledScopes()) != laptopCount || len(local.cancelledScopes()) != localCount {
		t.Fatalf("delivered cancels were sent again: laptop %d->%d local %d->%d", laptopCount, len(laptop.cancelledScopes()), localCount, len(local.cancelledScopes()))
	}

	// Live Cancel while Engine.Run is running nudges the sweep immediately; also check 410 older worker response.
	local.cancelStatus.Store(http.StatusGone)
	liveCancelID := store.Hash("live-cancel-job")
	seed(liveCancelID, "queued", "compressing")
	if err := e.Cancel(liveCancelID); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		var delivered int
		if err := e.Store.DB.QueryRow("SELECT cancel_sent FROM job_work WHERE job=?", liveCancelID).Scan(&delivered); err == nil && delivered == 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !slices.Contains(laptop.cancelledScopes(), liveCancelID) || !slices.Contains(local.cancelledScopes(), liveCancelID) {
		t.Fatalf("live cancel not delivered to both workers: laptop=%v local=%v", laptop.cancelledScopes(), local.cancelledScopes())
	}
}

func TestTasksPollHandlesCancelledStageWithoutPanicking(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/capabilities") {
			json.NewEncoder(rw).Encode(map[string]any{"protocol": 4, "protocols": []int{4}, "tasks": map[string]int{"video.download": 1}})
			return
		}
		json.NewEncoder(rw).Encode(map[string]any{"stage": "cancelled", "protocol": 4})
	}))
	t.Cleanup(server.Close)
	e, _ := setup(t)
	tasks := &Tasks{Path: filepath.Join(t.TempDir(), "workers.json"), Store: e.Store,
		Local: &HTTPDownloader{Endpoint: server.URL + "/download", Token: token}}
	st := TaskState{}
	status, err := tasks.Poll(context.Background(), &st, strings.Repeat("a", 64), strings.Repeat("b", 64), time.Now().Add(time.Minute), TaskRequest{Task: "video.download", Version: 1})
	var failed taskFailed
	if status.Stage != "cancelled" || !errorsAs(err, &failed) || failed.code != "task_cancelled" {
		t.Fatalf("status=%+v err=%v", status, err)
	}
}
