package bot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/deusical/smsbot-server/internal/store"
)

// Workers run tasks for the bot: named, versioned units of work such as
// "video.compress@1". The SmsBot host's own worker is "local"; others run on
// other machines and are reached over Tailscale. workers.json in the data
// directory lists them and, per task, the workers allowed to run it; the
// dashboard edits the same file. A step asks Tasks for its task; Tasks picks
// the first worker in order that advertises it and answers, keeps the job
// there, and fetches its output files. Workers only prepare outputs: only the
// bot sends, after its own checks.

// LocalWorker is the name of the SmsBot host's own worker.
const LocalWorker = "local"

// WorkerEntry is one worker on another machine, as saved in workers.json.
type WorkerEntry struct {
	Name     string `json:"name"`
	Endpoint string `json:"endpoint"`
	// TokenFile is for configuration written by hand. A token entered on the
	// dashboard is kept in the encrypted store, never in this file.
	TokenFile string `json:"token_file,omitempty"`
}

// WorkerConfig is workers.json.
type WorkerConfig struct {
	Workers []WorkerEntry `json:"workers"`
	// Routes lists, per task name, the workers that may run it, in order of
	// preference ("local" is the host's own worker). A task without a route
	// may run on every worker, in list order, then local.
	Routes map[string][]string `json:"routes,omitempty"`
}

var (
	workerName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,23}$`)
	taskName   = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)+$`)
	taskWord   = regexp.MustCompile(`^[a-z][a-z0-9_]{0,47}$`)
	// tailnet is Tailscale's address range. Tailscale encrypts and
	// authenticates the link, so plain HTTP there never exposes a token.
	tailnet = netip.MustParsePrefix("100.64.0.0/10")
)

// validEndpoint accepts plain HTTP to a Tailscale address, a *.ts.net name or
// loopback, and reports whether it is on another machine.
func validEndpoint(endpoint string) (remote bool, err error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "http" || u.User != nil || u.Hostname() == "" || u.RawQuery != "" || u.Fragment != "" {
		return false, errors.New("endpoint must be http://host:port/download")
	}
	host := u.Hostname()
	if addr, err := netip.ParseAddr(host); err == nil {
		if addr.IsLoopback() || tailnet.Contains(addr) {
			return !addr.IsLoopback(), nil
		}
	} else if net.ParseIP(host) == nil && strings.HasSuffix(strings.ToLower(host), ".ts.net") {
		return true, nil
	}
	return false, errors.New("endpoint must be a Tailscale 100.x address or *.ts.net name")
}

// Validate checks names, endpoints and routes.
func (c WorkerConfig) Validate() error {
	seen := map[string]bool{LocalWorker: true}
	for _, w := range c.Workers {
		if !workerName.MatchString(w.Name) || w.Name == LocalWorker {
			return fmt.Errorf("worker names are 1-24 lowercase letters, digits or -, and not %q", LocalWorker)
		}
		if seen[w.Name] && w.Name != LocalWorker {
			return fmt.Errorf("worker %s is listed twice", w.Name)
		}
		seen[w.Name] = true
		if _, err := validEndpoint(w.Endpoint); err != nil {
			return fmt.Errorf("worker %s: %w", w.Name, err)
		}
	}
	for task, names := range c.Routes {
		if !taskName.MatchString(task) {
			return fmt.Errorf("route %q: task names are dotted lowercase words", task)
		}
		used := map[string]bool{}
		for _, n := range names {
			if !seen[n] || used[n] {
				return fmt.Errorf("route %s: unknown or repeated worker %q", task, n)
			}
			used[n] = true
		}
	}
	return nil
}

// TaskValue is a task input or output: text, or a file held by a worker.
// Handles are "<worker>/<job>/<name>" in the bot and "<job>/<name>" on the
// worker that holds the file.
type TaskValue struct {
	Type   string `json:"type,omitempty"`
	Text   string `json:"text,omitempty"`
	Handle string `json:"handle,omitempty"`
	Size   int64  `json:"size,omitempty"`
}

