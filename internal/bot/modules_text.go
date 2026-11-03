package bot

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/deusical/smsbot-server/internal/media"
	"github.com/deusical/smsbot-server/internal/store"
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
	apply := func(s *stepRun) {
		var t templateSettings
		_ = s.decode(&t)
		s.out("text", store.Value{Type: typeText, Text: strings.ReplaceAll(t.Template, "{input}", s.text("input"))})
		s.Done = true
	}
	return module{
		ModuleDefinition: ModuleDefinition{
			ID: "text.template", Version: 1, Title: "Text", Description: "Writes a fixed text. {input} is replaced by the connected input.",
			Inputs:  []Port{{Name: "input", Title: "Input", Types: []string{typeText, typeURL}}},
			Outputs: []Port{{Name: "text", Title: "Text", Types: []string{typeText}}},
			Effect:  effectPure, RetryClass: "none", ResourceClass: "local", Immediate: true,
			Schema: SettingsSchema{Type: "object", Properties: map[string]SettingsSchema{
				"template": {Type: "string", Title: "Text", UI: "multiline", Default: "", Maximum: bound(12000)},
			}},
		},
		settings: func(raw json.RawMessage, _ pipelineEnv) (json.RawMessage, error) {
			var t templateSettings
			if err := strictSettings(raw, &t); err != nil {
				return nil, err
			}
			if len(t.Template) > 12000 {
				return nil, errors.New("text is too long")
			}
			return json.Marshal(t)
		},
		accept: func(_ *Engine, _ *acceptRun, s *stepRun) (acceptOutcome, error) {
			if s.bound("input") {
				if _, ok := s.in("input"); !ok {
					return stepPending, nil
				}
			}
			apply(s)
			return stepDone, nil
		},
		run: func(_ *Engine, _ *execRun, s *stepRun) (bool, error) {
			apply(s)
			return false, nil
		},
		simulate: func(_ *simRun, s *stepRun) error {
			apply(s)
			return nil
		},
	}
}

const noVideoLink = "Include one Douyin or Xiaohongshu video link after the command."

// text.extract_url finds the one supported video link in its text. Without
// one, the command answers with how to use it.
func extractURLModule() module {
	find := func(s *stepRun) (string, bool) {
		u := downloadURL(s.text("text"))
		if u == "" {
			return noVideoLink, false
		}
		s.out("url", store.Value{Type: typeURL, Text: u})
		s.Done = true
		return "", true
	}
	return module{
		ModuleDefinition: ModuleDefinition{
			ID: "text.extract_url", Version: 1, Title: "Find video link", Description: "Finds one Douyin or Xiaohongshu video link in the text, or replies with how to include one.",
			Inputs:  []Port{{Name: "text", Title: "Text", Types: []string{typeText}, Required: true}},
			Outputs: []Port{{Name: "url", Title: "Link", Types: []string{typeURL}}},
			Effect:  effectPure, RetryClass: "none", ResourceClass: "local", Immediate: true,
			Schema: SettingsSchema{Type: "object", Properties: map[string]SettingsSchema{}},
		},
		settings: noSettings,
		accept: func(_ *Engine, a *acceptRun, s *stepRun) (acceptOutcome, error) {
			if _, ok := s.in("text"); !ok {
				return stepPending, nil
			}
			if text, ok := find(s); !ok {
				a.halt = text
				return stepHalt, nil
			}
			return stepDone, nil
		},
		run: func(_ *Engine, r *execRun, s *stepRun) (bool, error) {
			if text, ok := find(s); !ok {
				haltPlan(r.p, text)
				s.Done = true
			}
			return false, nil
		},
		simulate: func(sim *simRun, s *stepRun) error {
			if text, ok := find(s); !ok {
				sim.text = text
			}
			return nil
		},
	}
}

// reply is the pipeline's only sink. It puts the text and file to send into
// the plan; the dispatcher then uploads, checks and submits it exactly as for
// every other reply. continues names the AI conversation this reply is a turn
// of, which is remembered only once the reply is submitted.
func replyModule() module {
	fill := func(p *store.Plan, s *stepRun) {
		p.Reply = s.text("text")
		if v, ok := s.in("file"); ok && v.Media != nil {
			p.Attachments = []media.File{*v.Media}
			// The plan carries the bytes once, as the reply's attachment.
			from, port, _ := strings.Cut(s.Inputs["file"], ".")
			for i := range s.steps {
				if s.steps[i].ID == from {
					out := s.steps[i].Outputs[port]
					out.Media = nil
					s.steps[i].Outputs[port] = out
				}
			}
		}
		s.Done = true
	}
	return module{
		ModuleDefinition: ModuleDefinition{
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
		settings: noSettings,
		accept: func(_ *Engine, a *acceptRun, s *stepRun) (acceptOutcome, error) {
			for _, port := range []string{"text", "file"} {
				if _, ok := s.in(port); s.bound(port) && !ok {
					return stepPending, nil
				}
			}
			fill(a.p, s)
			return stepDone, nil
		},
		run: func(_ *Engine, r *execRun, s *stepRun) (bool, error) {
			fill(r.p, s)
			return false, nil
		},
		simulate: func(sim *simRun, s *stepRun) error {
			sim.text = s.text("text")
			if v, ok := s.in("file"); ok && sim.text == "" {
				sim.text = v.Text
			}
			return nil
		},
	}
}
