package bot

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/deusical/smsbot-server/internal/media"
	"github.com/deusical/smsbot-server/internal/store"
	"github.com/deusical/smsbot-server/internal/transport"
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

type recordingVideoTransport struct {
	*transport.Fixture
	prepared [][]media.File
	sent     [][]media.File
}

func (r *recordingVideoTransport) PrepareFiles(_ context.Context, files []media.File) ([]media.File, error) {
	cp := append([]media.File(nil), files...)
	r.prepared = append(r.prepared, cp)
	out := make([]media.File, len(files))
	for i, f := range files {
		out[i] = f
		out[i].RemoteID = "remote-" + f.ID
	}
	return out, nil
}

func (r *recordingVideoTransport) SendFiles(ctx context.Context, conv, txn, text string, files []media.File) (transport.Receipt, error) {
	r.sent = append(r.sent, append([]media.File(nil), files...))
	return r.Fixture.SendFiles(ctx, conv, txn, text, files)
}

type hookProvider struct {
	onGenerate func()
}

func (h *hookProvider) Models(context.Context) ([]Model, error) {
	return SimulatedProvider{}.Models(context.Background())
}

func (h *hookProvider) Generate(_ context.Context, _ string, plan store.Plan, _ []store.Turn, _ Model, _ *Attachment) (Result, error) {
	if h.onGenerate != nil {
		h.onGenerate()
	}
	zero := 0.0
	return Result{Text: "Caption: " + plan.Input, Cost: &zero}, nil
}

// Compressed video bytes are staged in job_files out of jobs.plan as soon as
// video.compress finishes, reach outbound attachments intact when reply runs,
// and are removed from job_files when the plan is cleared.
func TestCompressedVideoIsStagedOutOfRunDeliveredAndCleared(t *testing.T) {
	t.Parallel()
	e, fixture := setup(t)
	rec := &recordingVideoTransport{Fixture: fixture}
	e.Transport = rec
	arm(t, e)
	c := configureAI(t, e, false, false)
	c.Commands = append(c.Commands, customCommand("grab", Pipeline{ExecutionMinutes: 5, Steps: []PipelineStep{
		step("link", "text.extract_url", map[string]string{"text": "trigger.args"}, ""),
		step("source", "video.download", map[string]string{"url": "link.url"}, ""),
		step("video", "video.compress", map[string]string{"source": "source.source"}, `{"preset":"fast"}`),
		step("caption", "ai.chat", map[string]string{"prompt": "trigger.args"}, ""),
		reply(map[string]string{"text": "caption.text", "file": "video.video"}),
	}}))
	if err := e.SetConfiguration(c); err != nil {
		t.Fatal(err)
	}

	videoBytes := bytes.Repeat([]byte{0x42}, 256<<10)
	copy(videoBytes, "synthetic-compressed-mp4-marker")
	e.Tasks = &scriptTasks{fn: func(context.Context, string, int64) ([]byte, error) {
		return append([]byte(nil), videoBytes...), nil
	}}
	// Turn on approval while the AI caption generates (after video.compress
	// has already finished and saved), so Dispatch pauses before reply runs.
	e.Provider = &hookProvider{onGenerate: func() {
		cfg, err := e.Configuration()
		if err != nil {
			t.Error(err)
			return
		}
		for i := range cfg.Commands {
			if cfg.Commands[i].Name == "grab" {
				cfg.Commands[i].Approval = true
			}
		}
		if err := e.SetConfiguration(cfg); err != nil {
			t.Error(err)
		}
	}}

	m := incoming()
	m.ID = "grab-send"
	m.Text = "!grab https://v.douyin.com/synthetic/"
	if ok, err := e.Accept(context.Background(), m); !ok || err != nil {
		t.Fatal(ok, err)
	}
	id := store.Hash(m.ConversationID + "\x00" + m.ID)
	if err := e.Dispatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := stateOf(t, e, id); got != "awaiting_approval" {
		t.Fatalf("expected awaiting_approval before reply, got %s", got)
	}

	var enc string
	var fileRows int
	if err := e.Store.DB.QueryRow("SELECT plan FROM jobs WHERE id=?", id).Scan(&enc); err != nil {
		t.Fatal(err)
	}
	if err := e.Store.DB.QueryRow("SELECT count(*) FROM job_files WHERE job=?", id).Scan(&fileRows); err != nil {
		t.Fatal(err)
	}
	if fileRows != 1 {
		t.Fatalf("expected 1 staged job_files row, got %d", fileRows)
	}
	if len(enc) > 8192 {
		t.Fatalf("saved plan still carries video bytes (%d bytes)", len(enc))
	}
	rawPlan, err := e.Store.Vault.Open("plan:"+id, enc)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(rawPlan, []byte("synthetic-compressed-mp4-marker")) {
		t.Fatal("compressed video marker leaked into saved plan JSON")
	}
	_, p, err := e.Store.JobPlan(id)
	if err != nil {
		t.Fatal(err)
	}
	out := videoStep(t, p).Outputs["video"]
	if out.Handle == "" || len(out.Data) != 0 {
		t.Fatalf("video output should carry only a handle: %+v", out)
	}
	if len(p.Attachments) != 0 || len(p.Staged) != 1 || !bytes.Equal(p.Staged[0].File.Data, videoBytes) {
		t.Fatalf("unexpected staged/attachment state before reply: attachments=%d staged=%d", len(p.Attachments), len(p.Staged))
	}

	// Approve and complete upload + submission; verify outbound attachment is intact and job_files is cleared.
	if err := e.Approve(id); err != nil {
		t.Fatal(err)
	}
	if err := e.Dispatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	finishDownloadUploads(t, e)
	if err := e.Dispatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := stateOf(t, e, id); got != "submitted" {
		t.Fatalf("expected submitted, got %s", got)
	}
	if len(rec.prepared) != 1 || len(rec.prepared[0]) != 1 || !bytes.Equal(rec.prepared[0][0].Data, videoBytes) {
		t.Fatal("upload did not receive intact staged video bytes")
	}
	if len(rec.sent) != 1 || len(rec.sent[0]) != 1 || !bytes.Equal(rec.sent[0][0].Data, videoBytes) {
		t.Fatal("outbound send did not receive intact staged video bytes")
	}
	if err := e.Store.DB.QueryRow("SELECT count(*) FROM job_files WHERE job=?", id).Scan(&fileRows); err != nil || fileRows != 0 {
		t.Fatalf("job_files rows remaining after send: %d (%v)", fileRows, err)
	}

	// Also verify cancelling a job with a staged video removes its job_files row.
	cfg, _ := e.Configuration()
	for i := range cfg.Commands {
		if cfg.Commands[i].Name == "grab" {
			cfg.Commands[i].Approval = false
		}
	}
	if err := e.SetConfiguration(cfg); err != nil {
		t.Fatal(err)
	}
	m2 := incoming()
	m2.ID = "grab-cancel"
	m2.Text = "!grab https://v.douyin.com/synthetic/"
	if ok, err := e.Accept(context.Background(), m2); !ok || err != nil {
		t.Fatal(ok, err)
	}
	id2 := store.Hash(m2.ConversationID + "\x00" + m2.ID)
	if err := e.Dispatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := e.Store.DB.QueryRow("SELECT count(*) FROM job_files WHERE job=?", id2).Scan(&fileRows); err != nil || fileRows != 1 {
		t.Fatalf("expected staged row before cancel, got %d (%v)", fileRows, err)
	}
	if err := e.Cancel(id2); err != nil {
		t.Fatal(err)
	}
	if err := e.Store.DB.QueryRow("SELECT count(*) FROM job_files WHERE job=?", id2).Scan(&fileRows); err != nil || fileRows != 0 {
		t.Fatalf("job_files rows remaining after cancel: %d (%v)", fileRows, err)
	}
}

