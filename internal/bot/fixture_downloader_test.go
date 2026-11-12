package bot

import (
	"context"
	"testing"
	"time"
)

func TestFixtureDownloaderAdvancesPerJobAndRespectsTheLimit(t *testing.T) {
	t.Parallel()
	f := &FixtureDownloader{}
	ctx := context.Background()
	deadline := time.Now().Add(time.Minute)
	download := TaskRequest{Task: "video.download", Version: 1}
	if status, err := f.Poll(ctx, &TaskState{}, "a", "scope", deadline, download); status.Stage != "downloading" || err != nil {
		t.Fatal(status, err)
	}
	status, err := f.Poll(ctx, &TaskState{}, "a", "scope", deadline, download)
	if err != nil || status.Stage != "complete" || status.Outputs["original"].Handle != "a/original" {
		t.Fatal(status, err)
	}
	compress := TaskRequest{Task: "video.compress", Version: 1}
	if status, err := f.Poll(ctx, &TaskState{}, "b", "scope", deadline, compress); status.Stage != "compressing" || err != nil {
		t.Fatal(status, err)
	}
	status, err = f.Poll(ctx, &TaskState{}, "b", "scope", deadline, compress)
	if err != nil || status.Stage != "complete" || status.Outputs["video"].Handle != "b/video" {
		t.Fatal(status, err)
	}
	data, stage, err := f.File(ctx, "b/video", 1000)
	if err != nil || stage != "complete" || string(data) != string(fixtureVideo) {
		t.Fatal(stage, err)
	}
	if _, _, err := f.File(ctx, "b/video", 1); err == nil {
		t.Fatal("over-limit file accepted")
	}
}
