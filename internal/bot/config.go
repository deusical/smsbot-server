package bot

import (
	"encoding/json"
	"errors"
	"math"
	"regexp"
	"strings"
	"time"
)

type Generation struct {
	Reasoning    string   `json:"reasoning"`
	AnswerLength string   `json:"answer_length"`
	Tokens       int      `json:"tokens"`
	Temperature  *float64 `json:"temperature,omitempty"`
}
type Settings struct {
	DefaultModel    string                `json:"default_model"`
	HourlyLimit     int                   `json:"hourly_limit"`
	DailyLimit      int                   `json:"daily_limit"`
	DailyBudget     float64               `json:"daily_budget"`
	QuietEnabled    bool                  `json:"quiet_enabled"`
	QuietStart      int                   `json:"quiet_start"`
	QuietEnd        int                   `json:"quiet_end"`
	Timezone        string                `json:"timezone"`
	MemoryHours     int                   `json:"memory_hours"`
	RetentionDays   int                   `json:"retention_days"`
	MaxChars        int                   `json:"max_chars"`
	Favorites       []string              `json:"favorites"`
	ModelGeneration map[string]Generation `json:"model_generation"`
	// OwnerCommands lets the owner run commands from their own phone in
	// conversations with bot replies on.
	OwnerCommands bool `json:"owner_commands"`
}
type Command struct {
	Name                 string      `json:"name"`
	Description          string      `json:"description"`
	Instructions         string      `json:"instructions"`
	Type                 string      `json:"type"`
	Enabled              bool        `json:"enabled"`
	Model                string      `json:"model"`
	ProfileID            string      `json:"profile_id"`
	Memory               bool        `json:"memory"`
	Approval             bool        `json:"approval"`
	AllowedConversations []string    `json:"allowed_conversations"`
	Generation           *Generation `json:"generation,omitempty"`
}
type Profile struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Model        string `json:"model"`
	Instructions string `json:"instructions"`
}
type Configuration struct {
	Settings Settings  `json:"settings"`
	Commands []Command `json:"commands"`
	Profiles []Profile `json:"profiles"`
}

func DefaultConfiguration() Configuration {
	return Configuration{Settings: Settings{DefaultModel: "", HourlyLimit: 20, DailyLimit: 100, DailyBudget: 1, QuietStart: 22, QuietEnd: 8, Timezone: "UTC", MemoryHours: 24, RetentionDays: 7, MaxChars: 12000, Favorites: []string{}, ModelGeneration: map[string]Generation{}}, Commands: []Command{}, Profiles: []Profile{}}
}
func DefaultGeneration() Generation {
	return Generation{Reasoning: "default", AnswerLength: "short", Tokens: 2048}
}
func validateGeneration(g Generation) error {
	if g.Tokens < 128 || g.Tokens > 8192 || !strings.Contains("|default|off|minimal|low|medium|high|xhigh|max|", "|"+g.Reasoning+"|") || !strings.Contains("|short|standard|detailed|", "|"+g.AnswerLength+"|") {
		return errors.New("invalid generation settings")
	}
	if g.Temperature != nil && (math.IsNaN(*g.Temperature) || math.IsInf(*g.Temperature, 0) || *g.Temperature < 0 || *g.Temperature > 2) {
		return errors.New("invalid temperature")
	}
	return nil
}

var commandPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,23}$`)

func (c Configuration) Validate() error {
	s := c.Settings
	if s.HourlyLimit < 1 || s.HourlyLimit > 10000 || s.DailyLimit < 1 || s.DailyLimit > 100000 || s.DailyBudget < 0 || s.DailyBudget > 10000 || math.IsNaN(s.DailyBudget) || math.IsInf(s.DailyBudget, 0) || s.MemoryHours < 1 || s.MemoryHours > 168 || s.RetentionDays < 1 || s.RetentionDays > 365 || s.MaxChars < 128 || s.MaxChars > 12000 || s.QuietStart < 0 || s.QuietStart > 23 || s.QuietEnd < 0 || s.QuietEnd > 23 || len(s.DefaultModel) > 200 || len(s.Favorites) > 100 || len(s.ModelGeneration) > 100 {
		return errors.New("invalid bot settings")
	}
	if _, err := time.LoadLocation(s.Timezone); err != nil {
		return errors.New("invalid timezone")
	}
	for _, g := range s.ModelGeneration {
		if err := validateGeneration(g); err != nil {
			return err
		}
	}
	if len(c.Commands) > 100 || len(c.Profiles) > 100 {
		return errors.New("too many commands or profiles")
	}
	profiles := map[string]bool{}
	for _, p := range c.Profiles {
		if p.ID == "" || len(p.ID) > 80 || profiles[p.ID] || p.Name == "" || len(p.Name) > 80 || len(p.Model) > 200 || len(p.Instructions) > 12000 {
			return errors.New("invalid profile")
		}
		profiles[p.ID] = true
	}
	names := map[string]bool{}
	for _, cmd := range c.Commands {
		if !commandPattern.MatchString(cmd.Name) || strings.Contains("|ping|help|status|new|more|deepseek|", "|"+cmd.Name+"|") || names[cmd.Name] || len(cmd.Description) > 200 || len(cmd.Instructions) > 12000 || (cmd.Type != "ai" && cmd.Type != "fixed") || len(cmd.Model) > 200 || len(cmd.AllowedConversations) > 100 || cmd.ProfileID != "" && !profiles[cmd.ProfileID] {
			return errors.New("invalid command")
		}
		if cmd.Generation != nil {
			if err := validateGeneration(*cmd.Generation); err != nil {
				return err
			}
		}
		names[cmd.Name] = true
	}
	return nil
}
func (e *Engine) Configuration() (Configuration, error) {
	var c Configuration
	b, err := e.Store.GetSecretSetting("bot_config", "config")
	if err != nil {
		return c, err
	}
	if len(b) == 0 {
		return DefaultConfiguration(), nil
	}
	if json.Unmarshal(b, &c) != nil {
		return c, errors.New("invalid saved configuration")
	}
	return c, c.Validate()
}
func (e *Engine) SetConfiguration(c Configuration) error {
	if err := c.Validate(); err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	old, err := e.Configuration()
	if err != nil {
		return err
	}
	if c.Settings.ModelGeneration == nil {
		c.Settings.ModelGeneration = map[string]Generation{}
	}
	// Seed preferences only when selecting a new default model. Persist the choice
	// explicitly, so legacy model-default preferences remain unchanged.
	if c.Settings.DefaultModel != "" && c.Settings.DefaultModel != old.Settings.DefaultModel {
		if _, exists := c.Settings.ModelGeneration[c.Settings.DefaultModel]; !exists {
			g := DefaultGeneration()
			if model, err := e.model(c.Settings.DefaultModel); err == nil && model.HasReasoning && (model.ReasoningEfforts == nil || contains(model.ReasoningEfforts, "low")) {
				g.Reasoning = "low"
			}
			c.Settings.ModelGeneration[c.Settings.DefaultModel] = g
		}
	}
	for _, previous := range old.Commands {
		if !previous.Memory {
			continue
		}
		consented := false
		for _, current := range c.Commands {
			if current.Name == previous.Name && current.Memory && current.Enabled && current.Type == previous.Type {
				consented = true
				break
			}
		}
		if !consented {
			if err = e.Store.InvalidateMemory(); err != nil {
				return err
			}
			e.cancelClearedThreadRuns()
			break
		}
	}
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}
	return e.Store.SetSecretSetting("bot_config", "config", b)
}
func quiet(s Settings, now time.Time) bool {
	if !s.QuietEnabled {
		return false
	}
	loc, err := time.LoadLocation(s.Timezone)
	if err != nil {
		return true
	}
	h := now.In(loc).Hour()
	if s.QuietStart == s.QuietEnd {
		return true
	}
	if s.QuietStart < s.QuietEnd {
		return h >= s.QuietStart && h < s.QuietEnd
	}
	return h >= s.QuietStart || h < s.QuietEnd
}
func allowed(cmd Command, conv string) bool {
	if len(cmd.AllowedConversations) == 0 {
		return true
	}
	for _, id := range cmd.AllowedConversations {
		if id == conv {
			return true
		}
	}
	return false
}
func resolve(c Configuration, name string) (Command, string, string, Generation, bool) {
	if name == "deepseek" {
		name = "ask"
	}
	for _, cmd := range c.Commands {
		if cmd.Name != name || !cmd.Enabled {
			continue
		}
		model := c.Settings.DefaultModel
		instructions := cmd.Instructions
		for _, p := range c.Profiles {
			if p.ID == cmd.ProfileID {
				if p.Model != "" {
					model = p.Model
				}
				instructions = p.Instructions + "\n" + instructions
			}
		}
		if cmd.Model != "" {
			model = cmd.Model
		}
		g := DefaultGeneration()
		if value, ok := c.Settings.ModelGeneration[model]; ok {
			g = value
		}
		if cmd.Generation != nil {
			g = *cmd.Generation
		}
		return cmd, model, instructions, g, true
	}
	return Command{}, "", "", Generation{}, false
}
func (e *Engine) ProviderConfigured() bool {
	b, err := e.Store.GetSecretSetting("provider_key", "provider-key")
	return err == nil && len(b) > 0
}
func (e *Engine) SetProviderKey(key string) error {
	key = strings.TrimSpace(key)
	if len(key) > 4096 || strings.ContainsAny(key, "\r\n") {
		return errors.New("invalid provider key")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.Store.SetSecretSetting("provider_key", "provider-key", []byte(key))
}

// Recipe exports contain no identities or provider secrets. Imports start disabled.
func CleanRecipe(c Configuration) Configuration {
	c.Commands = append([]Command{}, c.Commands...)
	for i := range c.Commands {
		c.Commands[i].AllowedConversations = []string{}
		c.Commands[i].Enabled = false
	}
	return c
}
func (e *Engine) ImportRecipe(c Configuration) error {
	for _, cmd := range c.Commands {
		if len(cmd.AllowedConversations) > 0 {
			return errors.New("recipes must not include sender identities")
		}
	}
	return e.SetConfiguration(CleanRecipe(c))
}
