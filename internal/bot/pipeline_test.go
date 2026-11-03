package bot

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/deusical/smsbot-server/internal/store"
)

func step(id, module string, inputs map[string]string, settings string) PipelineStep {
	s := PipelineStep{ID: id, Module: module, Inputs: inputs}
	if settings != "" {
		s.Settings = json.RawMessage(settings)
	}
	return s
}

func reply(inputs map[string]string) PipelineStep { return step("reply", "reply", inputs, "") }

func TestPipelineValidation(t *testing.T) {
	ok := Pipeline{Steps: []PipelineStep{
		step("caption", "text.template", map[string]string{"input": "trigger.args"}, `{"template":"Hi {input}"}`),
		reply(map[string]string{"text": "caption.text"}),
	}}
	if err := validatePipeline(&ok, pipelineEnv{}); err != nil {
		t.Fatal(err)
	}
	if ok.Steps[0].Version != 1 {
		t.Fatal("version not pinned")
	}
	cases := map[string]Pipeline{
		"empty":            {},
		"no reply":         {Steps: []PipelineStep{step("caption", "text.template", nil, `{"template":"x"}`)}},
		"reply not last":   {Steps: []PipelineStep{reply(map[string]string{"text": "trigger.args"}), step("caption", "text.template", nil, "")}},
		"two replies":      {Steps: []PipelineStep{reply(map[string]string{"text": "trigger.args"}), step("again", "reply", map[string]string{"text": "trigger.args"}, "")}},
		"unknown module":   {Steps: []PipelineStep{step("x", "shell.exec", nil, ""), reply(map[string]string{"text": "trigger.args"})}},
		"unknown input":    {Steps: []PipelineStep{step("x", "text.template", map[string]string{"cmd": "trigger.args"}, ""), reply(map[string]string{"text": "x.text"})}},
		"missing required": {Steps: []PipelineStep{step("link", "text.extract_url", nil, ""), reply(map[string]string{"text": "trigger.args"})}},
		"later step":       {Steps: []PipelineStep{step("a", "text.template", map[string]string{"input": "b.text"}, ""), step("b", "text.template", nil, ""), reply(map[string]string{"text": "a.text"})}},
		"type mismatch":    {Steps: []PipelineStep{step("v", "video.download", map[string]string{"url": "trigger.args"}, ""), reply(map[string]string{"file": "v.video"})}},
		"reply file text":  {Steps: []PipelineStep{reply(map[string]string{"file": "trigger.args"})}},
		"nothing to send":  {Steps: []PipelineStep{reply(nil)}},
		"bad setting":      {Steps: []PipelineStep{step("x", "text.template", nil, `{"template":"x","shell":"rm"}`), reply(map[string]string{"text": "x.text"})}},
		"bad step id":      {Steps: []PipelineStep{step("trigger", "text.template", nil, ""), reply(map[string]string{"text": "trigger.text"})}},
		"duplicate id":     {Steps: []PipelineStep{step("x", "text.template", nil, ""), step("x", "text.template", nil, ""), reply(map[string]string{"text": "x.text"})}},
		"video bounds":     {Steps: []PipelineStep{step("link", "text.extract_url", map[string]string{"text": "trigger.args"}, ""), step("v", "video.download", map[string]string{"url": "link.url"}, `{"passes":5}`), reply(map[string]string{"file": "v.video"})}},
		"two ai steps": {Steps: []PipelineStep{
			step("a", "ai.chat", map[string]string{"prompt": "trigger.args"}, ""),
			step("b", "ai.chat", map[string]string{"prompt": "a.text"}, ""),
			reply(map[string]string{"text": "b.text"}),
		}},
		"question from later work": {Steps: []PipelineStep{
			step("a", "ai.chat", map[string]string{"prompt": "trigger.args"}, ""),
			step("q", "message.question", map[string]string{"text": "a.text", "message": "trigger.message"}, ""),
			reply(map[string]string{"text": "q.prompt"}),
		}},
		"thread not continued": {Steps: []PipelineStep{
			step("c", "context.thread", nil, ""),
			step("a", "ai.chat", map[string]string{"prompt": "trigger.args", "context": "c.context"}, ""),
			reply(map[string]string{"text": "a.text"}),
		}},
	}
	tooMany := Pipeline{}
	for i := 0; i < maxPipelineSteps; i++ {
		tooMany.Steps = append(tooMany.Steps, step("s"+string(rune('a'+i)), "text.template", nil, ""))
	}
	tooMany.Steps = append(tooMany.Steps, reply(map[string]string{"text": "sa.text"}))
	cases["too many"] = tooMany
	for name, p := range cases {
		err := validatePipeline(&p, pipelineEnv{})
		var perr *PipelineError
		if err == nil || !errors.As(err, &perr) {
			t.Errorf("%s: accepted (%v)", name, err)
		}
	}
}

