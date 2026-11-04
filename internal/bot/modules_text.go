package bot

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/deusical/smsbot-server/internal/pipeline"
)

func init() {
	registerModule(textTemplateModule())
	registerModule(extractURLModule())
	registerModule(replyModule())
}

type templateSettings struct {
	Template string `json:"template"`
}

// text.template fills a fixed text with its input: {input} is replaced.
func textTemplateModule() module {
	apply := func(_ *runCtx, s *pipeline.Step) (pipeline.Outcome, error) {
		var t templateSettings
		_ = s.Decode(&t)
		s.Out("text", textValue(strings.ReplaceAll(t.Template, "{input}", s.Text("input"))))
		return pipeline.Done(), nil
	}
	return module{
		Definition: ModuleDefinition{
			ID: "text.template", Version: 1, Title: "Text", Description: "Writes a fixed text. {input} is replaced by the connected input.",
			Inputs:  []Port{{Name: "input", Title: "Input", Types: []string{typeText, typeURL}}},
			Outputs: []Port{{Name: "text", Title: "Text", Types: []string{typeText}}},
			Effect:  effectPure, RetryClass: "none", ResourceClass: "local", Immediate: true,
			Schema: SettingsSchema{Type: "object", Properties: map[string]SettingsSchema{
				"template": {Type: "string", Title: "Text", UI: "multiline", Default: "", Maximum: bound(12000)},
			}},
		},
		Settings: func(raw json.RawMessage, _ any) (json.RawMessage, error) {
			var t templateSettings
			if err := strictSettings(raw, &t); err != nil {
				return nil, err
			}
			if len(t.Template) > 12000 {
				return nil, errors.New("text is too long")
			}
			return json.Marshal(t)
		},
		Accept: func(c *runCtx, s *pipeline.Step) (pipeline.Outcome, error) {
			if _, ok := s.In("input"); s.Bound("input") && !ok {
				return pipeline.Pending(), nil
			}
			return apply(c, s)
		},
		Run:      apply,
		Simulate: apply,
	}
}

const noVideoLink = "Include one Douyin or Xiaohongshu video link after the command."

// text.extract_url finds the one supported video link in its text. Without
// one, the command answers with how to use it.
func extractURLModule() module {
	find := func(_ *runCtx, s *pipeline.Step) (pipeline.Outcome, error) {
		u := downloadURL(s.Text("text"))
		if u == "" {
			return pipeline.Halt(textValue(noVideoLink)), nil
		}
		s.Out("url", pipeline.Value{Type: typeURL, Text: u})
		return pipeline.Done(), nil
	}
	return module{
		Definition: ModuleDefinition{
			ID: "text.extract_url", Version: 1, Title: "Find Douyin/Xiaohongshu link", Description: "Finds one Douyin or Xiaohongshu video link in the text, or replies with how to include one.",
			Inputs:  []Port{{Name: "text", Title: "Text", Types: []string{typeText}, Required: true}},
			Outputs: []Port{{Name: "url", Title: "Link", Types: []string{typeURL}}},
			Effect:  effectPure, RetryClass: "none", ResourceClass: "local", Immediate: true,
			Schema: SettingsSchema{Type: "object", Properties: map[string]SettingsSchema{}},
		},
		Settings: noSettings,
		Accept: func(c *runCtx, s *pipeline.Step) (pipeline.Outcome, error) {
			if _, ok := s.In("text"); !ok {
				return pipeline.Pending(), nil
			}
			return find(c, s)
		},
		Run:      find,
		Simulate: find,
	}
}

// reply is the pipeline's only sink. Its intent (the connected text, file and
// conversation) goes to SmsBot's host, which puts the text and file into the
// plan; the dispatcher then uploads, checks and submits it exactly as for
// every other reply. continues names the AI conversation this reply is a turn
// of, which is remembered only once the reply is submitted.
func replyModule() module {
	return module{
		Definition: ModuleDefinition{
			ID: "reply", Version: 1, Title: "Reply", Description: "Sends the text and file back in the conversation, after every safety check.",
			Inputs: []Port{
				{Name: "text", Title: "Text", Types: []string{typeText}},
				{Name: "file", Title: "File", Types: []string{typeVideo}},
				{Name: "continues", Title: "Continues conversation", Types: []string{typeContext}},
			},
			Outputs: []Port{},
			Effect:  effectSink, RetryClass: "none", ResourceClass: "upload",
			Schema: SettingsSchema{Type: "object", Properties: map[string]SettingsSchema{}},
		},
		Settings: noSettings,
		Accept: func(_ *runCtx, s *pipeline.Step) (pipeline.Outcome, error) {
			for _, port := range []string{"text", "file"} {
				if _, ok := s.In(port); s.Bound(port) && !ok {
					return pipeline.Pending(), nil
				}
			}
			return pipeline.Done(), nil
		},
		Run: func(*runCtx, *pipeline.Step) (pipeline.Outcome, error) { return pipeline.Done(), nil },
		Simulate: func(c *runCtx, s *pipeline.Step) (pipeline.Outcome, error) {
			c.sim.text = s.Text("text")
			if v, ok := s.In("file"); ok && c.sim.text == "" {
				c.sim.text = v.Text
			}
			return pipeline.Done(), nil
		},
	}
}