// TaskRequest is what a step asks for.
type TaskRequest struct {
	Task     string
	Version  int
	Inputs   map[string]TaskValue
	Settings any
}

// TaskStatus is a worker's answer to one poll.
type TaskStatus struct {
	Stage   string               `json:"stage"`
	Outputs map[string]TaskValue `json:"outputs,omitempty"`
	Code    string               `json:"code,omitempty"`
}

// TaskState is what a step keeps between polls of its task: the worker running
// it and since when (Unix ms) that worker has been unreachable.
type TaskState struct {
	Worker  string `json:"worker,omitempty"`
	Waiting int64  `json:"waiting,omitempty"`
	// Stage is the worker's last progress word, kept with the step.
	Stage string `json:"stage,omitempty"`
}

// taskFailed is a task's failure code, which becomes the job's.
type taskFailed struct{ code string }

func (f taskFailed) Error() string { return f.code }

// Tasks routes task jobs to workers. Configure it once; it is safe for
// concurrent use.
type Tasks struct {
	// Local is the host's own worker, or nil.
	Local *HTTPDownloader
	// Path is workers.json. A missing file means no other workers.
	Path string
	// Store keeps tokens entered on the dashboard, encrypted.
	Store *store.Store
	// Grace is how long a worker may be unreachable before its job moves on
	// (or, when it holds the job's input file, fails); 0 means two minutes.
	Grace time.Duration
	// Probe bounds a capabilities request; 0 means two seconds.
	Probe time.Duration
	now   func() time.Time

	mu      sync.Mutex
	stamp   time.Time
	loaded  bool
	cfg     WorkerConfig
	workers map[string]*HTTPDownloader
}

func tokenKey(name string) string { return "worker_token:" + name }

// Config returns the current configuration, reloading workers.json when it
// changed on disk, so hand edits apply without a restart.
func (t *Tasks) Config() (WorkerConfig, error) {
	cfg, _, err := t.current()
	return cfg, err
}

func (t *Tasks) current() (WorkerConfig, map[string]*HTTPDownloader, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	var stamp time.Time
	info, err := os.Stat(t.Path)
	if err == nil {
		stamp = info.ModTime()
	} else if !errors.Is(err, os.ErrNotExist) {
		return WorkerConfig{}, nil, err
	}
	if t.loaded && stamp.Equal(t.stamp) {
		return t.cfg, t.workers, nil
	}
	var cfg WorkerConfig
	if !stamp.IsZero() {
		raw, err := os.ReadFile(t.Path)
		if err != nil {
			return WorkerConfig{}, nil, err
		}
		d := json.NewDecoder(bytes.NewReader(raw))
		d.DisallowUnknownFields()
		if err := d.Decode(&cfg); err != nil {
			return WorkerConfig{}, nil, fmt.Errorf("workers.json: %w", err)
		}
		if err := cfg.Validate(); err != nil {
			return WorkerConfig{}, nil, fmt.Errorf("workers.json: %w", err)
		}
	}
	workers := map[string]*HTTPDownloader{}
	if t.Local != nil {
		workers[LocalWorker] = t.Local
	}
	for _, w := range cfg.Workers {
		token, err := t.token(w)
		if err != nil {
			return WorkerConfig{}, nil, err
		}
		if token == "" {
			continue // configured but no token yet: not usable
		}
		remote, _ := validEndpoint(w.Endpoint)
		if old := t.workers[w.Name]; old != nil && old.Endpoint == w.Endpoint && old.Token == token {
			workers[w.Name] = old // keeps background fetches in flight
			continue
		}
		workers[w.Name] = &HTTPDownloader{Endpoint: w.Endpoint, Token: token, Remote: remote}
	}
	t.cfg, t.workers, t.stamp, t.loaded = cfg, workers, stamp, true
	return cfg, workers, nil
}

