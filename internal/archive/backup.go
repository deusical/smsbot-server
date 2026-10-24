package archive

// Message archive transfer format: the export scope, progress counts, job
// status and the manifest, row and blob records shared by export and restore.
// It is distinct from the store's operational backup (store.ExportBackup).

import (
	"errors"
	"time"

	"github.com/deusical/smsbot-server/internal/media"
	"github.com/deusical/smsbot-server/internal/store"
)

type Scope struct {
	Plaintext     bool     `json:"plaintext,omitempty"`
	Conversations []string `json:"conversations,omitempty"`
	Since         string   `json:"since,omitempty"`
}

func (s Scope) Validate() error {
	if len(s.Conversations) > 500 {
		return errors.New("invalid_backup_scope")
	}
	if s.Since != "" {
		_, err := time.Parse(time.RFC3339, s.Since)
		return err
	}
	return nil
}

type Counts struct {
	Messages     int64 `json:"messages"`
	SavedFiles   int64 `json:"saved_files"`
	MissingFiles int64 `json:"missing_files"`
	Bytes        int64 `json:"bytes"`
}
type Job struct {
	ID     string `json:"id"`
	State  string `json:"state"`
	Error  string `json:"error,omitempty"`
	Scope  Scope  `json:"scope"`
	Counts Counts `json:"counts"`
}
type manifest struct {
	Version int    `json:"version"`
	Scope   Scope  `json:"scope"`
	Counts  Counts `json:"counts"`
	Digest  string `json:"digest"`
}
type archiveRow struct {
	Interaction  string         `json:"interaction,omitempty"`
	Kind         string         `json:"kind"`
	ID           string         `json:"id,omitempty"`
	Conversation string         `json:"conversation,omitempty"`
	Direction    string         `json:"direction,omitempty"`
	At           string         `json:"at,omitempty"`
	Payload      string         `json:"payload,omitempty"`
	Attachments  string         `json:"attachments,omitempty"`
	Origin       string         `json:"origin,omitempty"`
	Status       string         `json:"status,omitempty"`
	Private      bool           `json:"private"`
	Message      string         `json:"message,omitempty"`
	Alias        string         `json:"alias,omitempty"`
	Revision     int            `json:"revision,omitempty"`
	Observed     string         `json:"observed,omitempty"`
	Archive      *store.Archive `json:"archive,omitempty"`
	Retention    int            `json:"retention,omitempty"`
}
type blobEntry struct {
	file media.File
	path string
}
