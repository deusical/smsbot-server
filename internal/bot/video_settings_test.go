package bot

import (
	"context"
	"encoding/json"
	"github.com/deusical/cmdflow"
	"github.com/deusical/smsbot-server/internal/media"
	"github.com/deusical/smsbot-server/internal/store"
	"testing"
	"time"
)

func TestVideoSettingsBounds(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{`{"preset":"shell"}`, `{"passes":0}`, `{"passes":3}`, `{"max_attempts":0}`, `{"max_attempts":4}`, `{"copy_if_fits":1}`, `{"max_mb":0}`, `{"max_mb":20}`, `{"execution_minutes":0}`, `{"execution_minutes":61}`, `{"preset":null}`, `{"encoder":"libx264"}`} {
		c := Command{Kind: "media.video", Settings: json.RawMessage(raw)}
		if normalizeCommand(&c) == nil {
			t.Fatalf("invalid setting accepted: %s", raw)
		}
	}
	c := Command{Type: "download"}
	if err := normalizeCommand(&c); err != nil {
		t.Fatal(err)
	}
	if got := videoSettings(c); got != DefaultVideoSettings() {
		t.Fatalf("defaults changed %+v", got)
	}
}
func TestVideoSettingsSnapshotAndEffectiveCap(t *testing.T) {
	t.Parallel()
	e, _ := setup(t)
	arm(t, e)
	host, err := e.Collection.Settings()
	if err != nil {
		t.Fatal(err)
	}
	host.AttachmentLimits = &media.Limits{MaxFiles: 4, MaxFileMiB: 3, MaxTotalMiB: 2}
	if err = e.Collection.SetSettings(context.Background(), host); err != nil {
		t.Fatal(err)
	}
	v := DefaultVideoSettings()
	v.Preset = "fast"
	v.ExecutionMinutes = 7
	raw, _ := json.Marshal(v)
	c := DefaultConfiguration()
	c.Commands = []Command{{Name: "download", Kind: "media.video", Settings: raw, Enabled: true}}
	if err = e.SetConfiguration(c); err != nil {
		t.Fatal(err)
	}
	m := incoming()
	m.Text = "!download https://v.douyin.com/synthetic/"
	if ok, err := e.Accept(context.Background(), m); !ok || err != nil {
		t.Fatal(ok, err)
	}
	id := store.Hash(m.ConversationID + "\x00" + m.ID)
	_, p, err := e.Store.JobPlan(id)
	if err != nil {
		t.Fatal(err)
	}
	var state compressState
	if err = json.Unmarshal(videoStep(t, p).State, &state); err != nil {
		t.Fatal(err)
	}
	snapshot := state.Settings
	if state.MaxBytes != 2<<20 || snapshot.MaxMB != float64(2<<20)/1e6 || snapshot.Preset != "fast" || snapshot.ExecutionMinutes != 7 {
		t.Fatalf("bad effective snapshot %+v", snapshot)
	}
	deadline, _ := time.Parse(time.RFC3339Nano, p.ExecutionDeadline)
	if delta := time.Until(deadline); delta < 6*time.Minute || delta > 8*time.Minute {
		t.Fatal("wrong deadline")
	}
	c, _ = e.Configuration()
	v.Preset = "veryfast"
	v.ExecutionMinutes = 1
	raw, _ = json.Marshal(v)
	c.Commands[0].Settings = raw
	if err = e.SetConfiguration(c); err != nil {
		t.Fatal(err)
	}
	_, after, _ := e.Store.JobPlan(id)
	if string(videoStep(t, after).State) != string(videoStep(t, p).State) || after.ExecutionDeadline != p.ExecutionDeadline {
		t.Fatal("mutable accepted settings")
	}
	defs, err := e.CommandKindDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	if *defs[2].Schema.Properties["max_mb"].EffectiveMaximum != float64(2<<20)/1e6 {
		t.Fatal("UI cap not exposed")
	}
}
func TestVideoSettingsDeadlineUsesAuthoritativeSettings(t *testing.T) {
	t.Parallel()
	c := Command{ExecutionMinutes: 20, Settings: json.RawMessage(`{"execution_minutes":7}`)}
	if videoSettings(c).ExecutionMinutes != 7 {
		t.Fatal("flat execution value overrode settings")
	}
}

func videoStep(t *testing.T, p store.Plan) cmdflow.StepRun {
	t.Helper()
	if p.Pipeline == nil {
		t.Fatal("no pipeline snapshot")
	}
	for _, s := range p.Pipeline.Steps {
		if s.Module == "video.compress" {
			return s
		}
	}
	t.Fatal("no video step")
	return cmdflow.StepRun{}
}
