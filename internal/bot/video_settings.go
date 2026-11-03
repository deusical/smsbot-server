package bot

import (
	"encoding/json"
	"errors"
	"github.com/deusical/smsbot-server/internal/media"
	"math"
)

// VideoSettings contains only bounded policy choices, never process arguments.
type VideoSettings struct {
	Preset           string  `json:"preset"`
	Passes           int     `json:"passes"`
	MaxAttempts      int     `json:"max_attempts"`
	CopyIfFits       bool    `json:"copy_if_fits"`
	MaxMB            float64 `json:"max_mb"`
	ExecutionMinutes int     `json:"execution_minutes"`
}

func DefaultVideoSettings() VideoSettings { return VideoSettings{"slow", 2, 3, true, 19.99, 15} }
func (v VideoSettings) Validate() error {
	if !contains([]string{"veryfast", "faster", "fast", "medium", "slow"}, v.Preset) || v.Passes < 1 || v.Passes > 2 || v.MaxAttempts < 1 || v.MaxAttempts > 3 || math.IsNaN(v.MaxMB) || math.IsInf(v.MaxMB, 0) || v.MaxMB < 0.1 || v.MaxMB > 19.99 || v.ExecutionMinutes < 1 || v.ExecutionMinutes > 60 {
		return errors.New("invalid video settings")
	}
	return nil
}
func videoSettings(c Command) VideoSettings {
	v := DefaultVideoSettings()
	_ = json.Unmarshal(c.Settings, &v)
	if c.ExecutionMinutes > 0 {
		v.ExecutionMinutes = c.ExecutionMinutes
	}
	return v
}
func videoHostLimit(l media.Limits) int64 {
	limit := downloadLimit
	if l.MaxFileMiB > 0 {
		limit = min(limit, l.FileBytes())
	}
	if l.MaxTotalMiB > 0 {
		limit = min(limit, l.MaxTotalMiB<<20)
	}
	return limit
}
func (e *Engine) CommandKindDefinitions() ([]KindDefinition, error) {
	settings, err := e.Collection.Settings()
	if err != nil {
		return nil, err
	}
	defs := KindDefinitions()
	for i := range defs {
		if defs[i].ID == "media.video" {
			// Definitions are copied before applying host-specific effective bounds.
			defs[i].Schema = kindSchema(defs[i].ID)
			field := defs[i].Schema.Properties["max_mb"]
			field.EffectiveMaximum = bound(min(19.99, float64(videoHostLimit(settings.FileLimits()))/1e6))
			defs[i].Schema.Properties["max_mb"] = field
		}
	}
	return defs, nil
}
