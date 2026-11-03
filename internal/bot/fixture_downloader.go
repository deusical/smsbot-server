package bot

import (
	"context"
	"errors"
	"sync"
	"time"
)

// FixtureDownloader stands in for the local video worker in offline fixture
// mode. It never touches the network: each job reports one "downloading"
// stage, then completes with a tiny synthetic video.
type FixtureDownloader struct {
	mu   sync.Mutex
	seen map[string]bool
}

var fixtureVideo = []byte("synthetic-fixture-video")

func (f *FixtureDownloader) advance(id string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.seen == nil {
		f.seen = map[string]bool{}
	}
	if !f.seen[id] {
		f.seen[id] = true
		return "downloading"
	}
	return "complete"
}

func (f *FixtureDownloader) PollDownload(_ context.Context, id, _ string, limit int64, _ time.Time) ([]byte, string, error) {
	if stage := f.advance(id); stage != "complete" {
		return nil, stage, nil
	}
	if int64(len(fixtureVideo)) > limit {
		return nil, "", errors.New("download_failed")
	}
	return fixtureVideo, "complete", nil
}

func (f *FixtureDownloader) PollSource(_ context.Context, id, _, _ string, _ time.Time) (string, error) {
	return f.advance(id), nil
}

func (f *FixtureDownloader) PollCompress(ctx context.Context, id, _, _ string, limit int64, deadline time.Time, _ VideoSettings) ([]byte, string, error) {
	return f.PollDownload(ctx, id, "", limit, deadline)
}
