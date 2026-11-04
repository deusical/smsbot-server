package bot

import (
	"context"
	"testing"
	"time"
)

func TestFixtureDownloaderAdvancesPerJobAndRespectsTheLimit(t *testing.T) {
	t.Parallel()
	f := &FixtureDownloader{}
	ctx, deadline := context.Background(), time.Now().Add(time.Minute)
	if stage, err := f.PollSource(ctx, "a", "scope", "https://douyin.com/x", deadline); stage != "downloading" || err != nil {
		t.Fatal(stage, err)
	}
	if stage, _ := f.PollSource(ctx, "a", "scope", "https://douyin.com/x", deadline); stage != "complete" {
		t.Fatal("source did not complete", stage)
	}
	// Each job advances on its own.
	if _, stage, _ := f.PollCompress(ctx, "b", "scope", "a", 1000, deadline, DefaultVideoSettings()); stage != "downloading" {
		t.Fatal(stage)
	}
	data, stage, err := f.PollCompress(ctx, "b", "scope", "a", 1000, deadline, DefaultVideoSettings())
	if stage != "complete" || err != nil || string(data) != string(fixtureVideo) {
		t.Fatal(stage, err)
	}
	f.PollDownload(ctx, "c", "", 1, deadline)
	if _, _, err := f.PollDownload(ctx, "c", "", 1, deadline); err == nil {
		t.Fatal("file over the limit returned")
	}
}

func TestCommandPipelineForEveryTemplate(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		command Command
		modules []string
	}{
		{Command{Name: "hi", Type: "fixed", Instructions: "Hi"}, []string{"text.template", "reply"}},
		{Command{Name: "q", Type: "ai"}, []string{"message.question", "context.none", "ai.chat", "reply"}},
		{Command{Name: "q", Type: "ai", Memory: true}, []string{"message.question", "context.rolling", "ai.chat", "reply"}},
		{Command{Name: "ask", Type: "ai", Memory: true}, []string{"message.question", "context.thread", "ai.chat", "reply"}},
		{Command{Name: "dl", Type: "download"}, []string{"text.extract_url", "video.download", "video.compress", "reply"}},
	} {
		p, err := CommandPipeline(tc.command)
		if err != nil || len(p.Steps) != len(tc.modules) {
			t.Fatal(tc.command, err)
		}
		for i, m := range tc.modules {
			if p.Steps[i].Module != m {
				t.Fatalf("%+v step %d: %s", tc.command, i, p.Steps[i].Module)
			}
		}
	}
	custom := Pipeline{Steps: []PipelineStep{step("t", "text.template", nil, `{"template":"x"}`), reply(map[string]string{"text": "t.text"})}}
	if p, err := CommandPipeline(Command{Name: "c", Kind: "pipeline", Pipeline: &custom}); err != nil || len(p.Steps) != 2 {
		t.Fatal("custom steps not passed through", err)
	}
	for _, bad := range []Command{
		{Name: "x", Type: "shell"},
		{Name: "x", Kind: "pipeline"},
		{Name: "x", Kind: "media.video", Settings: []byte(`{"passes":9}`)},
	} {
		if _, err := CommandPipeline(bad); err == nil {
			t.Fatalf("%+v converted", bad)
		}
	}
}
