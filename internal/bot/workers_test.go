package bot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/deusical/smsbot-server/internal/store"
	"github.com/deusical/smsbot-server/internal/transport"
)

// pooledWorker is a fakeWorker that can be taken off the network: while down,
// every connection is dropped, as when the PC sleeps.
type pooledWorker struct {
	fakeWorker
	down atomic.Bool
}

func (p *pooledWorker) serve(t *testing.T) *httptest.Server {
	p.t, p.capabilities = t, `{"protocol":2,"protocols":[1,2,3]}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if p.down.Load() {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				conn.Close()
			}
			return
		}
		p.handler(w, r)
	}))
	t.Cleanup(server.Close)
	return server
}

func (p *pooledWorker) jobs() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.order...)
}

func (p *pooledWorker) first(job string) map[string]any {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.posts[job]) == 0 {
		return nil
	}
	return p.posts[job][0]
}

// runPooled runs one video command through pool, calling between after each
// dispatch round, until the job leaves the queue.
func runPooled(t *testing.T, pool *WorkerPool, between func()) (*Engine, *transport.Fixture, string) {
	t.Helper()
	e, f := setup(t)
	arm(t, e)
	c := DefaultConfiguration()
	c.Commands = []Command{{Name: "download", Kind: "media.video", Enabled: true, Settings: json.RawMessage(`{"preset":"fast","passes":1,"execution_minutes":10}`)}}
	if err := e.SetConfiguration(c); err != nil {
		t.Fatal(err)
	}
	e.Downloader = pool
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
		time.Sleep(2 * time.Millisecond) // lets a background result fetch finish
	}
	return e, f, id
}

func twoWorkers(t *testing.T) (*WorkerPool, *pooledWorker, *pooledWorker) {
	remote, local := &pooledWorker{}, &pooledWorker{}
	r, l := remote.serve(t), local.serve(t)
	pool := &WorkerPool{Grace: time.Nanosecond, Probe: time.Second, Workers: []NamedWorker{
		{Name: "lenovo", HTTPDownloader: &HTTPDownloader{Endpoint: r.URL + "/download", Token: "SYNTHETIC", Remote: true}},
		{Name: "local", HTTPDownloader: &HTTPDownloader{Endpoint: l.URL + "/download", Token: "SYNTHETIC"}},
	}}
	return pool, remote, local
}

func TestWorkerPoolKeepsBothStepsOnTheFirstReachableWorker(t *testing.T) {
	t.Parallel()
	pool, remote, local := twoWorkers(t)
	e, f, id := runPooled(t, pool, nil)
	if got := stateOf(t, e, id); got != "submitted" || len(f.Sent) != 1 {
		t.Fatal("state", got, f.Sent)
	}
	j := store.Job{ID: id}
	download, compress := workerJob(j, "source"), workerJob(j, "video")
	if got := remote.jobs(); len(got) != 2 || got[0] != download || got[1] != compress {
		t.Fatal("remote jobs", got)
	}
	if got := local.jobs(); len(got) != 0 {
		t.Fatal("local worker used", got)
	}
	// The compress job reads the original by its job ID, never the routed handle.
	if c := remote.first(compress); c["op"] != "compress" || c["source"] != download {
		t.Fatalf("compress request %v", c)
	}
}

func TestWorkerPoolSkipsAnUnreachableWorker(t *testing.T) {
	t.Parallel()
	pool, remote, local := twoWorkers(t)
	remote.down.Store(true)
	e, f, id := runPooled(t, pool, nil)
	if got := stateOf(t, e, id); got != "submitted" || len(f.Sent) != 1 {
		t.Fatal("state", got, f.Sent)
	}
	if got := local.jobs(); len(got) != 2 || len(remote.jobs()) != 0 {
		t.Fatal("jobs", got, remote.jobs())
	}
}

func TestWorkerPoolRestartsTheDownloadWhenItsWorkerDisappears(t *testing.T) {
	t.Parallel()
	pool, remote, local := twoWorkers(t)
	j := func(id string) store.Job { return store.Job{ID: id} }
	var job string
	e, f, id := runPooled(t, pool, func() {
		// The PC goes to sleep while still downloading.
		if job == "" && len(remote.jobs()) == 1 {
			job = remote.jobs()[0]
			remote.down.Store(true)
		}
	})
	if got := stateOf(t, e, id); got != "submitted" || len(f.Sent) != 1 {
		t.Fatal("state", got, f.Sent)
	}
	download, compress := workerJob(j(id), "source"), workerJob(j(id), "video")
	if job != download {
		t.Fatal("remote never started the download", job)
	}
	if got := local.jobs(); len(got) != 2 || got[0] != download || got[1] != compress {
		t.Fatal("local jobs", got)
	}
	if c := local.first(compress); c["op"] != "compress" || c["source"] != download {
		t.Fatalf("compress request %v", c)
	}
}

func TestWorkerPoolStartsOverWhenTheOriginalsWorkerDisappears(t *testing.T) {
	t.Parallel()
	pool, remote, local := twoWorkers(t)
	e, f, id := runPooled(t, pool, func() {
		// The PC goes to sleep after downloading, before compressing.
		if len(remote.jobs()) == 2 {
			remote.down.Store(true)
		}
	})
	if got := stateOf(t, e, id); got != "submitted" || len(f.Sent) != 1 {
		t.Fatal("state", got, f.Sent)
	}
	compress := workerJob(store.Job{ID: id}, "video")
	// The local worker never had the original, so it downloads and compresses
	// in one job from the saved link, with the step's settings.
	if got := local.jobs(); len(got) != 1 || got[0] != compress {
		t.Fatal("local jobs", got)
	}
	c := local.first(compress)
	settings, _ := c["settings"].(map[string]any)
	if c["url"] != "https://v.douyin.com/synthetic/" || c["op"] != nil || c["protocol"] != float64(2) || settings["preset"] != "fast" {
		t.Fatalf("fallback request %v", c)
	}
}

func TestRemoteResultArrivesInTheBackground(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release // a slow transfer, longer than a dispatch poll may block
		w.Header().Set("Content-Type", "video/mp4")
		w.Write([]byte("synthetic-video"))
	}))
	t.Cleanup(server.Close)
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	d := &HTTPDownloader{Endpoint: server.URL + "/download", Token: "SYNTHETIC", Remote: true}
	for i := 0; i < 2; i++ {
		if data, stage, err := d.result(context.Background(), "job", 1000); data != nil || stage != "receiving" || err != nil {
			t.Fatal("poll", i, data, stage, err)
		}
	}
	close(release)
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
		data, stage, err := d.result(context.Background(), "job", 1000)
		if stage == "receiving" {
			continue
		}
		if string(data) != "synthetic-video" || stage != "complete" || err != nil {
			t.Fatal(data, stage, err)
		}
		return
	}
	t.Fatal("result never arrived")
}

func TestWorkerWaitGivesUpOnlyAfterGrace(t *testing.T) {
	t.Parallel()
	now := time.UnixMilli(1_000_000)
	pool := &WorkerPool{Grace: time.Minute, now: func() time.Time { return now }, Workers: []NamedWorker{{Name: "a"}, {Name: "b"}, {Name: "local"}}}
	worker, since := "a", int64(0)
	for _, step := range []struct {
		advance time.Duration
		stage   string
		moved   bool
		want    string
	}{
		{0, "waiting_worker", false, "a"},
		{59 * time.Second, "waiting_worker", false, "a"},
		{2 * time.Second, "downloading", false, "a"}, // answered again: the clock resets
		{0, "waiting_worker", false, "a"},
		{time.Minute, "waiting_worker", true, "b"},
		{0, "waiting_worker", false, "b"},
		{time.Minute, "waiting_worker", true, "local"},
		{time.Hour, "waiting_worker", false, "local"}, // the last worker is never given up on
		{time.Hour, "waiting_worker", false, "local"},
	} {
		now = now.Add(step.advance)
		if moved := pool.wait(&worker, &since, step.stage); moved != step.moved || worker != step.want {
			t.Fatalf("after %v %s: moved %v to %s, want %v %s", step.advance, step.stage, moved, worker, step.moved, step.want)
		}
	}
}

func TestNewRemoteWorkerOnlyOverTailscale(t *testing.T) {
	t.Parallel()
	token := strings.Repeat("t", 40)
	for _, ok := range []string{"http://100.92.5.20:8767/download", "http://lenovo.tailddfcef.ts.net:8767/download", "http://127.0.0.1:8767/download"} {
		w, err := NewRemoteWorker("lenovo", ok, token)
		if err != nil {
			t.Fatal(ok, err)
		}
		if w.Remote != !strings.Contains(ok, "127.0.0.1") {
			t.Fatal("remote", ok, w.Remote)
		}
	}
	for _, bad := range []string{"http://192.168.1.20:8767/download", "http://10.0.0.5/download", "http://lenovo:8767/download",
		"https://100.92.5.20/download", "http://user:pass@100.92.5.20/download", "http://100.128.0.1/download", "http://evil.ts.net.example.com/download"} {
		if _, err := NewRemoteWorker("lenovo", bad, token); err == nil {
			t.Fatal("accepted", bad)
		}
	}
	for _, name := range []string{"local", "", "Lenovo", "a b", strings.Repeat("a", 25)} {
		if _, err := NewRemoteWorker(name, "http://100.92.5.20:8767/download", token); err == nil {
			t.Fatal("accepted name", name)
		}
	}
	if _, err := NewRemoteWorker("lenovo", "http://100.92.5.20:8767/download", "short"); err == nil {
		t.Fatal("accepted a short token")
	}
}
