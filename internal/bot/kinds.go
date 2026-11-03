package bot

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

// KindDefinition describes a built-in command template: a ready-made pipeline
// configured through a small settings form. "pipeline" is a command whose
// steps are configured directly.
type KindDefinition struct {
	Title         string         `json:"title"`
	LegacyType    string         `json:"legacy_type"`
	ID            string         `json:"id"`
	Version       int            `json:"version"`
	Threads       bool           `json:"threads"`
	RetryClass    string         `json:"retry_class"`
	ResourceClass string         `json:"resource_class"`
	Schema        SettingsSchema `json:"schema"`
}

var kindRegistry = map[string]KindDefinition{
	"fixed.reply": {ID: "fixed.reply", Title: "Fixed text", LegacyType: "fixed", Version: 1, RetryClass: "none", ResourceClass: "upload"},
	"ai.generate": {ID: "ai.generate", Title: "AI", LegacyType: "ai", Version: 1, Threads: true, RetryClass: "none", ResourceClass: "provider"},
	"media.video": {ID: "media.video", Title: "Download video", LegacyType: "download", Version: 1, RetryClass: "idempotent-poll", ResourceClass: "media"},
	"pipeline":    {ID: "pipeline", Title: "Custom steps", LegacyType: "fixed", Version: 1, RetryClass: "none", ResourceClass: "local"},
}

func init() {
	for id, k := range kindRegistry {
		k.Schema = kindSchema(id)
		kindRegistry[id] = k
	}
}

// templatePipeline is the pipeline a command runs, before validation. For a
// template kind it reproduces exactly what the command did before commands
// were pipelines.
func templatePipeline(c Command) Pipeline {
	raw := func(v any) json.RawMessage { b, _ := json.Marshal(v); return b }
	switch commandKind(c) {
	case "fixed.reply":
		return Pipeline{Steps: []PipelineStep{
			{ID: "text", Module: "text.template", Inputs: map[string]string{"input": "trigger.args"}, Settings: raw(templateSettings{Template: c.Instructions})},
			{ID: "reply", Module: "reply", Inputs: map[string]string{"text": "text.text"}},
		}}
	case "ai.generate":
		conversation := "context.none"
		if c.Memory {
			conversation = "context.rolling"
			if c.Name == "ask" {
				// Only the canonical ask continues explicit reply threads.
				conversation = "context.thread"
			}
		}
		return Pipeline{Steps: []PipelineStep{
			{ID: "question", Module: "message.question", Inputs: map[string]string{"text": "trigger.args", "message": "trigger.message"}, Settings: raw(questionSettings{RequireQuestion: c.Name == "ask"})},
			{ID: "conversation", Module: conversation},
			{ID: "answer", Module: "ai.chat", Inputs: map[string]string{"prompt": "question.prompt", "file": "question.file", "context": "conversation.context"}, Settings: raw(aiSettings{Model: c.Model, ProfileID: c.ProfileID, Instructions: c.Instructions, Generation: c.Generation})},
			{ID: "reply", Module: "reply", Inputs: map[string]string{"text": "answer.text", "continues": "answer.context"}},
		}}
	case "media.video":
		v := videoSettings(c)
		return Pipeline{ExecutionMinutes: v.ExecutionMinutes, Steps: []PipelineStep{
			{ID: "link", Module: "text.extract_url", Inputs: map[string]string{"text": "trigger.args"}},
			{ID: "video", Module: "video.download", Inputs: map[string]string{"url": "link.url"}, Settings: raw(videoStepSettings{v.Preset, v.Passes, v.MaxAttempts, v.CopyIfFits, v.MaxMB})},
			{ID: "reply", Module: "reply", Inputs: map[string]string{"file": "video.video"}},
		}}
	case "pipeline":
		if c.Pipeline != nil {
			return clonePipeline(*c.Pipeline)
		}
	}
	return Pipeline{}
}

func clonePipeline(p Pipeline) Pipeline {
	p.Steps = append([]PipelineStep(nil), p.Steps...)
	for i := range p.Steps {
		inputs := map[string]string{}
		for k, v := range p.Steps[i].Inputs {
			inputs[k] = v
		}
		p.Steps[i].Inputs = inputs
	}
	return p
}

// commandPipeline is the validated pipeline a command runs.
func commandPipeline(c Command) Pipeline {
	p := templatePipeline(c)
	if validatePipeline(&p, pipelineEnv{}) != nil {
		return Pipeline{}
	}
	return p
}

func commandKind(c Command) string {
	if c.Kind != "" {
		return c.Kind
	}
	if c.Type == "" && c.Pipeline != nil {
		return "pipeline"
	}
	switch c.Type {
	case "fixed":
		return "fixed.reply"
	case "ai":
		return "ai.generate"
	case "download":
		return "media.video"
	}
	return ""
}

