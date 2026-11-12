package bot

import (
	"context"
	"sync"
	"time"
)

// FixtureDownloader stands in for a video worker in offline fixture mode.
// It never touches the network: each task reports one progress stage, then
// completes. A compress task's file is a tiny synthetic video.
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
		return "working"
	}
	return "complete"
}

// Poll advances one task job. The first poll is still working; the next
// finishes it. The original stays a handle. The send copy is fetched with File.
func (f *FixtureDownloader) Poll(_ context.Context, _ *TaskState, id, _ string, _ time.Time, req TaskRequest) (TaskStatus, error) {
	stage := f.advance(id)
	if stage != "complete" {
		if req.Task == "video.compress" {
			return TaskStatus{Stage: "compressing"}, nil
		}
		return TaskStatus{Stage: "downloading"}, nil
	}
	switch req.Task {
	case "video.download":
		return TaskStatus{Stage: "complete", Outputs: map[string]TaskValue{
			"original": {Type: "video", Handle: id + "/original"},
		}}, nil
	case "video.compress":
		return TaskStatus{Stage: "complete", Outputs: map[string]TaskValue{
			"video": {Type: "video", Handle: id + "/video"},
		}}, nil
	default:
		return TaskStatus{}, taskFailed{"unknown_task"}
	}
}

// File returns the synthetic send copy, or refuses it when it would not fit.
func (f *FixtureDownloader) File(_ context.Context, _ string, limit int64) ([]byte, string, error) {
	if int64(len(fixtureVideo)) > limit {
		return nil, "", taskFailed{"task_output_too_large"}
	}
	return fixtureVideo, "complete", nil
}