func TestTemplatesAreValidPipelines(t *testing.T) {
	for _, c := range []Command{
		{Name: "echo", Type: "fixed", Instructions: "Hi {input}"},
		{Name: "ask", Type: "ai", Memory: true},
		{Name: "ask", Type: "ai"},
		{Name: "translate", Type: "ai", Memory: true},
		{Name: "download", Type: "download", ExecutionMinutes: 20},
	} {
		if err := normalizeCommand(&c); err != nil || len(commandPipeline(c).Steps) == 0 {
			t.Fatalf("%s/%s: %v", c.Name, c.Type, err)
		}
	}
	if !commandPipeline(Command{Name: "ask", Type: "ai", Memory: true}).has("context.thread") || !commandPipeline(Command{Name: "translate", Type: "ai", Memory: true}).has("context.rolling") {
		t.Fatal("memory mapped to the wrong conversation")
	}
	if commandPipeline(Command{Type: "download", ExecutionMinutes: 20}).ExecutionMinutes != 20 {
		t.Fatal("deadline lost")
	}
}

func TestModuleDefinitionsAreComplete(t *testing.T) {
	defs := ModuleDefinitions()
	if len(defs) < 9 {
		t.Fatal("modules missing")
	}
	sinks := 0
	for _, d := range defs {
		if d.ID == "" || d.Version < 1 || d.Title == "" || d.Description == "" || d.Effect == "" || d.RetryClass == "" || d.ResourceClass == "" || d.Schema.Type != "object" {
			t.Fatalf("incomplete module %+v", d)
		}
		if d.Effect == effectSink {
			sinks++
		}
		for _, p := range append(append([]Port{}, d.Inputs...), d.Outputs...) {
			if p.Name == "" || len(p.Types) == 0 {
				t.Fatalf("incomplete port on %s", d.ID)
			}
		}
	}
	if sinks != 1 {
		t.Fatal("only reply may send")
	}
}

func customCommand(name string, p Pipeline) Command {
	return Command{Name: name, Kind: "pipeline", Enabled: true, Pipeline: &p}
}

// A command built only from configuration: no command-specific code.
func TestCustomTextPipelineRunsEndToEnd(t *testing.T) {
	e, fixture := setup(t)
	arm(t, e)
	c := DefaultConfiguration()
	c.Commands = []Command{customCommand("shout", Pipeline{Steps: []PipelineStep{
		step("loud", "text.template", map[string]string{"input": "trigger.args"}, `{"template":"{input}!!!"}`),
		step("framed", "text.template", map[string]string{"input": "loud.text"}, `{"template":"<< {input} >>"}`),
		reply(map[string]string{"text": "framed.text"}),
	}})}
	if err := e.SetConfiguration(c); err != nil {
		t.Fatal(err)
	}
	m := incoming()
	m.Text = "!shout hello"
	if ok, err := e.Accept(context.Background(), m); !ok || err != nil {
		t.Fatal(ok, err)
	}
	if err := e.Dispatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, text := range fixture.Sent {
		if text != "<< hello!!! >>" {
			t.Fatalf("sent %q", text)
		}
		return
	}
	t.Fatal("nothing sent")
}

