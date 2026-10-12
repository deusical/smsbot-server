package bot

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
)

func strictRecipe(raw []byte, out any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(out) != nil || d.Decode(new(any)) != io.EOF {
		return errors.New("invalid_recipe")
	}
	return nil
}

// ImportRecipeJSON accepts server configuration recipes or Android's existing
// exported command pack. Neither format carries keys or enrolled identities.
func (e *Engine) ImportRecipeJSON(raw []byte) error {
	var probe struct {
		Format string `json:"format"`
	}
	if json.Unmarshal(raw, &probe) != nil {
		return errors.New("invalid_recipe")
	}
	if probe.Format == "" {
		var c Configuration
		if err := strictRecipe(raw, &c); err != nil {
			return err
		}
		return e.ImportRecipe(c)
	}
	if len(raw) > 128000 {
		return errors.New("legacy_recipe_too_large")
	}
	var pack struct {
		Format   string `json:"format"`
		Version  int    `json:"version"`
		Commands []struct {
			Name           string   `json:"name"`
			Description    string   `json:"description"`
			Instructions   string   `json:"instructions"`
			Type           string   `json:"type"`
			Enabled        bool     `json:"enabled"`
			Model          string   `json:"model"`
			Memory         bool     `json:"memory"`
			MaxSegments    int      `json:"maxSegments"`
			Approval       bool     `json:"approval"`
			AllowedSenders []string `json:"allowedSenders"`
			ProfileID      string   `json:"profileId"`
			Generation     *struct {
				Reasoning    string   `json:"reasoning"`
				AnswerLength string   `json:"answerLength"`
				Tokens       int      `json:"tokens"`
				Temperature  *float64 `json:"temperature"`
			} `json:"generation"`
		} `json:"commands"`
	}
	if err := strictRecipe(raw, &pack); err != nil {
		return err
	}
	if pack.Format != "smsbot.commands" || pack.Version != 1 || len(pack.Commands) > 50 {
		return errors.New("unsupported_recipe")
	}
	c, err := e.Configuration()
	if err != nil {
		return err
	}
	names := map[string]bool{}
	for _, cmd := range c.Commands {
		names[cmd.Name] = true
	}
	for _, cmd := range pack.Commands {
		if len(cmd.AllowedSenders) > 0 || cmd.ProfileID != "" {
			return errors.New("recipes_must_not_include_sender_or_unresolved_profile")
		}
		name := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(cmd.Name)), "!")
		if names[name] {
			return errors.New("duplicate_recipe_command")
		}
		names[name] = true
		command := Command{Name: name, Description: cmd.Description, Instructions: cmd.Instructions, Type: strings.ToLower(cmd.Type), Enabled: false, Model: cmd.Model, Memory: cmd.Memory, Approval: cmd.Approval, AllowedConversations: []string{}}
		if cmd.Generation != nil {
			g := DefaultGeneration()
			if cmd.Generation.Reasoning != "" {
				g.Reasoning = strings.ToLower(cmd.Generation.Reasoning)
			}
			if cmd.Generation.AnswerLength != "" {
				g.AnswerLength = strings.ToLower(cmd.Generation.AnswerLength)
			}
			if cmd.Generation.Tokens != 0 {
				g.Tokens = cmd.Generation.Tokens
			}
			g.Temperature = cmd.Generation.Temperature
			command.Generation = &g
		}
		c.Commands = append(c.Commands, command)
	}
	// Preserve the existing server enrollment restrictions; only imported commands
	// have had identity fields rejected above and are disabled.
	return e.SetConfiguration(c)
}
