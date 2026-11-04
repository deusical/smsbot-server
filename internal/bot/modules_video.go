package bot

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"time"

	"github.com/deusical/cmdflow"
	"github.com/deusical/smsbot-server/internal/store"
)

func init() {
	registerModule(videoDownloadModule())
	registerModule(videoCompressModule())
}

// videoStepSettings are video.compress's settings. The execution deadline is
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

// workerJob is the worker job ID for one step of a bot job. The bot job ID is
// the scope that ties a compress job to its own job's original.
func workerJob(j store.Job, step string) string { return store.Hash(j.ID + "\x00" + step) }

// sourceState is what a video.download step fixed at acceptance.
type sourceState struct {
	URL string `json:"url"`
}

// video.download asks the server-local worker to download the linked video.
// The original stays on the worker, encrypted; the step outputs a reference
// to it that only this job's later steps can use.
func videoDownloadModule() module {
	return module{
		Definition: ModuleDefinition{
			ID: "video.download", Version: 2, Title: "Download Douyin/Xiaohongshu video", Description: "Downloads the linked Douyin or Xiaohongshu video on the server. The original stays on the server.",
			Inputs:  []Port{{Name: "url", Title: "Link", Types: []string{typeURL}, Required: true}},
			Outputs: []Port{{Name: "source", Title: "Original video", Types: []string{typeStoredVideo}}},
			Effect:  effectLocal, RetryClass: "idempotent-poll", ResourceClass: "media", LongRunning: true,
			Schema: SettingsSchema{Type: "object", Properties: map[string]SettingsSchema{}},
		},
		Settings: noSettings,
		Accept: func(_ *runCtx, s *cmdflow.Step) (cmdflow.Outcome, error) {
			if u, ok := s.In("url"); ok {
				if err := s.Save(sourceState{URL: u.Text}); err != nil {
					return cmdflow.Outcome{}, err
				}
			}
			return cmdflow.Pending(), nil
		},
		Run: func(r *runCtx, s *cmdflow.Step) (cmdflow.Outcome, error) {
			e := r.e
			var st sourceState
			_ = s.Load(&st)
			if st.URL == "" {
				st.URL = s.Text("url")
				if err := s.Save(st); err != nil {
					return cmdflow.Outcome{}, err
				}
			}
			if st.URL == "" {
				return cmdflow.Fail("download_failed"), nil
			}
			worker, separate := e.Downloader.(videoWorkerV3)
			combined := !separate
			complete, err := e.pollWorker(r.ctx, r.j, r.p, func(ctx context.Context, deadline time.Time) (string, error) {
				if combined {
					return "complete", nil
				}
				stage, err := worker.PollSource(ctx, workerJob(r.j, s.ID), r.j.ID, st.URL, deadline)
				if errors.Is(err, errWorkerV3Unavailable) {
					combined = true
					return "complete", nil
				}
				return stage, err
			})
			if !complete || err != nil {
				return cmdflow.Yield(), err
			}
			if combined {
				// The compress step downloads and compresses in one older job.
				s.Out("source", cmdflow.Value{Type: typeStoredVideo, Text: st.URL})
			} else {
				s.Out("source", cmdflow.Value{Type: typeStoredVideo, Handle: workerJob(r.j, s.ID)})
			}
			s.Status = cmdflow.StatusDone
			return yielded(e.finishWorkerStep(r.j, r.p))
		},
		Simulate: func(_ *runCtx, s *cmdflow.Step) (cmdflow.Outcome, error) {
			s.Out("source", cmdflow.Value{Type: typeStoredVideo, Text: "(original kept on the server)"})
			return cmdflow.Done(), nil
		},
	}
}

// compressState is what a video.compress step fixed at acceptance.
type compressState struct {
	MaxBytes int64         `json:"max_bytes"`
	Settings VideoSettings `json:"settings"`
}

