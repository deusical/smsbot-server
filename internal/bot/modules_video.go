package bot

import (
	"encoding/json"
	"errors"
	"math"

	"github.com/deusical/smsbot-server/internal/store"
)

func init() {
	registerModule(videoDownloadModule())
}

// videoStepSettings are video.download's settings. The execution deadline is
// the pipeline's, not the step's.
type videoStepSettings struct {
	Preset      string  `json:"preset"`
	Passes      int     `json:"passes"`
	MaxAttempts int     `json:"max_attempts"`
	CopyIfFits  bool    `json:"copy_if_fits"`
	MaxMB       float64 `json:"max_mb"`
}

func defaultVideoStep() videoStepSettings {
	v := DefaultVideoSettings()
	return videoStepSettings{v.Preset, v.Passes, v.MaxAttempts, v.CopyIfFits, v.MaxMB}
}

func (v videoStepSettings) full(minutes int) VideoSettings {
	if minutes == 0 {
		minutes = 15
	}
	return VideoSettings{v.Preset, v.Passes, v.MaxAttempts, v.CopyIfFits, v.MaxMB, minutes}
}

const videoPreview = "Video link recognized. A live command saves the original on the server and sends a video under 20 MB. No download or send was performed by this preview."

// video.download asks the server-local worker to download the linked video and
// compress it to a sendable size. The worker keeps the original.
func videoDownloadModule() module {
	return module{
		ModuleDefinition: ModuleDefinition{
			ID: "video.download", Version: 1, Title: "Download video", Description: "Downloads the linked video on the server and compresses it to a size that can be sent.",
			Inputs:  []Port{{Name: "url", Title: "Link", Types: []string{typeURL}, Required: true}},
			Outputs: []Port{{Name: "video", Title: "Video", Types: []string{typeVideo}}},
			Effect:  effectLocal, RetryClass: "idempotent-poll", ResourceClass: "media", LongRunning: true,
			Schema: SettingsSchema{Type: "object", UI: "video-cost", Properties: map[string]SettingsSchema{
				"preset":       {Type: "string", Title: "Encoding preset", Default: "slow", Enum: []string{"veryfast", "faster", "fast", "medium", "slow"}},
				"passes":       {Type: "number", Title: "Passes", Default: 2, Minimum: bound(1), Maximum: bound(2)},
				"max_attempts": {Type: "number", Title: "Maximum attempts", Default: 3, Minimum: bound(1), Maximum: bound(3)},
				"copy_if_fits": {Type: "boolean", Title: "Copy original when it fits", Default: true},
				"max_mb":       {Type: "number", Title: "Maximum video size (MB)", Default: 19.99, Minimum: bound(0.1), Maximum: bound(19.99), Unit: "MB"},
			}},
		},
		settings: func(raw json.RawMessage, _ pipelineEnv) (json.RawMessage, error) {
			v := defaultVideoStep()
			if err := strictSettings(raw, &v); err != nil {
				return nil, err
			}
			if err := v.full(15).Validate(); err != nil {
				return nil, errors.New("invalid video settings")
			}
			return json.Marshal(v)
		},
		accept: func(e *Engine, a *acceptRun, s *stepRun) (acceptOutcome, error) {
			if err := prepareVideoState(e, a.pipeline.ExecutionMinutes, s); err != nil {
				return stepDecline, err
			}
			return stepPending, nil
		},
		run: func(e *Engine, r *execRun, s *stepRun) (bool, error) {
			var st videoState
			_ = s.load(&st)
			if st.URL == "" {
				// The link came from a step that ran after acceptance.
				if err := prepareVideoState(e, st.Settings.ExecutionMinutes, s); err != nil {
					return true, err
				}
				if err := e.Store.SavePlan(r.j.ID, *r.p); err != nil {
					return true, err
				}
			}
			return e.runVideoDownload(r.ctx, r.j, r.p, s)
		},
		simulate: func(_ *simRun, s *stepRun) error {
			s.out("video", store.Value{Type: typeVideo, Text: videoPreview})
			return nil
		},
	}
}

// prepareVideoState fixes the step's effective settings: the configured size
// ceiling, lowered to the server's attachment limits.
func prepareVideoState(e *Engine, minutes int, s *stepRun) error {
	v := defaultVideoStep()
	if err := s.decode(&v); err != nil {
		return err
	}
	st := videoState{Settings: v.full(minutes)}
	if u, ok := s.in("url"); ok {
		st.URL = u.Text
		host, err := e.Collection.Settings()
		if err != nil {
			return err
		}
		st.MaxBytes = min(videoHostLimit(host.FileLimits()), int64(math.Round(v.MaxMB*1e6)))
		st.Settings.MaxMB = float64(st.MaxBytes) / 1e6
	}
	s.save(st)
	return nil
}
