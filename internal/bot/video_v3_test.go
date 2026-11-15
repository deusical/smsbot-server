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

func TestCombinedVideoStepUpgradeAvoidsIDCollisions(t *testing.T) {
	t.Parallel()
	// Existing step named "video_source" collides with the default upgrade id.
	existing := Pipeline{Steps: []PipelineStep{
		step("link", "text.extract_url", map[string]string{"text": "trigger.args"}, ""),
		{ID: "video", Module: "video.download", Version: 1, Inputs: map[string]string{"url": "link.url"}},
		step("video_source", "text.template", map[string]string{"input": "trigger.args"}, `{"template":"{input}"}`),
		reply(map[string]string{"file": "video.video", "text": "video_source.text"}),
	}}
	if err := validatePipeline(&existing, pipelineEnv{}); err != nil {
		t.Fatalf("existing collision failed: %v", err)
	}
	if existing.Steps[1].ID != "video_source_2" || existing.Steps[2].Inputs["source"] != "video_source_2.source" {
		t.Fatalf("unexpected upgraded steps: %+v", existing.Steps)
	}

	// Two long video.download@1 step ids sharing a 17-character prefix.
	prefix := Pipeline{Steps: []PipelineStep{
		step("link", "text.extract_url", map[string]string{"text": "trigger.args"}, ""),
		{ID: "douyin_video_down_one", Module: "video.download", Version: 1, Inputs: map[string]string{"url": "link.url"}},
		{ID: "douyin_video_down_two", Module: "video.download", Version: 1, Inputs: map[string]string{"url": "link.url"}},
		reply(map[string]string{"file": "douyin_video_down_two.video"}),
	}}
	if err := validatePipeline(&prefix, pipelineEnv{}); err != nil {
		t.Fatalf("prefix collision failed: %v", err)
	}
	if prefix.Steps[1].ID != "douyin_video_down_source" || prefix.Steps[3].ID != "douyin_video_do_source_2" {
		t.Fatalf("unexpected prefix-upgraded ids: %+v", prefix.Steps)
	}
	if prefix.Steps[2].Inputs["source"] != "douyin_video_down_source.source" || prefix.Steps[4].Inputs["source"] != "douyin_video_do_source_2.source" {
		t.Fatalf("unexpected prefix-upgraded bindings: %+v", prefix.Steps)
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