func (t *Tasks) token(w WorkerEntry) (string, error) {
	if w.TokenFile != "" {
		raw, err := os.ReadFile(w.TokenFile)
		if err != nil || len(strings.TrimSpace(string(raw))) < 32 {
			return "", fmt.Errorf("worker %s: token file unavailable or invalid", w.Name)
		}
		return strings.TrimSpace(string(raw)), nil
	}
	if t.Store == nil {
		return "", nil
	}
	raw, err := t.Store.GetSecretSetting(tokenKey(w.Name), tokenKey(w.Name))
	return strings.TrimSpace(string(raw)), err
}

// HasToken reports whether a worker has a usable token.
func (t *Tasks) HasToken(w WorkerEntry) bool {
	token, err := t.token(w)
	return err == nil && len(token) >= 32
}

// Save replaces the configuration. tokens sets the dashboard token of the
// named workers ("" keeps the current one); a removed worker's token is
// deleted. workers.json is written in one step, owner-only.
func (t *Tasks) Save(cfg WorkerConfig, tokens map[string]string) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	keep := map[string]bool{}
	for _, w := range cfg.Workers {
		keep[w.Name] = true
		if token := strings.TrimSpace(tokens[w.Name]); token != "" {
			if len(token) < 32 || w.TokenFile != "" {
				return fmt.Errorf("worker %s: a token is at least 32 characters, and not set with a token file", w.Name)
			}
			if err := t.Store.SetSecretSetting(tokenKey(w.Name), tokenKey(w.Name), []byte(token)); err != nil {
				return err
			}
		}
	}
	old, _ := t.Config()
	for _, w := range old.Workers {
		if !keep[w.Name] {
			if err := t.Store.SetSecretSetting(tokenKey(w.Name), tokenKey(w.Name), nil); err != nil {
				return err
			}
		}
	}
	raw, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(t.Path), 0o700); err != nil {
		return err
	}
	tmp := t.Path + ".tmp"
	if err := os.WriteFile(tmp, append(raw, '\n'), 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, t.Path); err != nil {
		return err
	}
	t.mu.Lock()
	t.loaded = false // reload even if the clock didn't move the file's time
	t.mu.Unlock()
	return nil
}

// WorkerStatus is one worker as the dashboard shows it.
type WorkerStatus struct {
	Name     string         `json:"name"`
	Endpoint string         `json:"endpoint,omitempty"`
	Token    bool           `json:"token"`
	Online   bool           `json:"online"`
	Tasks    map[string]int `json:"tasks"`
}

// Status probes every worker, local first.
func (t *Tasks) Status(ctx context.Context) ([]WorkerStatus, error) {
	cfg, workers, err := t.current()
	if err != nil {
		return nil, err
	}
	list := []WorkerStatus{}
	if t.Local != nil {
		list = append(list, WorkerStatus{Name: LocalWorker, Token: true})
	}
	for _, w := range cfg.Workers {
		list = append(list, WorkerStatus{Name: w.Name, Endpoint: w.Endpoint, Token: t.HasToken(w)})
	}
	var wg sync.WaitGroup
	for i := range list {
		d := workers[list[i].Name]
		list[i].Tasks = map[string]int{}
		if d == nil {
			continue
		}
		wg.Add(1)
		go func(s *WorkerStatus) {
			defer wg.Done()
			if tasks, ok := t.capabilities(ctx, d); ok {
				s.Online, s.Tasks = true, tasks
			}
		}(&list[i])
	}
	wg.Wait()
	return list, nil
}

func (t *Tasks) probe() time.Duration {
	if t.Probe > 0 {
		return t.Probe
	}
	return 2 * time.Second
}

func (t *Tasks) grace() time.Duration {
	if t.Grace > 0 {
		return t.Grace
	}
	return 2 * time.Minute
}

func (t *Tasks) clock() time.Time {
	if t.now != nil {
		return t.now()
	}
	return time.Now()
}

