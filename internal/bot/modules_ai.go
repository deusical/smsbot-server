package bot

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/deusical/smsbot-server/internal/pipeline"
	"github.com/deusical/smsbot-server/internal/store"
)

func init() {
	registerModule(questionModule())
	registerModule(contextModule("context.none", "No memory", "Each request starts a new conversation.", "none"))
	registerModule(contextModule("context.rolling", "Recent memory", "Remembers this command's recent exchanges with the person (or with the group, when it shares one conversation).", "rolling"))
	registerModule(contextModule("context.thread", "Reply threads", "A native reply to an AI answer continues that answer's conversation; any other request starts a new one.", "thread"))
	registerModule(aiChatModule())
}

type questionSettings struct {
	RequireQuestion bool `json:"require_question"`
}

// message.question turns the command message into an AI prompt: the text
// after the command, the file sent with it, and the messages it natively
// replies to (unless it continues an AI thread, which has its own history).
func questionModule() module {
	return module{
		Definition: ModuleDefinition{
			ID: "message.question", Version: 1, Title: "Question", Description: "Builds a question from the command text, its file and the messages it replies to.",
			Inputs: []Port{
				{Name: "text", Title: "Question text", Types: []string{typeText}, Required: true},
				{Name: "message", Title: "Message", Types: []string{typeMessage}, Required: true},
			},
			Outputs: []Port{
				{Name: "prompt", Title: "Prompt", Types: []string{typeText}},
				{Name: "file", Title: "File", Types: []string{typeFile}},
			},
			Effect: effectPure, RetryClass: "none", ResourceClass: "local", Immediate: true,
			Schema: SettingsSchema{Type: "object", Properties: map[string]SettingsSchema{
				"require_question": {Type: "boolean", Title: "Ask for a question when none is given", Default: false},
			}},
		},
		Settings: func(raw json.RawMessage, _ any) (json.RawMessage, error) {
			var q questionSettings
			if err := strictSettings(raw, &q); err != nil {
				return nil, err
			}
			return json.Marshal(q)
		},
		Accept: func(a *runCtx, s *pipeline.Step) (pipeline.Outcome, error) {
			e := a.e
			var q questionSettings
			_ = s.Decode(&q)
			input := s.Text("text")
			own := ownFile(a.m)
			var file *store.FileRef
			if own != nil {
				reply := fileUnreadable(*own, a.cfg.Settings.AIPhotoMaxMB)
				if reply == "" && !e.filesSaved(a.m.ConversationID) {
					reply = "I can only read files in conversations you archive. Turn that on in Settings, or reply to the file with !" + a.cmd.Name + " once it's saved."
				}
				if reply != "" {
					return pipeline.Halt(textValue(reply)), nil
				}
				input = ownFilePrompt(input, own)
				file = own
			}
			if a.parent == nil {
				if quoted, qf, ok := e.quotedInput(a.m, input, a.p.Roster != "", own != nil); ok {
					// A bare command on a quoted message responds to that message.
					input = quoted
					if qf != nil && own == nil {
						if reply := fileUnreadable(*qf, a.cfg.Settings.AIPhotoMaxMB); reply != "" {
							return pipeline.Halt(textValue(reply)), nil
						}
						file = qf
					}
				}
			}
			if q.RequireQuestion && input == "" {
				return pipeline.Halt(textValue("Send !" + a.cmd.Name + " followed by a question. Reply to an AI answer to continue it.")), nil
			}
			s.Out("prompt", textValue(input))
			if file != nil {
				s.Out("file", fileValue(file))
			}
			return pipeline.Done(), nil
		},
		AcceptOnly: true,
		Run:        pendingImmediate,
		Simulate: func(_ *runCtx, s *pipeline.Step) (pipeline.Outcome, error) {
			s.Out("prompt", textValue(s.Text("text")))
			return pipeline.Done(), nil
		},
	}
}

