package bot

import (
	"context"
	"testing"
	"time"

	"github.com/deusical/smsbot-server/internal/media"
	"github.com/deusical/smsbot-server/internal/store"
	"github.com/deusical/smsbot-server/internal/transport"
)

// scriptTasks is an in-process video worker. Download finishes on the first
// poll. Compress calls fn for the bytes, unless hold is set: the first
// compress poll then returns that stage and the next one calls fn.
type scriptTasks struct {
	fn     func(context.Context, string, int64) ([]byte, error)
	hold   string
	onPoll func(time.Time)
	url    string
	data   []byte
	polls  int
	held   bool
}

func (s *scriptTasks) Poll(ctx context.Context, _ *TaskState, id, _ string, deadline time.Time, req TaskRequest) (TaskStatus, error) {
	if s.onPoll != nil {
		s.onPoll(deadline)
	}
	switch req.Task {
	case "video.download":
		if v, ok := req.Inputs["url"]; ok {
			s.url = v.Text
		}
		return TaskStatus{Stage: "complete", Outputs: map[string]TaskValue{"original": {Handle: id + "/original"}}}, nil
	case "video.compress":
		s.polls++
		if s.hold != "" && !s.held {
			s.held = true
			return TaskStatus{Stage: s.hold}, nil
		}
		settings, _ := req.Settings.(map[string]any)
		limit := settingInt(settings["max_bytes"])
		data, err := s.fn(ctx, s.url, limit)
		if err != nil {
			return TaskStatus{}, err
		}
		s.data = data
		return TaskStatus{Stage: "complete", Outputs: map[string]TaskValue{"video": {Handle: id + "/video"}}}, nil
	default:
		return TaskStatus{}, taskFailed{"unknown_task"}
	}
}

func (s *scriptTasks) File(context.Context, string, int64) ([]byte, string, error) {
	if len(s.data) == 0 {
		return nil, "", taskFailed{"task_output_unavailable"}
	}
	return s.data, "complete", nil
}

func settingInt(v any) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case int:
		return int64(n)
	case float64:
		return int64(n)
	default:
		return 0
	}
}

func TestExtractDownloadURL(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ text, want string }{
		{"Copied share text https://v.douyin.com/Synthetic/， open it", "https://v.douyin.com/Synthetic/"},
		{"(https://www.xiaohongshu.com/explore/synthetic?xsec_token=synthetic)", "https://www.xiaohongshu.com/explore/synthetic?xsec_token=synthetic"},
		{"https://xhslink.com/synthetic", "https://xhslink.com/synthetic"},
		{"https://douyin.com.attacker.test/video/1", ""},
		{"https://user:password@douyin.com/video/1", ""},
		{"http://v.douyin.com/synthetic/", ""},
		{"https://douyin.com:123/video/1", ""},
		{"https://douyin.com/video/1 https://douyin.com/video/2", ""},
		{"ordinary text", ""},
	} {
		if got := downloadURL(tc.text); got != tc.want {
			t.Fatalf("URL extraction: got %q want %q", got, tc.want)
		}
	}
}

func TestDownloadCommandSafety(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"success", "slow", "pause", "unenroll", "expired", "unknown_send", "oversize", "worker_missing"} {
		t.Run(mode, func(t *testing.T) {
			e, fixture := setup(t)
			arm(t, e)
			cfg := DefaultConfiguration()
			cfg.Commands = []Command{{Name: "download", Type: "download", Enabled: true}}
			if err := e.SetConfiguration(cfg); err != nil {
				t.Fatal(err)
			}
			calls := 0
			if mode != "worker_missing" {
				e.Tasks = &scriptTasks{fn: func(ctx context.Context, target string, limit int64) ([]byte, error) {
					calls++
					if target != "https://v.douyin.com/synthetic/" || limit > downloadLimit {
						t.Fatal("wrong worker input")
					}
					switch mode {
					case "pause":
						if err := e.Pause(true); err != nil {
							t.Fatal(err)
						}
						if ctx.Err() == nil {
							t.Fatal("pause did not cancel work")
						}
					case "unenroll":
						if err := e.Unenroll("demo-chat"); err != nil {
							t.Fatal(err)
						}
					case "slow":
						e.now = func() time.Time { return time.Now().Add(3 * time.Minute) }
					case "expired":
						e.now = func() time.Time { return time.Now().Add(16 * time.Minute) }
					case "oversize":
						return make([]byte, limit+1), nil
					}
					return []byte("synthetic-video"), nil
				}}
			}
			if mode == "unknown_send" {
				fixture.Fail = "unknown"
			}
			m := incoming()
			m.Text = "!download share text https://v.douyin.com/synthetic/"
			if ok, err := e.Accept(context.Background(), m); err != nil || !ok {
				t.Fatal("command not accepted", err)
			}
			id := store.Hash(m.ConversationID + "\x00" + m.ID)
			if err := e.Dispatch(context.Background()); err != nil {
				t.Fatal(err)
			}
			finishDownloadUploads(t, e)
			want := "submitted"
			switch mode {
			case "pause", "unenroll", "expired":
				want = "cancelled"
			case "unknown_send":
				want = "unknown_submission"
			case "oversize", "worker_missing":
				want = "failed"
			}
			if got := stateOf(t, e, id); got != want {
				t.Fatalf("state %s want %s", got, want)
			}
			if mode == "success" || mode == "slow" {
				if len(fixture.Sent) != 1 {
					t.Fatal("no attachment send")
				}
			} else if len(fixture.Sent) != 0 {
				t.Fatal("unsafe result sent")
			}
			if err := e.Dispatch(context.Background()); err != nil {
				t.Fatal(err)
			}
			if calls > 1 {
				t.Fatal("terminal download repeated")
			}
		})
	}
}