func TestCustomPipelineSnapshotIsImmutable(t *testing.T) {
	e, _ := setup(t)
	arm(t, e)
	c := DefaultConfiguration()
	c.Commands = []Command{customCommand("note", Pipeline{Steps: []PipelineStep{
		step("t", "text.template", map[string]string{"input": "trigger.args"}, `{"template":"first {input}"}`),
		reply(map[string]string{"text": "t.text"}),
	}})}
	c.Commands[0].Approval = true
	if err := e.SetConfiguration(c); err != nil {
		t.Fatal(err)
	}
	m := incoming()
	m.Text = "!note x"
	if ok, err := e.Accept(context.Background(), m); !ok || err != nil {
		t.Fatal(ok, err)
	}
	id := store.Hash(m.ConversationID + "\x00" + m.ID)
	_, before, _ := e.Store.JobPlan(id)
	c, _ = e.Configuration()
	c.Commands[0].Pipeline.Steps[0].Settings = json.RawMessage(`{"template":"second {input}"}`)
	if err := e.SetConfiguration(c); err != nil {
		t.Fatal(err)
	}
	_, after, _ := e.Store.JobPlan(id)
	a, _ := json.Marshal(before.Pipeline)
	b, _ := json.Marshal(after.Pipeline)
	if string(a) != string(b) || after.Reply != "first x" {
		t.Fatal("accepted job changed with the command")
	}
}

// An AI prompt built by an earlier step, with no AI-command code.
func TestCustomPromptFeedsTheAIStep(t *testing.T) {
	e, fixture := setup(t)
	arm(t, e)
	c := configureAI(t, e, false, false)
	c.Commands = append(c.Commands, customCommand("french", Pipeline{Steps: []PipelineStep{
		step("prompt", "text.template", map[string]string{"input": "trigger.args"}, `{"template":"Translate into French: {input}"}`),
		step("answer", "ai.chat", map[string]string{"prompt": "prompt.text"}, `{"instructions":"You translate."}`),
		reply(map[string]string{"text": "answer.text"}),
	}}))
	if err := e.SetConfiguration(c); err != nil {
		t.Fatal(err)
	}
	provider := &transcriptProvider{}
	e.Provider = provider
	m := incoming()
	m.Text = "!french good morning"
	if ok, err := e.Accept(context.Background(), m); !ok || err != nil {
		t.Fatal(ok, err)
	}
	if err := e.Dispatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, text := range fixture.Sent {
		if text != "AI: Translate into French: good morning" {
			t.Fatalf("sent %q", text)
		}
		return
	}
	t.Fatal("nothing sent")
}

// A native reply continues whichever command answered, not only ask.
func TestAnyThreadedCommandContinuesItsOwnThread(t *testing.T) {
	e, _ := setup(t)
	arm(t, e)
	c := configureAI(t, e, true, false)
	c.Commands = append(c.Commands, customCommand("tutor", Pipeline{Steps: []PipelineStep{
		step("question", "message.question", map[string]string{"text": "trigger.args", "message": "trigger.message"}, `{"require_question":true}`),
		step("conversation", "context.thread", nil, ""),
		step("answer", "ai.chat", map[string]string{"prompt": "question.prompt", "file": "question.file", "context": "conversation.context"}, `{"instructions":"Teach."}`),
		reply(map[string]string{"text": "answer.text", "continues": "answer.context"}),
	}}))
	if err := e.SetConfiguration(c); err != nil {
		t.Fatal(err)
	}
	e.Provider = &transcriptProvider{}
	root := threadPrompt("root", "!tutor What is a prime?", "")
	threadAccept(t, e, root)
	if err := e.Dispatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	threadEcho(t, e, root, "answer")
	p := threadAccept(t, e, threadPrompt("child", "And 7?", "answer"))
	if p.Thread == nil || len(p.Context) != 2 || !strings.Contains(p.Context[0].Content, "What is a prime?") {
		t.Fatalf("thread not continued: %+v", p.Context)
	}
	var command string
	if err := e.Store.DB.QueryRow("SELECT command FROM jobs WHERE id=?", store.Hash("demo-chat\x00child")).Scan(&command); err != nil || command != "!tutor" {
		t.Fatal("reply routed to", command, err)
	}
	// An explicit different command does not continue tutor's thread.
	p = threadAccept(t, e, threadPrompt("other", "!ask Unrelated", "answer"))
	if p.Thread == nil || p.Thread.Parent != "" || len(p.Context) != 0 {
		t.Fatal("another command continued tutor's thread")
	}
}

