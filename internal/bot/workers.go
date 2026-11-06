package bot

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/deusical/cmdflow"
)

// NamedWorker is one configured video worker.
type NamedWorker struct {
	Name string
	*HTTPDownloader
}

// WorkerPool sends each video to the first worker, in order of preference,
// that answers, and keeps the video's download and compression on that worker,
// where the original is. A worker that stays unreachable for Grace is given up
// and the video starts again on the next one. That is safe because workers only
// prepare files: only the bot sends, after its own checks. The last worker is
// never given up on, so a pool ending with the local worker behaves like it
// when the others are away.
type WorkerPool struct {
	Workers []NamedWorker
	// Grace is how long a worker may be unreachable before the video moves on;
	// 0 means two minutes.
	Grace time.Duration
	// Probe bounds the capabilities request used to choose a worker; 0 means
	// two seconds.
	Probe time.Duration
	now   func() time.Time
}

var workerName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,23}$`)

// tailnet is Tailscale's address range. Tailscale encrypts and authenticates
// the link, so plain HTTP to a worker there never exposes its bearer token.
var tailnet = netip.MustParsePrefix("100.64.0.0/10")

// NewRemoteWorker validates a worker on another machine. Its endpoint must be
// plain HTTP to a Tailscale address or *.ts.net name (or loopback), never a LAN
// address, and its token must be at least 32 characters.
func NewRemoteWorker(name, endpoint, token string) (NamedWorker, error) {
	if !workerName.MatchString(name) || name == "local" {
		return NamedWorker{}, errors.New("worker names are 1-24 lowercase letters, digits or -, and not \"local\"")
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "http" || u.User != nil || u.Hostname() == "" || u.RawQuery != "" || u.Fragment != "" {
		return NamedWorker{}, errors.New("worker " + name + ": endpoint must be http://host:port/download")
	}
	host := u.Hostname()
	addr, ipErr := netip.ParseAddr(host)
	switch {
	case ipErr == nil && (addr.IsLoopback() || tailnet.Contains(addr)):
	case ipErr != nil && net.ParseIP(host) == nil && strings.HasSuffix(strings.ToLower(host), ".ts.net"):
	default:
		return NamedWorker{}, errors.New("worker " + name + ": endpoint must be a Tailscale 100.x address or *.ts.net name")
	}
	if len(strings.TrimSpace(token)) < 32 {
		return NamedWorker{}, errors.New("worker " + name + ": token file unavailable or invalid")
	}
	remote := ipErr != nil || !addr.IsLoopback()
	return NamedWorker{Name: name, HTTPDownloader: &HTTPDownloader{Endpoint: endpoint, Token: strings.TrimSpace(token), Remote: remote}}, nil
}

func (w *WorkerPool) clock() time.Time {
	if w.now != nil {
		return w.now()
	}
	return time.Now()
}

func (w *WorkerPool) grace() time.Duration {
	if w.Grace > 0 {
		return w.Grace
	}
	return 2 * time.Minute
}

func (w *WorkerPool) last() string { return w.Workers[len(w.Workers)-1].Name }

func (w *WorkerPool) named(name string) (*HTTPDownloader, bool) {
	for _, n := range w.Workers {
		if n.Name == name {
			return n.HTTPDownloader, true
		}
	}
	return nil, false
}

// next is the worker after name in order of preference; the last stays last.
func (w *WorkerPool) next(name string) string {
	for i, n := range w.Workers[:len(w.Workers)-1] {
		if n.Name == name {
			return w.Workers[i+1].Name
		}
	}
	return w.last()
}

// choose returns the first worker that answers with protocol 3 within Probe,
// or the last worker when none does.
func (w *WorkerPool) choose(ctx context.Context) string {
	probe := w.Probe
	if probe <= 0 {
		probe = 2 * time.Second
	}
	for _, n := range w.Workers[:len(w.Workers)-1] {
		pctx, cancel := context.WithTimeout(ctx, probe)
		supported, waiting, err := n.protocols(pctx)
		cancel()
		if err == nil && !waiting && supported[3] {
			return n.Name
		}
	}
	return w.last()
}

// wait tracks how long worker has been unreachable and, after Grace, moves it
// to the next worker, reporting true. Any other stage resets the clock.
func (w *WorkerPool) wait(worker *string, since *int64, stage string) bool {
	if stage != "waiting_worker" || *worker == w.last() {
		*since = 0
		return false
	}
	now := w.clock()
	if *since == 0 {
		*since = now.UnixMilli()
		return false
	}
	if now.Sub(time.UnixMilli(*since)) < w.grace() {
		return false
	}
	*worker, *since = w.next(*worker), 0
	return true
}

// PollDownload runs an old combined job on the last worker.
func (w *WorkerPool) PollDownload(ctx context.Context, id, target string, limit int64, deadline time.Time) ([]byte, string, error) {
	return w.Workers[len(w.Workers)-1].PollDownload(ctx, id, target, limit, deadline)
}

// PollDownloadSettings runs a combined job with settings on the last worker.
func (w *WorkerPool) PollDownloadSettings(ctx context.Context, id, target string, limit int64, deadline time.Time, settings VideoSettings) ([]byte, string, error) {
	return w.Workers[len(w.Workers)-1].PollDownloadSettings(ctx, id, target, limit, deadline, settings)
}

// download runs a video.download step on the chosen worker. Its output names
// the worker and the job ("<worker>/<job>") and keeps the link, so the
// compress step can find the original or, if that worker is gone, start over.
func (w *WorkerPool) download(r *runCtx, s *cmdflow.Step, st sourceState) (cmdflow.Outcome, error) {
	e, id := r.e, workerJob(r.j, s.ID)
	combined := false
	complete, err := e.pollWorker(r.ctx, r.j, r.p, func(ctx context.Context, deadline time.Time) (string, error) {
		if _, ok := w.named(st.Worker); !ok {
			st.Worker, st.Waiting = w.choose(ctx), 0
		}
		d, _ := w.named(st.Worker)
		stage, err := d.PollSource(ctx, id, r.j.ID, st.URL, deadline)
		if errors.Is(err, errWorkerV3Unavailable) {
			combined = true
			return "complete", nil
		}
		if err == nil {
			w.wait(&st.Worker, &st.Waiting, stage)
		}
		return stage, err
	})
	if serr := s.Save(st); err == nil {
		err = serr
	}
	if !complete || err != nil {
		return cmdflow.Yield(), err
	}
	if combined {
		// The compress step downloads and compresses in one older job.
		s.Out("source", cmdflow.Value{Type: typeStoredVideo, Text: st.URL})
	} else {
		s.Out("source", cmdflow.Value{Type: typeStoredVideo, Handle: st.Worker + "/" + id, Text: st.URL})
	}
	s.Status = cmdflow.StatusDone
	return yielded(e.finishWorkerStep(r.j, r.p))
}

// compressPoll is one poll of a video.compress step: compression on the worker
// holding the original, or, once that worker is given up, one combined
// download-and-compress job on the next worker, from the saved link.
func (w *WorkerPool) compressPoll(r *runCtx, s *cmdflow.Step, st *compressState, source cmdflow.Value, data *[]byte) func(context.Context, time.Time) (string, error) {
	holder, original, routed := strings.Cut(source.Handle, "/")
	if !routed {
		// A handle from before the pool: the original is on the local worker.
		holder, original = w.last(), source.Handle
	}
	if st.Worker == "" {
		st.Worker = holder
	}
	id := workerJob(r.j, s.ID)
	return func(ctx context.Context, deadline time.Time) (string, error) {
		d, ok := w.named(st.Worker)
		if !ok {
			// The worker was removed from the configuration.
			st.Worker, st.Fallback, st.Waiting = w.last(), source.Text != "", 0
			if d, ok = w.named(st.Worker); !ok || !st.Fallback {
				return "", errors.New("download_failed")
			}
		}
		var stage string
		var err error
		if st.Fallback {
			*data, stage, err = d.PollDownloadSettings(ctx, id, source.Text, st.MaxBytes, deadline, st.Settings)
		} else {
			*data, stage, err = d.PollCompress(ctx, id, r.j.ID, original, st.MaxBytes, deadline, st.Settings)
		}
		if err == nil && source.Text != "" && w.wait(&st.Worker, &st.Waiting, stage) {
			st.Fallback = true
		}
		return stage, err
	}
}