// commandThreads reports whether a command continues AI reply threads.
func commandThreads(c Command) bool { return commandPipeline(c).has("context.thread") }

type commandSettings struct {
	Instructions     string      `json:"instructions,omitempty"`
	Model            string      `json:"model,omitempty"`
	ProfileID        string      `json:"profile_id,omitempty"`
	Memory           bool        `json:"memory,omitempty"`
	Generation       *Generation `json:"generation,omitempty"`
	ExecutionMinutes int         `json:"execution_minutes,omitempty"`
	Preset           *string     `json:"preset,omitempty"`
	Passes           *int        `json:"passes,omitempty"`
	MaxAttempts      *int        `json:"max_attempts,omitempty"`
	CopyIfFits       *bool       `json:"copy_if_fits,omitempty"`
	MaxMB            *float64    `json:"max_mb,omitempty"`
}

// normalizeCommand accepts either representation and writes both. A supplied
// settings object is authoritative and is decoded strictly to avoid data loss.
func normalizeCommand(c *Command) error {
	id := commandKind(*c)
	if _, ok := kindRegistry[id]; !ok {
		return errors.New("invalid command kind")
	}
	if id == "pipeline" {
		trimmed := bytes.TrimSpace(c.Settings)
		if c.Pipeline == nil || len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("{}")) && !bytes.Equal(trimmed, []byte("null")) {
			return errors.New("invalid command pipeline")
		}
		pl := clonePipeline(*c.Pipeline)
		if err := validatePipeline(&pl, pipelineEnv{}); err != nil {
			return err
		}
		// The flat fields describe what older servers understand: a fixed reply
		// with no text, which they never send.
		c.Kind, c.Type, c.Pipeline, c.Settings = id, "fixed", &pl, nil
		c.Instructions, c.Model, c.ProfileID, c.Generation = "", "", "", nil
		c.Memory = pl.has("context.rolling") || pl.has("context.thread")
		c.ExecutionMinutes = pl.ExecutionMinutes
		return nil
	}
	s := commandSettings{Instructions: c.Instructions, Model: c.Model, ProfileID: c.ProfileID, Memory: c.Memory, Generation: c.Generation, ExecutionMinutes: c.ExecutionMinutes}
	if len(c.Settings) > 0 {
		if len(bytes.TrimSpace(c.Settings)) == 0 || bytes.TrimSpace(c.Settings)[0] != '{' {
			return errors.New("invalid command settings")
		}
		s = commandSettings{}
		d := json.NewDecoder(bytes.NewReader(c.Settings))
		d.DisallowUnknownFields()
		if d.Decode(&s) != nil || d.Decode(new(any)) != io.EOF {
			return errors.New("invalid command settings")
		}
	}
	if id == "media.video" {
		v := DefaultVideoSettings()
		if s.Preset != nil {
			v.Preset = *s.Preset
		}
		if s.Passes != nil {
			v.Passes = *s.Passes
		}
		if s.MaxAttempts != nil {
			v.MaxAttempts = *s.MaxAttempts
		}
		if s.CopyIfFits != nil {
			v.CopyIfFits = *s.CopyIfFits
		}
		if s.MaxMB != nil {
			v.MaxMB = *s.MaxMB
		}
		if s.ExecutionMinutes != 0 {
			v.ExecutionMinutes = s.ExecutionMinutes
		}
		var raw map[string]json.RawMessage
		if len(c.Settings) > 0 {
			_ = json.Unmarshal(c.Settings, &raw)
			for _, key := range []string{"preset", "passes", "max_attempts", "copy_if_fits", "max_mb", "execution_minutes"} {
				if value, ok := raw[key]; ok && (bytes.Equal(bytes.TrimSpace(value), []byte("null")) || key == "execution_minutes" && s.ExecutionMinutes == 0) {
					return errors.New("invalid video settings")
				}
			}
		}
		if err := v.Validate(); err != nil {
			return err
		}
		s.Preset, s.Passes, s.MaxAttempts, s.CopyIfFits, s.MaxMB = &v.Preset, &v.Passes, &v.MaxAttempts, &v.CopyIfFits, &v.MaxMB
		s.ExecutionMinutes = v.ExecutionMinutes
	} else if s.Preset != nil || s.Passes != nil || s.MaxAttempts != nil || s.CopyIfFits != nil || s.MaxMB != nil {
		return errors.New("invalid command settings")
	}
	c.Kind = id
	switch id {
	case "fixed.reply":
		c.Type = "fixed"
	case "ai.generate":
		c.Type = "ai"
	case "media.video":
		c.Type = "download"
	}
	c.Instructions, c.Model, c.ProfileID, c.Memory, c.Generation, c.ExecutionMinutes = s.Instructions, s.Model, s.ProfileID, s.Memory, s.Generation, s.ExecutionMinutes
	c.Settings, _ = json.Marshal(s)
	// The kind's settings are authoritative; its steps are derived when run,
	// so the configuration carries them once.
	c.Pipeline = nil
	pl := templatePipeline(*c)
	return validatePipeline(&pl, pipelineEnv{})
}