// A composed download: link, video, caption and reply, all configuration.
func TestCustomVideoWithCaption(t *testing.T) {
	e, fixture := setup(t)
	arm(t, e)
	c := DefaultConfiguration()
	c.Commands = []Command{customCommand("grab", Pipeline{ExecutionMinutes: 5, Steps: []PipelineStep{
		step("link", "text.extract_url", map[string]string{"text": "trigger.args"}, ""),
		step("video", "video.download", map[string]string{"url": "link.url"}, `{"preset":"fast"}`),
		step("caption", "text.template", map[string]string{"input": "link.url"}, `{"template":"Here you go: {input}"}`),
		reply(map[string]string{"text": "caption.text", "file": "video.video"}),
	}})}
	if err := e.SetConfiguration(c); err != nil {
		t.Fatal(err)
	}
	e.Downloader = downloadFunc(func(context.Context, string, int64) ([]byte, error) { return []byte("synthetic-video"), nil })
	m := incoming()
	m.Text = "!grab https://v.douyin.com/synthetic/"
	if ok, err := e.Accept(context.Background(), m); !ok || err != nil {
		t.Fatal(ok, err)
	}
	id := store.Hash(m.ConversationID + "\x00" + m.ID)
	if err := e.Dispatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	finishDownloadUploads(t, e)
	if err := e.Dispatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := stateOf(t, e, id); got != "submitted" {
		t.Fatal("state", got)
	}
	for _, text := range fixture.Sent {
		if text != "Here you go: https://v.douyin.com/synthetic/" {
			t.Fatalf("sent %q", text)
		}
	}
	// Without a link the composed command answers how to use it.
	m.ID, m.Text = "no-link", "!grab nothing here"
	if ok, err := e.Accept(context.Background(), m); !ok || err != nil {
		t.Fatal(ok, err)
	}
	_, p, _ := e.Store.JobPlan(store.Hash(m.ConversationID + "\x00no-link"))
	if p.Reply != noVideoLink || !p.Pipeline.Halted || p.ExecutionDeadline != "" {
		t.Fatal("missing link not answered", p.Reply)
	}
}

func TestCustomPipelineSurvivesConfigurationRoundTrip(t *testing.T) {
	e, _ := setup(t)
	c := DefaultConfiguration()
	c.Commands = []Command{customCommand("shout", Pipeline{Steps: []PipelineStep{
		step("loud", "text.template", map[string]string{"input": "trigger.args"}, `{"template":"{input}!"}`),
		reply(map[string]string{"text": "loud.text"}),
	}})}
	if err := e.SetConfiguration(c); err != nil {
		t.Fatal(err)
	}
	loaded, err := e.Configuration()
	if err != nil {
		t.Fatal(err)
	}
	got := loaded.Commands[0]
	if got.Kind != "pipeline" || got.Type != "fixed" || got.Instructions != "" || got.Pipeline == nil || len(got.Pipeline.Steps) != 2 {
		t.Fatalf("custom pipeline not kept: %+v", got)
	}
	// Template commands stay driven by their settings and carry no steps.
	c.Commands = []Command{{Name: "echo", Type: "fixed", Enabled: true, Instructions: "Hi {input}"}}
	c.Revision = loaded.Revision
	if err := e.SetConfiguration(c); err != nil {
		t.Fatal(err)
	}
	loaded, _ = e.Configuration()
	if loaded.Commands[0].Kind != "fixed.reply" || loaded.Commands[0].Pipeline != nil || commandPipeline(loaded.Commands[0]).Steps[0].Module != "text.template" {
		t.Fatal("template changed")
	}
	// A profile a pipeline names must exist.
	bad := DefaultConfiguration()
	bad.Commands = []Command{customCommand("x", Pipeline{Steps: []PipelineStep{
		step("a", "ai.chat", map[string]string{"prompt": "trigger.args"}, `{"profile_id":"missing"}`),
		reply(map[string]string{"text": "a.text"}),
	}})}
	if bad.Validate() == nil {
		t.Fatal("unknown profile accepted")
	}
}

