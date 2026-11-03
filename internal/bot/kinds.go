package bot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/deusical/smsbot-server/internal/store"
	"github.com/deusical/smsbot-server/internal/transport"
	"io"
	"strings"
)

// KindDefinition describes a versioned command implementation. Kinds build plans;
// permission to submit remains exclusively with the dispatcher.
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

// Input preserves the incoming quote/attachment references. Quoted content is
// resolved lazily through the existing conversation-scoped access checks.
type Input struct {
	transport.Incoming
	Arguments string
	Parent    *store.AIParent
}

type kind struct {
	KindDefinition
	plan    func(*Engine, context.Context, Input, *store.Plan, Configuration, Command, string, string, Generation) (string, bool, error)
	reply   func(*Engine, context.Context, transport.Incoming, *store.Plan, Configuration, Command, string, string, Generation, string, bool, *store.AIParent) (string, bool, error)
	prepare func(*Engine, context.Context, store.Job, *store.Plan) (bool, error)
	preview func(string, string, string) (string, string)
}

var kindRegistry map[string]kind

func init() {
	kindRegistry = map[string]kind{
		"fixed.reply": {KindDefinition: KindDefinition{ID: "fixed.reply", Title: "Fixed text", LegacyType: "fixed", Version: 1, RetryClass: "none", ResourceClass: "upload"}, plan: planReplyKind, reply: planFixedReply, prepare: noKindWork, preview: func(instructions, input, model string) (string, string) {
			return strings.ReplaceAll(instructions, "{input}", input), model
		}},
		"ai.generate": {KindDefinition: KindDefinition{ID: "ai.generate", Title: "AI", LegacyType: "ai", Version: 1, Threads: true, RetryClass: "none", ResourceClass: "provider"}, plan: planReplyKind, reply: planAIReply, prepare: noKindWork, preview: func(_, _, model string) (string, string) {
			return "Simulated AI response. No provider request was made.", model
		}},
		"media.video": {KindDefinition: KindDefinition{ID: "media.video", Title: "Download video", LegacyType: "download", Version: 1, RetryClass: "idempotent-poll", ResourceClass: "media"}, plan: planVideoKind, prepare: (*Engine).prepareDownload, preview: func(_, input, _ string) (string, string) {
			text := "Include one Douyin or Xiaohongshu video link after the command."
			if downloadURL(input) != "" {
				text = "Video link recognized. A live command saves the original on the server and sends a video under 20 MB. No download or send was performed by this preview."
			}
			return text, "Local"
		}},
	}

	for id, k := range kindRegistry {
		k.Schema = kindSchema(id)
		kindRegistry[id] = k
	}
}

func noKindWork(*Engine, context.Context, store.Job, *store.Plan) (bool, error) { return false, nil }

// Preparation only produces artifacts. Long work yields through DeferWork;
// submission remains in Dispatch, after the normal safety checks.
func prepareKind(e *Engine, ctx context.Context, j store.Job, p *store.Plan) (bool, error) {
	id := p.Kind
	if id == "" {
		if p.DownloadURL != "" {
			id = "media.video"
		} else {
			return false, nil
		}
	}
	k, ok := kindRegistry[id]
	if !ok {
		return true, errors.New("invalid command kind")
	}
	return k.prepare(e, ctx, j, p)
}

func commandKind(c Command) string {
	if c.Kind != "" {
		return c.Kind
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
func kindThreads(c Command) bool { return kindRegistry[commandKind(c)].Threads }

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
	return nil
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
	for _, id := range []string{"fixed.reply", "ai.generate", "media.video"} {
		d := kindRegistry[id].KindDefinition
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