// capabilities asks a worker which tasks it runs, within Probe.
func (t *Tasks) capabilities(ctx context.Context, d *HTTPDownloader) (map[string]int, bool) {
	ctx, cancel := context.WithTimeout(ctx, t.probe())
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, "GET", strings.TrimRight(d.Endpoint, "/")+"/capabilities", nil)
	if err != nil {
		return nil, false
	}
	request.Header.Set("Authorization", "Bearer "+d.Token)
	response, err := d.client().Do(request)
	if err != nil {
		return nil, false
	}
	defer response.Body.Close()
	var caps struct {
		Tasks map[string]int `json:"tasks"`
	}
	if response.StatusCode != 200 || json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&caps) != nil {
		return nil, false
	}
	if caps.Tasks == nil {
		caps.Tasks = map[string]int{}
	}
	return caps.Tasks, true
}

// order is the workers that may run task, in order of preference.
func order(cfg WorkerConfig, task string, local bool) []string {
	if names, ok := cfg.Routes[task]; ok {
		return names
	}
	names := []string{}
	for _, w := range cfg.Workers {
		names = append(names, w.Name)
	}
	if local {
		names = append(names, LocalWorker)
	}
	return names
}

// TaskClient runs one named task and fetches a file it produced.
type TaskClient interface {
	Poll(ctx context.Context, st *TaskState, id, scope string, deadline time.Time, req TaskRequest) (TaskStatus, error)
	File(ctx context.Context, handle string, limit int64) ([]byte, string, error)
}

// Poll runs one poll of a step's task job id, for bot job scope. The first
// poll chooses a worker; later polls keep the job there. A task with a file
// input runs on the worker holding the file. A worker unreachable for Grace
// is given up: the job starts again on another worker, or, if that worker held
// its input file, fails with "worker_unavailable".
func (t *Tasks) Poll(ctx context.Context, st *TaskState, id, scope string, deadline time.Time, req TaskRequest) (TaskStatus, error) {
	cfg, workers, err := t.current()
	if err != nil {
		return TaskStatus{}, err
	}
	holder := ""
	inputs := map[string]TaskValue{}
	for name, v := range req.Inputs {
		if v.Handle != "" {
			w, rest, ok := strings.Cut(v.Handle, "/")
			if !ok || holder != "" && holder != w {
				return TaskStatus{}, taskFailed{"task_inputs_unavailable"}
			}
			holder, v.Handle = w, rest
		}
		inputs[name] = v
	}
	if holder != "" {
		st.Worker = holder
	}
	if workers[st.Worker] == nil {
		if holder != "" {
			return TaskStatus{}, taskFailed{"worker_unavailable"}
		}
		st.Worker, st.Waiting = "", 0
		for _, name := range order(cfg, req.Task, t.Local != nil) {
			if d := workers[name]; d != nil {
				if tasks, ok := t.capabilities(ctx, d); ok && tasks[req.Task] == req.Version {
					st.Worker = name
					break
				}
			}
		}
		if st.Worker == "" {
			return TaskStatus{Stage: "waiting_worker"}, nil
		}
	}
	status, err := workers[st.Worker].pollTask(ctx, id, map[string]any{
		"protocol": 4, "task": req.Task, "version": req.Version, "scope": scope,
		"inputs": inputs, "settings": req.Settings, "deadline": deadline.Unix(),
	})
	if err != nil {
		return status, err
	}
	switch status.Stage {
	case "waiting_worker":
		now := t.clock()
		if st.Waiting == 0 {
			st.Waiting = now.UnixMilli()
		} else if now.Sub(time.UnixMilli(st.Waiting)) >= t.grace() {
			if holder != "" {
				return TaskStatus{}, taskFailed{"worker_unavailable"}
			}
			st.Worker, st.Waiting = "", 0 // choose again on the next poll
		}
		return status, nil
	case "failed":
		return status, taskFailed{status.Code}
	}
	st.Waiting = 0
	for name, v := range status.Outputs {
		if v.Handle != "" {
			v.Handle = st.Worker + "/" + v.Handle
			status.Outputs[name] = v
		}
	}
	return status, nil
}

