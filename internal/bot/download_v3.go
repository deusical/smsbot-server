package bot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
)

// errWorkerV3Unavailable means the worker can't keep an original for a
// separate compress step; the bot then runs download and compression as one
// protocol-2 job with the same settings.
var errWorkerV3Unavailable = errors.New("download_worker_v3_unavailable")

// videoWorkerV3 is a worker that downloads and compresses as separate jobs.
// scope is the bot job: a compress job may only read an original downloaded
// for the same bot job. IDs are the only references that cross the boundary.
type videoWorkerV3 interface {
	PollSource(ctx context.Context, id, scope, target string, deadline time.Time) (stage string, err error)
	PollCompress(ctx context.Context, id, scope, source string, limit int64, deadline time.Time, settings VideoSettings) ([]byte, string, error)
}

func (d *HTTPDownloader) client() *http.Client {
	client := http.Client{}
	if d.Client != nil {
		client = *d.Client
	}
	client.Timeout = 5 * time.Second
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &client
}

// protocols reports the worker protocols it supports. waiting is true when
// the worker can't be reached right now.
func (d *HTTPDownloader) protocols(ctx context.Context) (supported map[int]bool, waiting bool, err error) {
	request, err := http.NewRequestWithContext(ctx, "GET", strings.TrimRight(d.Endpoint, "/")+"/capabilities", nil)
	if err != nil {
		return nil, false, errors.New("download_failed")
	}
	request.Header.Set("Authorization", "Bearer "+d.Token)
	response, err := d.client().Do(request)
	if err != nil {
		return nil, true, nil
	}
	defer response.Body.Close()
	supported = map[int]bool{1: true}
	switch {
	case response.StatusCode == 200:
		var support struct {
			Protocol  int   `json:"protocol"`
			Protocols []int `json:"protocols"`
		}
		if json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&support) == nil {
			supported[support.Protocol] = true
			for _, p := range support.Protocols {
				supported[p] = true
			}
		}
	case response.StatusCode == 503:
		return nil, true, nil
	case response.StatusCode != 404 && response.StatusCode != 410 && response.StatusCode != 426 && response.StatusCode != 501 && response.StatusCode != 502:
		return nil, false, errors.New("download_failed")
	}
	return supported, false, nil
}

func (d *HTTPDownloader) warn(once interface{ Do(func()) }, code string) {
	once.Do(func() {
		if d.Warning != nil {
			d.Warning(code)
		} else {
			log.Print(code)
		}
	})
}

// postJob sends one v3 job request and returns its stage.
func (d *HTTPDownloader) postJob(ctx context.Context, id string, payload map[string]any) (string, error) {
	body, _ := json.Marshal(payload)
	request, err := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(d.Endpoint, "/")+"/jobs/"+id, bytes.NewReader(body))
	if err != nil {
		return "", errors.New("download_failed")
	}
	request.Header.Set("Authorization", "Bearer "+d.Token)
	request.Header.Set("Content-Type", "application/json")
	response, err := d.client().Do(request)
	if err != nil {
		return "waiting_worker", nil
	}
	defer response.Body.Close()
	if response.StatusCode == 503 {
		return "waiting_worker", nil
	}
	if response.StatusCode != 200 {
		return "", errors.New("download_failed")
	}
	var status struct {
		Stage    string `json:"stage"`
		Protocol int    `json:"protocol"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&status); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || ctx.Err() != nil {
			return "waiting_worker", nil
		}
		return "", errors.New("download_failed")
	}
	if status.Protocol != 3 {
		return "", errors.New("download_failed")
	}
	switch status.Stage {
	case "queued", "downloading", "compressing", "verifying", "complete":
		return status.Stage, nil
	}
	return "", errors.New("download_failed")
}

// PollSource downloads the original into the worker, by durable job ID.
func (d *HTTPDownloader) PollSource(ctx context.Context, id, scope, target string, deadline time.Time) (string, error) {
	supported, waiting, err := d.protocols(ctx)
	if err != nil || waiting {
		return "waiting_worker", err
	}
	if !supported[3] {
		d.warn(&d.noV3, "download_worker_v3_unavailable_using_v2")
		return "", errWorkerV3Unavailable
	}
	return d.postJob(ctx, id, map[string]any{"protocol": 3, "op": "download", "scope": scope, "url": target, "deadline": deadline.Unix()})
}

// PollCompress makes the send copy of a source job's original and returns it
// once verified.
func (d *HTTPDownloader) PollCompress(ctx context.Context, id, scope, source string, limit int64, deadline time.Time, settings VideoSettings) ([]byte, string, error) {
	if err := settings.Validate(); err != nil {
		return nil, "", errors.New("download_failed")
	}
	stage, err := d.postJob(ctx, id, map[string]any{"protocol": 3, "op": "compress", "scope": scope, "source": source, "max_bytes": limit, "deadline": deadline.Unix(), "settings": settings})
	if err != nil || stage != "complete" {
		return nil, stage, err
	}
	return d.result(ctx, id, limit)
}

// resultTimeout bounds one background result fetch from a remote worker.
const resultTimeout = 3 * time.Minute

// resultFetch is a remote result being read in the background.
type resultFetch struct {
	done    chan struct{}
	started time.Time
	data    []byte
	stage   string
	err     error
}

// result reads a completed job's verified MP4. From a remote worker it is
// read in the background: this poll and later ones report "receiving" until
// the bytes are here, so dispatch is never held for the transfer.
func (d *HTTPDownloader) result(ctx context.Context, id string, limit int64) ([]byte, string, error) {
	if !d.Remote {
		return d.fetchResult(ctx, id, limit, d.client())
	}
	d.fetchMu.Lock()
	defer d.fetchMu.Unlock()
	for other, f := range d.fetches {
		// Drop results nobody came back for, such as a cancelled job's.
		if time.Since(f.started) > 2*resultTimeout {
			delete(d.fetches, other)
		}
	}
	if f := d.fetches[id]; f != nil {
		select {
		case <-f.done:
			delete(d.fetches, id)
			return f.data, f.stage, f.err
		default:
			return nil, "receiving", nil
		}
	}
	if d.fetches == nil {
		d.fetches = map[string]*resultFetch{}
	}
	f := &resultFetch{done: make(chan struct{}), started: time.Now()}
	d.fetches[id] = f
	go func() {
		defer close(f.done)
		fctx, cancel := context.WithTimeout(context.Background(), resultTimeout)
		defer cancel()
		client := *d.client()
		client.Timeout = resultTimeout
		f.data, f.stage, f.err = d.fetchResult(fctx, id, limit, &client)
	}()
	return nil, "receiving", nil
}

func (d *HTTPDownloader) fetchResult(ctx context.Context, id string, limit int64, client *http.Client) ([]byte, string, error) {
	request, err := http.NewRequestWithContext(ctx, "GET", strings.TrimRight(d.Endpoint, "/")+"/jobs/"+id+"/result", nil)
	if err != nil {
		return nil, "", errors.New("download_failed")
	}
	request.Header.Set("Authorization", "Bearer "+d.Token)
	res, err := client.Do(request)
	if err != nil {
		return nil, "waiting_worker", nil
	}
	defer res.Body.Close()
	if res.StatusCode != 200 || res.Header.Get("Content-Type") != "video/mp4" {
		return nil, "", errors.New("download_failed")
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, limit+1))
	if err != nil {
		return nil, "waiting_worker", nil
	}
	if len(data) == 0 || int64(len(data)) > limit {
		return nil, "", errors.New("download_failed")
	}
	return data, "complete", nil
}