// Polls must yield the dispatcher, snapshot the lifetime, and leave uncertain
// submission terminal even when another preparation poll would be safe.
func TestDurableDownloadPolling(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"success", "pause", "unenroll", "unknown", "deadline"} {
		t.Run(mode, func(t *testing.T) {
			e, fixture := setup(t)
			arm(t, e)
			cfg := DefaultConfiguration()
			cfg.Commands = []Command{{Name: "download", Type: "download", Enabled: true, ExecutionMinutes: 20}}
			if err := e.SetConfiguration(cfg); err != nil {
				t.Fatal(err)
			}
			tasks := &scriptTasks{
				hold: "downloading",
				onPoll: func(deadline time.Time) {
					if deadline.Before(time.Now().Add(19 * time.Minute)) {
						t.Error("deadline not snapshotted")
					}
				},
				fn: func(context.Context, string, int64) ([]byte, error) {
					return []byte("synthetic-video"), nil
				},
			}
			e.Tasks = tasks
			m := incoming()
			m.Text = "!download https://v.douyin.com/synthetic/"
			if ok, err := e.Accept(context.Background(), m); !ok || err != nil {
				t.Fatal("accept", err)
			}
			id := store.Hash(m.ConversationID + "\x00" + m.ID)
			_, planned, err := e.Store.JobPlan(id)
			if err != nil {
				t.Fatal(err)
			}
			cfg.Commands[0].ExecutionMinutes = 1
			if err := e.SetConfiguration(cfg); err != nil {
				t.Fatal(err)
			}
			if err := e.Dispatch(context.Background()); err != nil {
				t.Fatal(err)
			}
			if stateOf(t, e, id) != "queued" || tasks.polls != 1 || len(fixture.Sent) != 0 {
				t.Fatal("poll did not yield")
			}
			ping := incoming()
			ping.ID = "other-command"
			if ok, err := e.Accept(context.Background(), ping); !ok || err != nil {
				t.Fatal("ping", err)
			}
			if err := e.Dispatch(context.Background()); err != nil {
				t.Fatal(err)
			}
			if len(fixture.Sent) != 1 || tasks.polls != 1 {
				t.Fatal("download blocked another command")
			}
			_, after, err := e.Store.JobPlan(id)
			if err != nil || after.ExecutionDeadline != planned.ExecutionDeadline {
				t.Fatal("mutable deadline")
			}
			if mode == "pause" {
				if err := e.Pause(true); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "unenroll" {
				if err := e.Unenroll("demo-chat"); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "unknown" {
				fixture.Fail = "unknown"
			}
			if mode == "deadline" {
				e.now = func() time.Time { return time.Now().Add(21 * time.Minute) }
			}
			if _, err := e.Store.DB.Exec("UPDATE job_work SET next_poll=0"); err != nil {
				t.Fatal(err)
			}
			if err := e.Dispatch(context.Background()); err != nil {
				t.Fatal(err)
			}
			finishDownloadUploads(t, e)
			want := "submitted"
			switch mode {
			case "pause", "unenroll":
				want = "cancelled"
			case "unknown":
				want = "unknown_submission"
			case "deadline":
				want = "expired"
			}
			if got := stateOf(t, e, id); got != want {
				t.Fatalf("state %s want %s", got, want)
			}
			before := tasks.polls
			if err := e.Dispatch(context.Background()); err != nil {
				t.Fatal(err)
			}
			if tasks.polls != before {
				t.Fatal("terminal job polled again")
			}
		})
	}
}

func TestDownloadAdmissionAndLifetimeValidation(t *testing.T) {
	t.Parallel()
	e, _ := setup(t)
	arm(t, e)
	cfg := DefaultConfiguration()
	cfg.Commands = []Command{{Name: "download", Type: "download", Enabled: true}}
	for _, minutes := range []int{-1, 61} {
		cfg.Commands[0].ExecutionMinutes = minutes
		if err := cfg.Validate(); err == nil {
			t.Fatal("invalid lifetime accepted")
		}
	}
	cfg.Commands[0].ExecutionMinutes = 0
	if err := e.SetConfiguration(cfg); err != nil {
		t.Fatal(err)
	}
	m := incoming()
	m.Text = "!download https://xhslink.com/synthetic"
	m.At = time.Now().Add(-3 * time.Minute)
	if ok, err := e.Accept(context.Background(), m); ok || err != nil {
		t.Fatal("stale command accepted", err)
	}
	m.At = time.Now()
	if ok, err := e.Accept(context.Background(), m); !ok || err != nil {
		t.Fatal("fresh command refused", err)
	}
	_, p, err := e.Store.JobPlan(store.Hash(m.ConversationID + "\x00" + m.ID))
	if err != nil {
		t.Fatal(err)
	}
	deadline, err := time.Parse(time.RFC3339Nano, p.ExecutionDeadline)
	if err != nil || time.Until(deadline) < 14*time.Minute || time.Until(deadline) > 15*time.Minute {
		t.Fatal("incorrect default deadline")
	}
}

func finishDownloadUploads(t *testing.T, e *Engine) {
	t.Helper()
	until := time.Now().Add(3 * time.Second)
	for len(e.downloadUploads) > 0 && time.Now().Before(until) {
		time.Sleep(time.Millisecond)
		if err := e.Dispatch(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if len(e.downloadUploads) > 0 {
		t.Fatal("upload did not finish")
	}
}

type downloadUploadTransport struct {
	*transport.Fixture
	upload func(context.Context, []media.File) ([]media.File, error)
}

func (f downloadUploadTransport) PrepareFiles(ctx context.Context, files []media.File) ([]media.File, error) {
	return f.upload(ctx, files)
}

func TestDownloadUploadYieldsAndChecksConsentAgain(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"success", "pause", "expired"} {
		t.Run(mode, func(t *testing.T) {
			e, fixture := setup(t)
			arm(t, e)
			cfg := DefaultConfiguration()
			cfg.Commands = []Command{{Name: "download", Type: "download", Enabled: true}}
			if err := e.SetConfiguration(cfg); err != nil {
				t.Fatal(err)
			}
			e.Tasks = &scriptTasks{fn: func(context.Context, string, int64) ([]byte, error) { return []byte("synthetic-video"), nil }}
			entered, release := make(chan struct{}), make(chan struct{})
			e.Transport = downloadUploadTransport{Fixture: fixture, upload: func(ctx context.Context, files []media.File) ([]media.File, error) {
				close(entered)
				select {
				case <-release:
					return files, nil
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}}
			t.Cleanup(func() { _ = e.Pause(true); e.downloadWG.Wait() })
			m := incoming()
			m.Text = "!download https://xhslink.com/synthetic"
			if ok, err := e.Accept(context.Background(), m); !ok || err != nil {
				t.Fatal("accept", err)
			}
			id := store.Hash(m.ConversationID + "\x00" + m.ID)
			if err := e.Dispatch(context.Background()); err != nil {
				t.Fatal(err)
			}
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("upload not started")
			}
			ping := incoming()
			ping.ID = "during-upload"
			if ok, err := e.Accept(context.Background(), ping); !ok || err != nil {
				t.Fatal("ping", err)
			}
			if err := e.Dispatch(context.Background()); err != nil {
				t.Fatal(err)
			}
			if len(fixture.Sent) != 1 {
				t.Fatal("upload blocked another send")
			}
			if mode == "pause" {
				if err := e.Pause(true); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "expired" {
				e.now = func() time.Time { return time.Now().Add(16 * time.Minute) }
			}
			close(release)
			finishDownloadUploads(t, e)
			want := "submitted"
			if mode == "pause" {
				want = "cancelled"
			}
			if mode == "expired" {
				want = "expired"
			}
			if got := stateOf(t, e, id); got != want {
				t.Fatalf("state %s want %s", got, want)
			}
			if mode != "success" && len(fixture.Sent) != 1 {
				t.Fatal("invalid upload result sent")
			}
		})
	}
}