// Context modules name which AI conversation a request belongs to. The AI step
// that reads the conversation loads it with its own model and context budget,
// and the reply records the new turn once it is submitted.
func contextModule(id, title, description, kind string) module {
	set := func(_ *runCtx, s *pipeline.Step) (pipeline.Outcome, error) {
		s.Out("context", contextValue(kind))
		return pipeline.Done(), nil
	}
	return module{
		Definition: ModuleDefinition{
			ID: id, Version: 1, Title: title, Description: description,
			Inputs:  []Port{},
			Outputs: []Port{{Name: "context", Title: "Conversation", Types: []string{typeContext}}},
			Effect:  effectPure, RetryClass: "none", ResourceClass: "local", Immediate: true,
			Schema: SettingsSchema{Type: "object", Properties: map[string]SettingsSchema{}},
		},
		Settings:   noSettings,
		Accept:     set,
		AcceptOnly: true,
		Run:        pendingImmediate,
		Simulate:   set,
	}
}

type aiSettings struct {
	Model        string      `json:"model"`
	ProfileID    string      `json:"profile_id"`
	Instructions string      `json:"instructions"`
	Generation   *Generation `json:"generation,omitempty"`
}

// aiState is what an AI step fixed at acceptance.
type aiState struct {
	Prepared     bool            `json:"prepared"`
	Model        string          `json:"model"`
	Instructions string          `json:"instructions"`
	Generation   json.RawMessage `json:"generation"`
	Input        string          `json:"input"`
	File         *store.FileRef  `json:"file,omitempty"`
	History      []store.Turn    `json:"history,omitempty"`
	Conversation string          `json:"conversation"`
}

// resolveAI picks the model, instructions and generation settings the way a
// command always has: its own model, else its profile's, else the default.
func resolveAI(cfg Configuration, s aiSettings) (string, string, Generation) {
	model := cfg.Settings.DefaultModel
	instructions := s.Instructions
	for _, p := range cfg.Profiles {
		if p.ID == s.ProfileID {
			if p.Model != "" {
				model = p.Model
			}
			instructions = p.Instructions + "\n" + instructions
		}
	}
	if s.Model != "" {
		model = s.Model
	}
	g := DefaultGeneration()
	if value, ok := cfg.Settings.ModelGeneration[model]; ok {
		g = value
	}
	if s.Generation != nil {
		g = *s.Generation
	}
	return model, instructions, g
}