// memoryKind is the AI conversation a command remembers: "thread", "rolling"
// or "" for none. Changing it, or turning memory off, withdraws consent.
func memoryKind(c Command) string {
	pl := commandPipeline(c)
	switch {
	case pl.has("context.thread"):
		return "thread"
	case pl.has("context.rolling"):
		return "rolling"
	}
	return ""
}
func normalizeCommands(c *Configuration) error {
	for i := range c.Commands {
		if err := normalizeCommand(&c.Commands[i]); err != nil {
			return err
		}
	}
	return nil
}

// SettingsSchema is the small, bounded form vocabulary exposed to clients.
type SettingsSchema struct {
	Type             string                    `json:"type"`
	Properties       map[string]SettingsSchema `json:"properties,omitempty"`
	Title            string                    `json:"title,omitempty"`
	UI               string                    `json:"ui,omitempty"`
	Default          any                       `json:"default,omitempty"`
	Minimum          *float64                  `json:"minimum,omitempty"`
	EffectiveMaximum *float64                  `json:"effective_maximum,omitempty"`
	Maximum          *float64                  `json:"maximum,omitempty"`
	Enum             []string                  `json:"enum,omitempty"`
	Unit             string                    `json:"unit,omitempty"`
}

func bound(v float64) *float64 { return &v }
func kindSchema(id string) SettingsSchema {
	fields := map[string]SettingsSchema{}
	if id != "media.video" {
		fields["instructions"] = SettingsSchema{Type: "string", Title: "Instructions", UI: "multiline", Default: "", Maximum: bound(12000)}
	}
	if id == "fixed.reply" {
		f := fields["instructions"]
		f.Title = "Reply"
		fields["instructions"] = f
	}
	if id == "ai.generate" {
		fields["model"] = SettingsSchema{Type: "string", Title: "Model", UI: "model", Default: "", Maximum: bound(200)}
		fields["profile_id"] = SettingsSchema{Type: "string", Title: "Profile", UI: "profile", Default: "", Maximum: bound(80)}
		fields["memory"] = SettingsSchema{Type: "boolean", Title: "Remember the conversation", Default: false}
		fields["generation"] = SettingsSchema{Type: "object", Title: "Generation", UI: "generation", Properties: map[string]SettingsSchema{
			"reasoning":     {Type: "string", Title: "Reasoning", Default: "default", Enum: []string{"default", "off", "minimal", "low", "medium", "high", "xhigh", "max"}},
			"answer_length": {Type: "string", Title: "Answer length", Default: "short", Enum: []string{"short", "standard", "detailed"}},
			"tokens":        {Type: "number", Title: "Tokens", Default: 2048, Minimum: bound(128), Maximum: bound(8192)},
			"temperature":   {Type: "number", Title: "Temperature", Minimum: bound(0), Maximum: bound(2)},
		}}
	}
	if id == "media.video" {
		fields["execution_minutes"] = SettingsSchema{Type: "number", Title: "Execution deadline (minutes)", Default: 15, Minimum: bound(1), Maximum: bound(60), Unit: "minutes"}
	}
	if id == "media.video" {
		fields["preset"] = SettingsSchema{Type: "string", Title: "Encoding preset", Default: "slow", Enum: []string{"veryfast", "faster", "fast", "medium", "slow"}}
		fields["passes"] = SettingsSchema{Type: "number", Title: "Passes", Default: 2, Minimum: bound(1), Maximum: bound(2)}
		fields["max_attempts"] = SettingsSchema{Type: "number", Title: "Maximum attempts", Default: 3, Minimum: bound(1), Maximum: bound(3)}
		fields["copy_if_fits"] = SettingsSchema{Type: "boolean", Title: "Copy original when it fits", Default: true}
		fields["max_mb"] = SettingsSchema{Type: "number", Title: "Maximum video size (MB)", Default: 19.99, Minimum: bound(0.1), Maximum: bound(19.99), Unit: "MB"}
		return SettingsSchema{Type: "object", UI: "video-cost", Properties: fields}
	}
	return SettingsSchema{Type: "object", Properties: fields}
}
func KindDefinitions() []KindDefinition {
	out := []KindDefinition{}
	for _, id := range []string{"fixed.reply", "ai.generate", "media.video", "pipeline"} {
		d := kindRegistry[id]
		out = append(out, d)
	}
	return out
}

func sameSettings(a, b json.RawMessage) bool {
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return false
	}
	ax, _ := json.Marshal(x)
	by, _ := json.Marshal(y)
	return bytes.Equal(ax, by)
}
