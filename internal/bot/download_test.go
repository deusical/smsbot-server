package bot

import (
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

type downloadFunc func(context.Context, string, int64) ([]byte, error)

func (f downloadFunc) PollDownload(ctx context.Context, id, u string, limit int64, deadline time.Time) ([]byte, string, error) {
	data, err := f(ctx, u, limit)
	return data, "complete", err
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
			e.Downloader = downloadFunc(func(ctx context.Context, target string, limit int64) ([]byte, error) {
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
			})
			if mode == "worker_missing" {
				e.Downloader = nil
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

func TestDownloadWorkerClient(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"success", "oversize", "bad_status", "bad_type", "redirect"} {
		t.Run(mode, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Header.Get("Authorization") != "Bearer SYNTHETIC_WORKER_TOKEN" {
					t.Error("missing worker auth")
				}
				var input struct {
					URL string `json:"url"`
					Max int64  `json:"max_bytes"`
				}
				if r.Method == "POST" {
					if err := json.NewDecoder(r.Body).Decode(&input); err != nil || input.Max != 100 || input.URL != "https://v.douyin.com/synthetic/" {
						t.Error("wrong worker request")
					}
				}
				if mode == "redirect" {
					w.Header().Set("Location", "/other")
					w.WriteHeader(302)
					return
				}
				if mode == "bad_status" {
					w.WriteHeader(502)
					w.Write([]byte("private upstream error"))
					return
				}
				if r.Method == "POST" {
					json.NewEncoder(w).Encode(map[string]string{"stage": "complete"})
					return
				}
				w.Header().Set("Content-Type", "video/mp4")
				if mode == "bad_type" {
					w.Header().Set("Content-Type", "text/html")
				}
				if mode == "oversize" {
					w.Write(make([]byte, 101))
					return
				}
				w.Write([]byte("synthetic-video"))
			}))
			defer server.Close()
			d := &HTTPDownloader{Endpoint: server.URL, Token: "SYNTHETIC_WORKER_TOKEN"}
			data, _, err := d.PollDownload(context.Background(), strings.Repeat("a", 64), "https://v.douyin.com/synthetic/", 100, time.Now().Add(15*time.Minute))
			if mode == "success" || mode == "slow" {
				if err != nil || string(data) != "synthetic-video" {
					t.Fatal("valid worker output rejected", err)
				}
			} else if err == nil || strings.Contains(err.Error(), "private") {
				t.Fatal("unsafe worker response handling")
			}
			wantCalls := 2
			if mode == "bad_status" || mode == "redirect" {
				wantCalls = 1
			}
			if calls != wantCalls {
				t.Fatal("worker request retried or redirect followed")
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
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer SYNTHETIC" {
					t.Error("missing auth")
				}
				if r.Method == "GET" {
					w.Header().Set("Content-Type", "video/mp4")
					w.Write([]byte("synthetic-video"))
					return
				}
				calls++
				var request struct {
					Deadline int64 `json:"deadline"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
				}
				if request.Deadline < time.Now().Add(19*time.Minute).Unix() {
					t.Error("deadline not snapshotted")
				}
				stage := "downloading"
				if calls > 1 {
					stage = "complete"
				}
				json.NewEncoder(w).Encode(map[string]string{"stage": stage})
			}))
			defer server.Close()
			e.Downloader = &HTTPDownloader{Endpoint: server.URL + "/download", Token: "SYNTHETIC"}
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
			if stateOf(t, e, id) != "queued" || calls != 1 || len(fixture.Sent) != 0 {
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
			if len(fixture.Sent) != 1 || calls != 1 {
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
			before := calls
			if err := e.Dispatch(context.Background()); err != nil {
				t.Fatal(err)
			}
			if calls != before {
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
			e.Downloader = downloadFunc(func(context.Context, string, int64) ([]byte, error) { return []byte("synthetic-video"), nil })
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