// ai.chat asks the configured model. With a conversation connected, it reads
// that conversation's earlier turns and outputs the conversation to continue.
func aiChatModule() module {
	// admitAI declines the command, as before pipelines, when no usable model is
	// configured: the message is then not a command at all.
	admit := func(a *runCtx, s *pipeline.Step) (Model, string, string, Generation, bool, error) {
		e := a.e
		var set aiSettings
		_ = s.Decode(&set)
		model, instructions, g := resolveAI(*a.cfg, set)
		if model == "" {
			return Model{}, "", "", g, false, nil
		}
		selected, err := e.model(model)
		if err != nil {
			return Model{}, "", "", g, false, err
		}
		if !e.ProviderConfiguredFor(selected) {
			return Model{}, "", "", g, false, nil
		}
		if err = validateModel(g, selected); err != nil {
			return Model{}, "", "", g, false, err
		}
		return selected, model, instructions, g, true, nil
	}
	return module{
		Definition: ModuleDefinition{
			ID: "ai.chat", Version: 1, Title: "AI answer", Description: "Asks the AI model, optionally continuing a conversation.",
			Inputs: []Port{
				{Name: "prompt", Title: "Prompt", Types: []string{typeText}, Required: true},
				{Name: "file", Title: "File", Types: []string{typeFile}},
				{Name: "context", Title: "Conversation", Types: []string{typeContext}},
			},
			Outputs: []Port{
				{Name: "text", Title: "Answer", Types: []string{typeText}},
				{Name: "context", Title: "Conversation", Types: []string{typeContext}},
			},
			Effect: effectPaid, RetryClass: "none", ResourceClass: "provider",
			Schema: SettingsSchema{Type: "object", Properties: map[string]SettingsSchema{
				"instructions": {Type: "string", Title: "Instructions", UI: "multiline", Default: "", Maximum: bound(12000)},
				"model":        {Type: "string", Title: "Model", UI: "model", Default: "", Maximum: bound(200)},
				"profile_id":   {Type: "string", Title: "Profile", UI: "profile", Default: "", Maximum: bound(80)},
				"generation": {Type: "object", Title: "Generation", UI: "generation", Properties: map[string]SettingsSchema{
					"reasoning":     {Type: "string", Title: "Reasoning", Default: "default", Enum: []string{"default", "off", "minimal", "low", "medium", "high", "xhigh", "max"}},
					"answer_length": {Type: "string", Title: "Answer length", Default: "short", Enum: []string{"short", "standard", "detailed"}},
					"tokens":        {Type: "number", Title: "Tokens", Default: 2048, Minimum: bound(128), Maximum: bound(8192)},
					"temperature":   {Type: "number", Title: "Temperature", Minimum: bound(0), Maximum: bound(2)},
				}},
			}},
		},
		Settings: func(raw json.RawMessage, rawEnv any) (json.RawMessage, error) {
			env := envOf(rawEnv)
			var set aiSettings
			if err := strictSettings(raw, &set); err != nil {
				return nil, err
			}
			if len(set.Model) > 200 || len(set.Instructions) > 12000 || len(set.ProfileID) > 80 {
				return nil, errors.New("invalid AI settings")
			}
			if set.ProfileID != "" && env.profiles != nil && !env.profiles[set.ProfileID] {
				return nil, errors.New("unknown profile")
			}
			if set.Generation != nil {
				if err := validateGeneration(*set.Generation); err != nil {
					return nil, err
				}
			}
			return json.Marshal(set)
		},
		Admit: func(a *runCtx, s *pipeline.Step) (bool, error) {
			_, _, _, _, ok, err := admit(a, s)
			return ok, err
		},
		Accept: func(a *runCtx, s *pipeline.Step) (pipeline.Outcome, error) {
			e := a.e
			selected, model, instructions, g, ok, err := admit(a, s)
			if !ok || err != nil {
				return pipeline.Decline(), err
			}
			st := aiState{Model: model, Instructions: instructions, Conversation: "none"}
			st.Generation, _ = json.Marshal(g)
			if v, ok := s.In("context"); ok {
				st.Conversation = valueContext(v)
			}
			// The conversation is fixed when the command arrives, even when the
			// prompt is built by an earlier step at dispatch.
			p := a.p
			if st.Conversation != "none" {
				p.Memory = true
				p.MemoryHours = a.cfg.Settings.MemoryHours
				if p.MemoryEpoch, err = e.Store.MemoryEpoch(a.m.ConversationID, p.MemberOf()); err != nil {
					return pipeline.Decline(), err
				}
			}
			if st.Conversation == "rolling" {
				if st.History, err = e.Store.Memory(memoryScope(a.m.ConversationID, "!"+a.cmd.Name, model, p.MemberOf()), e.now()); err != nil {
					return pipeline.Decline(), err
				}
			}
			prompt, known := s.In("prompt")
			if !known {
				// Built from a later value; prepared when the step runs. A
				// thread needs its prompt now, which validation guarantees.
				if turnStep(s.Steps()) == s.ID {
					mirrorAI(p, st)
				}
				s.Save(st)
				return pipeline.Pending(), nil
			}
			st.Input = prompt.Text
			if f, ok := s.In("file"); ok {
				st.File = valueFile(f)
			}
			switch st.Conversation {
			case "thread":
				thread, history, note, halt, err := e.planAIThread(a.m, p, a.parent, "!"+a.cmd.Name, selected, g, a.cfg.Settings, st.Input, st.Instructions, st.File)
				if err != nil {
					return pipeline.Decline(), err
				}
				if halt != "" {
					return pipeline.Halt(textValue(halt)), nil
				}
				p.Thread = thread
				st.History = history
				st.Instructions += note
			}
			if p.Shared && turnStep(s.Steps()) == s.ID {
				// One conversation among several people: each question says who
				// asked it. Other AI steps (translating, extracting) don't need it.
				st.Input = askerLabel(*p) + ": " + st.Input
				st.Instructions += "\nThis conversation is shared by a group chat. Each question starts with who asked it; answer the one who asked."
			}
			// A job makes one AI request; the plan records it, and with a
			// conversation it is the turn recorded when the reply is sent.
			if turnStep(s.Steps()) == s.ID {
				mirrorAI(p, st)
			}
			st.Prepared = true
			s.Save(st)
			return pipeline.Pending(), nil
		},
		Run: func(r *runCtx, s *pipeline.Step) (pipeline.Outcome, error) {
			e := r.e
			var st aiState
			if err := s.Load(&st); err != nil {
				return pipeline.Fail("pipeline_state_invalid"), nil
			}
			if !st.Prepared {
				st.Input = s.Text("prompt")
				if f, ok := s.In("file"); ok {
					st.File = valueFile(f)
				}
				if r.p.Shared && turnStep(s.Steps()) == s.ID {
					st.Input = askerLabel(*r.p) + ": " + st.Input
					st.Instructions += "\nThis conversation is shared by a group chat. Each question starts with who asked it; answer the one who asked."
				}
				st.Prepared = true
				s.Save(st)
				if turnStep(s.Steps()) == s.ID {
					mirrorAI(r.p, st)
				}
			}
			history := st.History
			if st.Conversation != "none" && r.p.Memory {
				// Memory cleared, or consent withdrawn, since acceptance.
				epoch, err := e.Store.MemoryEpoch(r.j.ConversationID, r.p.MemberOf())
				if err != nil {
					return pipeline.Yield(), err
				}
				cmd, _, _, _, enabled := resolve(*r.cfg, strings.TrimPrefix(r.j.Command, "!"))
				if epoch != r.p.MemoryEpoch || !enabled || !cmd.Memory {
					r.p.Memory = false
					r.p.Context = nil
					history = nil
				}
			}
			call := aiCall{Step: s.ID, Model: st.Model, Instructions: st.Instructions, Generation: st.Generation, Input: st.Input, File: st.File}
			// The answer is recorded in the step before the plan is saved, so
			// approval asked for after generating never regenerates it.
			return yielded(e.generateReply(r.ctx, r.j, r.p, r.cfg, call, history, func(text string) {
				s.Out("text", textValue(text))
				s.Out("context", contextValue(st.Conversation))
				s.Done = true
			}))
		},
		Simulate: func(c *runCtx, s *pipeline.Step) (pipeline.Outcome, error) {
			sim := c.sim
			var set aiSettings
			_ = s.Decode(&set)
			if model, _, _ := resolveAI(*c.cfg, set); model != "" {
				if sim.model == "Local" || sim.model == "" {
					sim.model = model
				} else if !strings.Contains(", "+sim.model+", ", ", "+model+", ") {
					sim.model += ", " + model
				}
			}
			kind := "none"
			if v, ok := s.In("context"); ok {
				kind = valueContext(v)
			}
			s.Out("text", textValue("Simulated AI response. No provider request was made."))
			s.Out("context", contextValue(kind))
			return pipeline.Done(), nil
		},
	}
}

func mirrorAI(p *store.Plan, st aiState) {
	p.Model, p.Input, p.Instructions, p.Generation, p.File, p.Context = st.Model, st.Input, st.Instructions, st.Generation, st.File, st.History
}

// aiCall is one provider request an AI step makes.
type aiCall struct {
	// Step names the AI step, so each request's usage is kept separately.
	Step         string
	Model        string
	Instructions string
	Generation   json.RawMessage
	Input        string
	File         *store.FileRef
}
