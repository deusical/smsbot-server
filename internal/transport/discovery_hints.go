package transport

// Discovery hints survive restarts in an encrypted file next to the session,
// so the first discovery pass after a restart reuses them instead of reading
// every conversation's details from the phone again. Like the in-memory
// hints, they only list conversations and never authorize phone operations.

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"

	"go.mau.fi/mautrix-gmessages/pkg/libgm/gmproto"
	"google.golang.org/protobuf/proto"
)

const hintsScope = "discovery-hints"

var errHintsUnreadable = errors.New("hints_unreadable")

type savedHints struct {
	Version int               `json:"version"`
	Details map[string]string `json:"details"`
}

func (g *Google) hintsPath() string { return g.path + ".hints" }

// loadHints restores saved hints. A missing or unreadable file only means the
// first pass reads details from the phone, as before.
func (g *Google) loadHints() {
	encoded, err := os.ReadFile(g.hintsPath())
	if err != nil {
		return
	}
	plain, err := g.vault.Open(hintsScope, string(encoded))
	var saved savedHints
	if err != nil || json.Unmarshal(plain, &saved) != nil || saved.Version != 1 {
		g.diagnose("discovery_hints_unreadable", errHintsUnreadable)
		return
	}
	details := make(map[string]*gmproto.Conversation, len(saved.Details))
	for id, b64 := range saved.Details {
		b, err := base64.RawStdEncoding.DecodeString(b64)
		c := &gmproto.Conversation{}
		if err != nil || proto.Unmarshal(b, c) != nil || c.GetConversationID() != id {
			continue
		}
		details[id] = c
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	for id, c := range g.discoveryDetails {
		details[id] = c // anything learned since startup is newer
	}
	g.discoveryDetails = details
}

// saveHints writes the hints if they changed since the last save.
func (g *Google) saveHints() error {
	if !g.hintsDirty.Swap(false) {
		return nil
	}
	g.mu.Lock()
	saved := savedHints{Version: 1, Details: make(map[string]string, len(g.discoveryDetails))}
	for id, c := range g.discoveryDetails {
		if b, err := proto.Marshal(c); err == nil {
			saved.Details[id] = base64.RawStdEncoding.EncodeToString(b)
		}
	}
	g.mu.Unlock()
	err := g.writeHints(saved)
	if err != nil {
		g.hintsDirty.Store(true)
	}
	return err
}

func (g *Google) writeHints(saved savedHints) error {
	g.saveMu.Lock()
	defer g.saveMu.Unlock()
	plain, err := json.Marshal(saved)
	if err != nil {
		return err
	}
	encoded, err := g.vault.Seal(hintsScope, plain)
	if err != nil {
		return err
	}
	if err = os.WriteFile(g.hintsPath()+".tmp", []byte(encoded), 0600); err != nil {
		return err
	}
	return os.Rename(g.hintsPath()+".tmp", g.hintsPath())
}