// video.compress makes a copy of an original video small enough to send.
func videoCompressModule() module {
	return module{
		Definition: ModuleDefinition{
			ID: "video.compress", Version: 1, Title: "Compress video", Description: "Makes a copy of the original video that is small enough to send.",
			Inputs:  []Port{{Name: "source", Title: "Original video", Types: []string{typeStoredVideo}, Required: true}},
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
		Settings: func(raw json.RawMessage, _ any) (json.RawMessage, error) {
			v := defaultVideoStep()
			if err := strictSettings(raw, &v); err != nil {
				return nil, err
			}
			if err := v.full(15).Validate(); err != nil {
				return nil, errors.New("invalid video settings")
			}
			return json.Marshal(v)
		},
		Accept: func(a *runCtx, s *cmdflow.Step) (cmdflow.Outcome, error) {
			return cmdflow.Pending(), prepareCompressState(a.e, a.pipeline.ExecutionMinutes, s)
		},
		Run: func(r *runCtx, s *cmdflow.Step) (cmdflow.Outcome, error) {
			e := r.e
			var st compressState
			if err := s.Load(&st); err != nil || st.MaxBytes == 0 {
				return cmdflow.Fail("download_failed"), nil
			}
			host, err := e.Collection.Settings()
			if err != nil {
				return cmdflow.Yield(), err
			}
			if st.MaxBytes > videoHostLimit(host.FileLimits()) {
				return yielded(true, e.Store.Complete(r.j.ID, "cancelled", "attachment_limits_changed"))
			}
			source, ok := s.In("source")
			if !ok || source.Handle == "" && source.Text == "" {
				return cmdflow.Fail("download_failed"), nil
			}
			var data []byte
			complete, err := e.pollWorker(r.ctx, r.j, r.p, func(ctx context.Context, deadline time.Time) (string, error) {
				var stage string
				var err error
				if source.Handle != "" {
					worker, separate := e.Downloader.(videoWorkerV3)
					if !separate {
						return "", errors.New("download_failed")
					}
					data, stage, err = worker.PollCompress(ctx, workerJob(r.j, s.ID), r.j.ID, source.Handle, st.MaxBytes, deadline, st.Settings)
				} else if v2, ok := e.Downloader.(interface {
					PollDownloadSettings(context.Context, string, string, int64, time.Time, VideoSettings) ([]byte, string, error)
				}); ok {
					data, stage, err = v2.PollDownloadSettings(ctx, r.j.ID, source.Text, st.MaxBytes, deadline, st.Settings)
				} else {
					data, stage, err = e.Downloader.PollDownload(ctx, r.j.ID, source.Text, st.MaxBytes, deadline)
				}
				return stage, err
			})
			if !complete || err != nil {
				return cmdflow.Yield(), err
			}
			file, done, err := e.sendableVideo(r.ctx, r.j, r.p, data, st.MaxBytes)
			if done || err != nil {
				return cmdflow.Yield(), err
			}
			s.Out("video", mediaValue(file))
			s.Status = cmdflow.StatusDone
			return yielded(e.finishWorkerStep(r.j, r.p))
		},
		Simulate: func(_ *runCtx, s *cmdflow.Step) (cmdflow.Outcome, error) {
			s.Out("video", cmdflow.Value{Type: typeVideo, Text: videoPreview})
			return cmdflow.Done(), nil
		},
	}
}

// prepareCompressState fixes the step's effective settings: the configured
// size ceiling, lowered to the server's attachment limits.
func prepareCompressState(e *Engine, minutes int, s *cmdflow.Step) error {
	v := defaultVideoStep()
	if err := s.Decode(&v); err != nil {
		return err
	}
	st := compressState{Settings: v.full(minutes)}
	host, err := e.Collection.Settings()
	if err != nil {
		return err
	}
	st.MaxBytes = min(videoHostLimit(host.FileLimits()), int64(math.Round(v.MaxMB*1e6)))
	st.Settings.MaxMB = float64(st.MaxBytes) / 1e6
	return s.Save(st)
}

// upgradeVideoSteps rewrites a saved video.download@1 step, which downloaded
// and compressed together, as video.download@2 plus video.compress@1. The
// compress step keeps the old step's id and settings, so bindings to
// "<id>.video" still work. An unpinned step is the old kind when it has
// compression settings or a later step reads its "video" output.
func upgradeVideoSteps(p *Pipeline) {
	steps := []PipelineStep{}
	for i, s := range p.Steps {
		if s.Module != "video.download" || s.Version != 1 && (s.Version != 0 || !combinedVideoStep(p.Steps[i+1:], s)) {
			steps = append(steps, s)
			continue
		}
		// Shorten the id itself, so the new id still starts with a letter.
		base := s.ID
		if len(base) > 24-len("_source") {
			base = base[:24-len("_source")]
		}
		source := base + "_source"
		steps = append(steps,
			PipelineStep{ID: source, Module: "video.download", Version: 2, Inputs: s.Inputs},
			PipelineStep{ID: s.ID, Module: "video.compress", Version: 1, Inputs: map[string]string{"source": source + ".source"}, Settings: s.Settings})
	}
	p.Steps = steps
}

func combinedVideoStep(later []PipelineStep, s PipelineStep) bool {
	var settings map[string]any
	if json.Unmarshal(s.Settings, &settings) == nil && len(settings) > 0 {
		return true
	}
	for _, l := range later {
		for _, binding := range l.Inputs {
			if binding == s.ID+".video" {
				return true
			}
		}
	}
	return false
}
