package bot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/deusical/smsbot-server/internal/media"
	"github.com/deusical/smsbot-server/internal/store"
	"github.com/deusical/smsbot-server/internal/transport"
)

const downloadLimit int64 = 20_000_000 - 1

type DownloadProvider interface {
	PollDownload(context.Context, string, string, int64, time.Time) ([]byte, string, error)
}

// HTTPDownloader talks only to the owner-configured local worker. The worker
// URL is not taken from chat input. Its token remains outside configuration/API.
type HTTPDownloader struct {
	Endpoint, Token string
	Client          *http.Client
}

// PollDownload repeats preparation requests by durable job ID, never sends.
func (d *HTTPDownloader) PollDownload(ctx context.Context, id, target string, limit int64, deadline time.Time) ([]byte, string, error) {
	endpoint := strings.TrimRight(d.Endpoint, "/") + "/jobs/" + id
	body, _ := json.Marshal(map[string]any{"url": target, "max_bytes": limit, "deadline": deadline.Unix()})
	request, err := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, "", errors.New("download_failed")
	}
	request.Header.Set("Authorization", "Bearer "+d.Token)
	request.Header.Set("Content-Type", "application/json")
	client := http.Client{}
	if d.Client != nil {
		client = *d.Client
	}
	client.Timeout = 5 * time.Second
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := client.Do(request)
	if err != nil {
		return nil, "waiting_worker", nil
	}
	defer response.Body.Close()
	if response.StatusCode == 503 {
		return nil, "waiting_worker", nil
	}
	if response.StatusCode != 200 {
		return nil, "", errors.New("download_failed")
	}
	var status struct {
		Stage string `json:"stage"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&status); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || ctx.Err() != nil {
			return nil, "waiting_worker", nil
		}
		return nil, "", errors.New("download_failed")
	}
	switch status.Stage {
	case "queued", "downloading", "compressing", "verifying":
		return nil, status.Stage, nil
	case "complete":
	default:
		return nil, "", errors.New("download_failed")
	}
	result, err := http.NewRequestWithContext(ctx, "GET", endpoint+"/result", nil)
	if err != nil {
		return nil, "", errors.New("download_failed")
	}
	result.Header.Set("Authorization", "Bearer "+d.Token)
	res, err := client.Do(result)
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

var shareURL = regexp.MustCompile(`https?://[^\s<>"'，。；、]+`)

func downloadURL(input string) string {
	found := ""
	for _, match := range shareURL.FindAllString(input, -1) {
		match = strings.TrimRight(match, ").,!?;:）】]}")
		u, err := url.Parse(match)
		if err != nil || u.User != nil || u.Port() != "" || u.Scheme != "https" {
			continue
		}
		host := strings.ToLower(u.Hostname())
		if host != "douyin.com" && !strings.HasSuffix(host, ".douyin.com") && host != "xiaohongshu.com" && !strings.HasSuffix(host, ".xiaohongshu.com") && host != "xhslink.com" && !strings.HasSuffix(host, ".xhslink.com") {
			continue
		}
		if found != "" && found != match {
			return ""
		}
		found = match
	}
	return found
}

// Caller holds the control lock. Release it during local download/encoding so
// pause and enrollment changes can cancel work. Never send a result that became
// stale or lost consent while it was being prepared.
func (e *Engine) prepareDownload(ctx context.Context, j store.Job, p *store.Plan) (bool, error) {
	if p.DownloadURL == "" || len(p.Attachments) > 0 {
		return false, nil
	}
	if e.Downloader == nil {
		return true, e.Store.Complete(j.ID, "failed", "download_worker_unavailable")
	}
	settings, err := e.Collection.Settings()
	if err != nil {
		return true, err
	}
	limits := settings.FileLimits()
	limit := downloadLimit
	if limits.MaxFileMiB > 0 {
		limit = min(limit, limits.FileBytes())
	}
	if limits.MaxTotalMiB > 0 {
		limit = min(limit, limits.MaxTotalMiB<<20)
	}
	if p.DownloadMaxBytes > limit {
		return true, e.Store.Complete(j.ID, "cancelled", "attachment_limits_changed")
	}
	if p.DownloadMaxBytes == 0 {
		p.DownloadMaxBytes = limit
		if err := e.Store.SavePlan(j.ID, *p); err != nil {
			return true, err
		}
	}
	limit = p.DownloadMaxBytes
	ok, err := e.Store.Transition(j.ID, "queued", "generating")
	if err != nil || !ok {
		return true, err
	}
	deadline, parseerr := time.Parse(time.RFC3339Nano, p.ExecutionDeadline)
	if parseerr != nil {
		created, _ := time.Parse(time.RFC3339Nano, j.CreatedAt)
		deadline = created.Add(95 * time.Second)
	}
	workctx, cancel := context.WithTimeout(ctx, max(time.Duration(0), deadline.Sub(e.now())))
	e.pending[j.ID] = cancel
	e.mu.Unlock()
	requestctx, requestcancel := context.WithTimeout(workctx, 10*time.Second)
	data, stage, workerr := e.Downloader.PollDownload(requestctx, j.ID, p.DownloadURL, limit, deadline)
	requestcancel()
	cancel()
	e.mu.Lock()
	delete(e.pending, j.ID)
	current, _, loaderr := e.Store.JobPlan(j.ID)
	if current.State != "" && current.State != "generating" {
		return true, nil
	}
	if loaderr != nil {
		return true, loaderr
	}
	if workerr == nil && stage != "complete" {
		if err := e.Store.DeferWork(j.ID, stage, time.Now().Add(time.Second).UnixMilli()); err != nil {
			return true, err
		}
		_, err := e.Store.Transition(j.ID, "generating", "queued")
		return true, err
	}
	if workerr != nil || len(data) == 0 || int64(len(data)) > limit {
		return true, e.Store.Complete(j.ID, "failed", "download_failed")
	}
	p.Attachments, err = media.PrepareWithLimits([]media.Upload{{Name: "video.mp4", MIME: "video/mp4", Data: data}}, limits)
	if err != nil {
		return true, e.Store.Complete(j.ID, "failed", "download_attachment_invalid")
	}
	_, code, err := e.check(ctx, j, *p)
	if err != nil {
		return true, err
	}
	if code != "" {
		return true, e.Store.Complete(j.ID, "cancelled", code)
	}
	if err = e.Store.DeferWork(j.ID, "complete", 0); err != nil {
		return true, err
	}
	if err = e.Store.SavePlan(j.ID, *p); err != nil {
		return true, err
	}
	_, err = e.Store.Transition(j.ID, "generating", "queued")
	return err != nil, err
}

// Download uploads run in a bounded background lane. Only the dispatch loop
// commits their result and returns a job to the send queue.
type downloadUploadResult struct {
	files []media.File
	err   error
}
type downloadUpload struct {
	result <-chan downloadUploadResult
	cancel context.CancelFunc
}

func (e *Engine) prepareDownloadUpload(ctx context.Context, j store.Job, p *store.Plan) (bool, error) {
	if p.DownloadUploaded {
		return false, nil
	}
	if len(e.downloadUploads) > 0 {
		return true, e.Store.DeferWork(j.ID, "waiting_upload", time.Now().Add(time.Second).UnixMilli())
	}
	preparer, ok := e.Transport.(transport.FilePreparer)
	if !ok {
		return true, e.Store.Complete(j.ID, "failed", "attachments_unavailable")
	}
	deadline, err := time.Parse(time.RFC3339Nano, p.ExecutionDeadline)
	if err != nil {
		return true, e.Store.Complete(j.ID, "failed", "download_deadline_invalid")
	}
	if err = e.Store.DeferWork(j.ID, "uploading", 0); err != nil {
		return true, err
	}
	ok, err = e.Store.Transition(j.ID, "queued", "generating")
	if err != nil || !ok {
		return true, err
	}
	uploadctx, cancel := context.WithTimeout(ctx, max(time.Duration(0), deadline.Sub(e.now())))
	result := make(chan downloadUploadResult, 1)
	e.pending[j.ID] = cancel
	e.downloadUploads[j.ID] = downloadUpload{result: result, cancel: cancel}
	files := p.Attachments
	e.downloadWG.Add(1)
	go func() {
		defer e.downloadWG.Done()
		defer cancel()
		prepared, err := preparer.PrepareFiles(uploadctx, files)
		result <- downloadUploadResult{files: prepared, err: err}
	}()
	return true, nil
}

// Caller holds the control lock. A cancelled job's result is discarded; no
// background goroutine can submit a message or change an immutable plan.
func (e *Engine) collectDownloadUploads(ctx context.Context) error {
	for id, upload := range e.downloadUploads {
		select {
		case result := <-upload.result:
			upload.cancel()
			delete(e.pending, id)
			delete(e.downloadUploads, id)
			j, p, err := e.Store.JobPlan(id)
			if j.State != "" && j.State != "generating" {
				continue
			}
			if err != nil {
				return err
			}
			if result.err != nil {
				if err = e.Store.Complete(id, "failed", "attachment_upload_failed"); err != nil {
					return err
				}
				continue
			}
			_, code, err := e.check(ctx, j, p)
			if err != nil {
				return err
			}
			if code != "" {
				state := "cancelled"
				if code == "execution_expired" {
					state = "expired"
				}
				if err = e.Store.Complete(id, state, code); err != nil {
					return err
				}
				continue
			}
			p.Attachments = result.files
			p.DownloadUploaded = true
			if err = e.Store.SavePlan(id, p); err != nil {
				return err
			}
			if err = e.Store.DeferWork(id, "ready", 0); err != nil {
				return err
			}
			if _, err = e.Store.Transition(id, "generating", "queued"); err != nil {
				return err
			}
		default:
		}
	}
	return nil
}
