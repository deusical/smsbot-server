package store

import (
	"encoding/json"

	"github.com/deusical/smsbot-server/internal/media"
)

// PipelineRun is the immutable recipe an accepted job runs, plus the progress
// of each step. It lives inside the encrypted plan: editing the command later
// never changes it. Only the reply step's output reaches the dispatcher, which
// alone decides whether to send.
type PipelineRun struct {
	Steps []StepRun `json:"steps"`
	// Halted ends the pipeline early with a fixed reply already in the plan,
	// for example "Include one video link after the command."
	Halted bool `json:"halted,omitempty"`
}

// StepRun is one step's snapshot and state. Inputs map a port to its binding
// ("trigger.args" or "<step>.<output>"); Settings are the effective, validated
// settings at acceptance.
type StepRun struct {
	ID       string            `json:"id"`
	Module   string            `json:"module"`
	Version  int               `json:"version"`
	Inputs   map[string]string `json:"inputs,omitempty"`
	Settings json.RawMessage   `json:"settings,omitempty"`
	Done     bool              `json:"done,omitempty"`
	Outputs  map[string]Value  `json:"outputs,omitempty"`
	// State is module-private progress, such as the prompt an AI step built at
	// acceptance. It is part of the encrypted plan.
	State json.RawMessage `json:"state,omitempty"`
}

// Value is one typed value passed between steps. Large file bytes travel only
// as the produced media.File the dispatcher later uploads.
type Value struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
	// Handle names a worker-held file (a stored video): a job-scoped worker
	// job ID, never a path.
	Handle  string           `json:"handle,omitempty"`
	File    *FileRef         `json:"file,omitempty"`
	Media   *media.File      `json:"media,omitempty"`
	Message *MessageRef      `json:"message,omitempty"`
	Context *ConversationRef `json:"context,omitempty"`
}

// MessageRef names the command message (or the message it quotes) in the job's
// own conversation. Content is read through the conversation-scoped store.
type MessageRef struct {
	ID string `json:"id"`
}

// ConversationRef is an AI conversation handle. Kind is "none", "rolling"
// (a command's recent exchanges) or "thread" (the reply chain of an AI answer).
// Whose conversation it is follows the plan: the asker, or the whole group when
// the group shares one.
type ConversationRef struct {
	Kind string `json:"kind"`
}