func TestPreviewTracesEachStep(t *testing.T) {
	e, _ := setup(t)
	c := DefaultConfiguration()
	c.Commands = []Command{customCommand("grab", Pipeline{Steps: []PipelineStep{
		step("link", "text.extract_url", map[string]string{"text": "trigger.args"}, ""),
		step("video", "video.download", map[string]string{"url": "link.url"}, ""),
		reply(map[string]string{"file": "video.video"}),
	}})}
	if err := e.SetConfiguration(c); err != nil {
		t.Fatal(err)
	}
	r, err := e.Playground("grab", "https://douyin.com/video/1")
	if err != nil {
		t.Fatal(err)
	}
	steps := r["steps"].([]map[string]any)
	if r["text"] != videoPreview || len(steps) != 3 || steps[0]["outputs"].(map[string]string)["url"] != "https://douyin.com/video/1" {
		t.Fatalf("preview %+v", r)
	}
	r, _ = e.Playground("grab", "no link")
	if r["text"] != noVideoLink || len(r["steps"].([]map[string]any)) != 1 {
		t.Fatalf("halted preview %+v", r)
	}
}

// Converting a template command to equivalent custom steps keeps memory.
func TestConvertingToCustomStepsKeepsMemoryConsent(t *testing.T) {
	e, _ := setup(t)
	arm(t, e)
	c := configureAI(t, e, true, false)
	root := threadPrompt("root", "!ask First", "")
	threadAccept(t, e, root)
	if err := e.Dispatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	threadEcho(t, e, root, "answer")
	c, _ = e.Configuration()
	pl := commandPipeline(c.Commands[0])
	c.Commands[0] = Command{Name: "ask", Kind: "pipeline", Enabled: true, Pipeline: &pl}
	if err := e.SetConfiguration(c); err != nil {
		t.Fatal(err)
	}
	if p := threadAccept(t, e, threadPrompt("child", "Next", "answer")); len(p.Context) != 2 {
		t.Fatal("conversion cleared the conversation")
	}
	// Switching the conversation kind withdraws consent.
	c, _ = e.Configuration()
	c.Commands[0].Pipeline.Steps[1].Module = "context.rolling"
	if err := e.SetConfiguration(c); err != nil {
		t.Fatal(err)
	}
	if ok, err := e.Accept(context.Background(), threadPrompt("late", "More", "answer")); ok || err != nil {
		t.Fatal("old thread continued after consent changed", ok, err)
	}
}

// A link produced at dispatch (here by the AI) still gets the video step's
// effective settings fixed before the worker is asked.
func TestVideoLinkFromALaterStep(t *testing.T) {
	e, fixture := setup(t)
	arm(t, e)
	c := configureAI(t, e, false, false)
	c.Commands = append(c.Commands, customCommand("find", Pipeline{ExecutionMinutes: 5, Steps: []PipelineStep{
		step("answer", "ai.chat", map[string]string{"prompt": "trigger.args"}, ""),
		step("link", "text.extract_url", map[string]string{"text": "answer.text"}, ""),
		step("video", "video.download", map[string]string{"url": "link.url"}, `{"max_mb":5}`),
		reply(map[string]string{"file": "video.video"}),
	}}))
	if err := e.SetConfiguration(c); err != nil {
		t.Fatal(err)
	}
	e.Provider = &transcriptProvider{}
	asked := ""
	var limit int64
	e.Downloader = downloadFunc(func(_ context.Context, u string, max int64) ([]byte, error) {
		asked, limit = u, max
		return []byte("synthetic-video"), nil
	})
	m := incoming()
	m.Text = "!find https://v.douyin.com/late/"
	if ok, err := e.Accept(context.Background(), m); !ok || err != nil {
		t.Fatal(ok, err)
	}
	id := store.Hash(m.ConversationID + string(rune(0)) + m.ID)
	_, p, _ := e.Store.JobPlan(id)
	if strings.Contains(string(videoStep(t, p).State), "late") {
		t.Fatal("link known before the AI answered")
	}
	for i := 0; i < 3; i++ {
		if err := e.Dispatch(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	finishDownloadUploads(t, e)
	if err := e.Dispatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if asked != "https://v.douyin.com/late/" || limit != 5_000_000 {
		t.Fatalf("worker asked for %q with limit %d", asked, limit)
	}
	if got := stateOf(t, e, id); got != "submitted" || len(fixture.Sent) != 1 {
		t.Fatal("state", got)
	}
}