func TestNoDuplicatePlanSavesOnWorkerAndAISteps(t *testing.T) {
	t.Parallel()
	e, fixture := setup(t)
	rec := &recordingVideoTransport{Fixture: fixture}
	e.Transport = rec
	arm(t, e)
	c := configureAI(t, e, false, false)
	c.Commands = append(c.Commands, Command{Name: "download", Type: "download", Enabled: true})
	if err := e.SetConfiguration(c); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Store.DB.Exec(`CREATE TABLE plan_saves(job TEXT); CREATE TRIGGER count_plan_saves AFTER UPDATE OF plan ON jobs WHEN NEW.plan!='' BEGIN INSERT INTO plan_saves VALUES(NEW.id); END;`); err != nil {
		t.Fatal(err)
	}
	savesFor := func(id string) int {
		t.Helper()
		var n int
		if err := e.Store.DB.QueryRow("SELECT count(*) FROM plan_saves WHERE job=?", id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	// Normal AI step: 1 save for ai.chat Done + 1 for reply StatusIntent + 1 for reply Effect/StatusDone = 3.
	e.Provider = &transcriptProvider{}
	ask := incoming()
	ask.ID = "ask-normal"
	ask.Text = "!ask hello"
	if ok, err := e.Accept(context.Background(), ask); !ok || err != nil {
		t.Fatal(ok, err)
	}
	askID := store.Hash(ask.ConversationID + "\x00" + ask.ID)
	if err := e.Dispatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := savesFor(askID); got != 3 {
		t.Fatalf("normal AI job wrote plan %d times, want 3", got)
	}

	// AI step interrupted by new approval requirement: 1 save from cmdflow Yield.
	e.Provider = &hookProvider{onGenerate: func() {
		cfg, _ := e.Configuration()
		for i := range cfg.Commands {
			if cfg.Commands[i].Name == "ask" {
				cfg.Commands[i].Approval = true
			}
		}
		_ = e.SetConfiguration(cfg)
	}}
	askApproval := incoming()
	askApproval.ID = "ask-approval"
	askApproval.Text = "!ask needs approval"
	if ok, err := e.Accept(context.Background(), askApproval); !ok || err != nil {
		t.Fatal(ok, err)
	}
	askApprovalID := store.Hash(askApproval.ConversationID + "\x00" + askApproval.ID)
	if err := e.Dispatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if stateOf(t, e, askApprovalID) != "awaiting_approval" || savesFor(askApprovalID) != 1 {
		t.Fatalf("approval AI job state=%s saves=%d, want awaiting_approval and 1", stateOf(t, e, askApprovalID), savesFor(askApprovalID))
	}

	// Worker download + compress pipeline: 1 save for video.download + 1 for video.compress +
	// 1 for reply StatusIntent + 1 for reply Effect/StatusDone + 1 for background upload completion = 5.
	e.Tasks = &scriptTasks{fn: func(context.Context, string, int64) ([]byte, error) {
		return []byte("synthetic-video"), nil
	}}
	dl := incoming()
	dl.ID = "dl-saves"
	dl.Text = "!download https://v.douyin.com/synthetic/"
	if ok, err := e.Accept(context.Background(), dl); !ok || err != nil {
		t.Fatal(ok, err)
	}
	dlID := store.Hash(dl.ConversationID + "\x00" + dl.ID)
	if err := e.Dispatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	finishDownloadUploads(t, e)
	if err := e.Dispatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if stateOf(t, e, dlID) != "submitted" || savesFor(dlID) != 5 {
		t.Fatalf("download job state=%s saves=%d, want submitted and 5", stateOf(t, e, dlID), savesFor(dlID))
	}
}
