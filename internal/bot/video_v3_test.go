package bot

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/deusical/smsbot-server/internal/store"
)

// A saved pipeline that pinned the combined video.download@1 keeps working.
func TestCombinedVideoStepUpgrades(t *testing.T) {
	t.Parallel()
	p := Pipeline{Steps: []PipelineStep{
		step("link", "text.extract_url", map[string]string{"text": "trigger.args"}, ""),
		{ID: "video", Module: "video.download", Version: 1, Inputs: map[string]string{"url": "link.url"}, Settings: json.RawMessage(`{"preset":"fast"}`)},
		reply(map[string]string{"file": "video.video"}),
	}}
	if err := validatePipeline(&p, pipelineEnv{}); err != nil {
		t.Fatal(err)
	}
	// Long ids are shortened at the end, keeping a valid id; an unpinned step
	// with compression settings or a ".video" reader is the old kind too.
	for _, id := range []string{"douyin_video_downloader", "x"} {
		for _, version := range []int{1, 0} {
			long := Pipeline{Steps: []PipelineStep{
				step("link", "text.extract_url", map[string]string{"text": "trigger.args"}, ""),
				{ID: id, Module: "video.download", Version: version, Inputs: map[string]string{"url": "link.url"}},
				reply(map[string]string{"file": id + ".video"}),
			}}
			if err := validatePipeline(&long, pipelineEnv{}); err != nil || long.Steps[2].Module != "video.compress" {
				t.Fatal(id, version, err)
			}
		}
	}
	// A new-style step with formatted empty settings is left alone.
	current := Pipeline{Steps: []PipelineStep{
		step("link", "text.extract_url", map[string]string{"text": "trigger.args"}, ""),
		step("source", "video.download", map[string]string{"url": "link.url"}, "{  }"),
		step("video", "video.compress", map[string]string{"source": "source.source"}, ""),
		reply(map[string]string{"file": "video.video"}),
	}}
	if err := validatePipeline(&current, pipelineEnv{}); err != nil || len(current.Steps) != 4 || current.Steps[1].Version != 2 {
		t.Fatal("new-style step rewritten", err)
	}
	if len(p.Steps) != 4 || p.Steps[1].Module != "video.download" || p.Steps[1].Version != 2 || p.Steps[2].ID != "video" || p.Steps[2].Module != "video.compress" || !strings.Contains(string(p.Steps[2].Settings), `"fast"`) {
		t.Fatalf("not upgraded: %+v", p.Steps)
	}
}

// An AI answer with no link ends the command with how to use it, without
// asking the worker.
func TestMissingLinkAtDispatchRepliesWithoutDownloading(t *testing.T) {
	t.Parallel()
	e, fixture := setup(t)
	arm(t, e)
	c := configureAI(t, e, false, false)
	c.Commands = append(c.Commands, customCommand("find", Pipeline{Steps: []PipelineStep{
		step("answer", "ai.chat", map[string]string{"prompt": "trigger.args"}, ""),
		step("link", "text.extract_url", map[string]string{"text": "answer.text"}, ""),
		step("source", "video.download", map[string]string{"url": "link.url"}, ""),
		step("video", "video.compress", map[string]string{"source": "source.source"}, ""),
		reply(map[string]string{"file": "video.video"}),
	}}))
	if err := e.SetConfiguration(c); err != nil {
		t.Fatal(err)
	}
	e.Provider = &transcriptProvider{}
	e.Tasks = &scriptTasks{fn: func(context.Context, string, int64) ([]byte, error) {
		t.Error("worker asked without a link")
		return nil, nil
	}}
	m := incoming()
	m.Text = "!find nothing to see"
	if ok, err := e.Accept(context.Background(), m); !ok || err != nil {
		t.Fatal(ok, err)
	}
	if err := e.Dispatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	id := store.Hash(m.ConversationID + "\x00" + m.ID)
	if got := stateOf(t, e, id); got != "submitted" {
		t.Fatal("state", got)
	}
	for _, text := range fixture.Sent {
		if text != noVideoLink {
			t.Fatalf("sent %q", text)
		}
	}
}