// File fetches an output file by its handle. From a worker on another
// machine it is read in the background: polls report "receiving" until the
// bytes are here, so dispatch never waits for a slow transfer.
func (t *Tasks) File(ctx context.Context, handle string, limit int64) ([]byte, string, error) {
	_, workers, err := t.current()
	if err != nil {
		return nil, "", err
	}
	w, path, _ := strings.Cut(handle, "/")
	job, name, ok := strings.Cut(path, "/")
	d := workers[w]
	if d == nil || !ok {
		return nil, "", taskFailed{"worker_unavailable"}
	}
	return d.file(ctx, job, name, limit)
}

// pollTask sends one protocol-4 poll. An unreachable worker is
// "waiting_worker"; a refused request fails as "task_rejected".
func (d *HTTPDownloader) pollTask(ctx context.Context, id string, payload map[string]any) (TaskStatus, error) {
	body, _ := json.Marshal(payload)
	request, err := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(d.Endpoint, "/")+"/jobs/"+id, bytes.NewReader(body))
	if err != nil {
		return TaskStatus{}, errors.New("download_failed")
	}
	request.Header.Set("Authorization", "Bearer "+d.Token)
	request.Header.Set("Content-Type", "application/json")
	response, err := d.client().Do(request)
	if err != nil {
		return TaskStatus{Stage: "waiting_worker"}, nil
	}
	defer response.Body.Close()
	switch response.StatusCode {
	case 200:
	case 503:
		return TaskStatus{Stage: "waiting_worker"}, nil
	case 401:
		return TaskStatus{}, taskFailed{"worker_unauthorized"}
	default:
		return TaskStatus{}, taskFailed{"task_rejected"}
	}
	var status TaskStatus
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&status); err != nil {
		return TaskStatus{Stage: "waiting_worker"}, nil
	}
	if !taskWord.MatchString(status.Stage) || status.Code != "" && !taskWord.MatchString(status.Code) {
		return TaskStatus{}, taskFailed{"task_rejected"}
	}
	if status.Stage == "failed" && status.Code == "" {
		status.Code = "task_failed"
	}
	return status, nil
}

// fileTimeout bounds one background file fetch.
const fileTimeout = 10 * time.Minute

type fileFetch struct {
	done    chan struct{}
	started time.Time
	data    []byte
	stage   string
	err     error
}

func (d *HTTPDownloader) file(ctx context.Context, job, name string, limit int64) ([]byte, string, error) {
	path := "/jobs/" + job + "/files/" + name
	if !d.Remote {
		return d.fetchFile(ctx, path, limit, d.client())
	}
	d.fetchMu.Lock()
	defer d.fetchMu.Unlock()
	for key, f := range d.fetches {
		if time.Since(f.started) > 2*fileTimeout {
			delete(d.fetches, key) // nobody came back for it
		}
	}
	if f := d.fetches[path]; f != nil {
		select {
		case <-f.done:
			delete(d.fetches, path)
			return f.data, f.stage, f.err
		default:
			return nil, "receiving", nil
		}
	}
	if d.fetches == nil {
		d.fetches = map[string]*fileFetch{}
	}
	f := &fileFetch{done: make(chan struct{}), started: time.Now()}
	d.fetches[path] = f
	go func() {
		defer close(f.done)
		fctx, cancel := context.WithTimeout(context.Background(), fileTimeout)
		defer cancel()
		client := *d.client()
		client.Timeout = fileTimeout
		f.data, f.stage, f.err = d.fetchFile(fctx, path, limit, &client)
	}()
	return nil, "receiving", nil
}

func (d *HTTPDownloader) fetchFile(ctx context.Context, path string, limit int64, client *http.Client) ([]byte, string, error) {
	request, err := http.NewRequestWithContext(ctx, "GET", strings.TrimRight(d.Endpoint, "/")+path, nil)
	if err != nil {
		return nil, "", errors.New("download_failed")
	}
	request.Header.Set("Authorization", "Bearer "+d.Token)
	response, err := client.Do(request)
	if err != nil {
		return nil, "waiting_worker", nil
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return nil, "", taskFailed{"task_output_unavailable"}
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return nil, "waiting_worker", nil
	}
	if len(data) == 0 || int64(len(data)) > limit {
		return nil, "", taskFailed{"task_output_too_large"}
	}
	return data, "complete", nil
}
