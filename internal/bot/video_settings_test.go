package bot

import (
	"context"
	"encoding/json"
	"github.com/deusical/smsbot-server/internal/media"
	"github.com/deusical/smsbot-server/internal/store"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestVideoSettingsBounds(t *testing.T) {
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
func TestWorkerProtocolV2AndOutdatedFallback(t *testing.T) {
	for _, outdated := range []bool{false, true} {
		t.Run(map[bool]string{false: "v2", true: "outdated"}[outdated], func(t *testing.T) {
			warnings := []string{}
			posts := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer SYNTHETIC" {
					t.Error("missing auth")
				}
				if strings.HasSuffix(r.URL.Path, "/capabilities") {
					if outdated {
						w.WriteHeader(502) // The original async worker rejects unknown paths with 502.
						w.Write([]byte("private upstream detail"))
					} else {
						w.Write([]byte(`{"protocol":2}`))
					}
					return
				}
				if r.Method == "GET" {
					w.Header().Set("Content-Type", "video/mp4")
					w.Write([]byte("synthetic"))
					return
				}
				posts++
				var value map[string]json.RawMessage
				if json.NewDecoder(r.Body).Decode(&value) != nil {
					t.Error("bad body")
				}
				if outdated {
					if value["settings"] != nil || value["protocol"] != nil {
						t.Error("legacy worker given custom settings")
					}
				} else {
					var got VideoSettings
					json.Unmarshal(value["settings"], &got)
					if string(value["protocol"]) != "2" || got.Preset != "fast" || got.Passes != 1 || got.CopyIfFits {
						t.Error("v2 settings missing")
					}
				}
				w.Write([]byte(`{"stage":"complete"}`))
			}))
			defer server.Close()
			d := &HTTPDownloader{Endpoint: server.URL + "/download", Token: "SYNTHETIC", Warning: func(code string) { warnings = append(warnings, code) }}
			v := DefaultVideoSettings()
			v.Preset = "fast"
			v.Passes = 1
			v.CopyIfFits = false
			data, stage, err := d.PollDownloadSettings(context.Background(), strings.Repeat("a", 64), "https://douyin.com/synthetic", 100000, time.Now().Add(time.Minute), v)
			if err != nil || stage != "complete" || string(data) != "synthetic" || posts != 1 {
				t.Fatal("job did not proceed", stage, err)
			}
			if outdated {
				if len(warnings) != 1 || warnings[0] != "download_worker_outdated_using_defaults" {
					t.Fatal("unsafe/missing warning")
				}
			} else if len(warnings) != 0 {
				t.Fatal("spurious warning")
			}
		})
	}
}

func TestWorkerCapabilitiesBusyYieldsWithoutFallback(t *testing.T) {
	calls := 0
	warned := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if !strings.HasSuffix(r.URL.Path, "/capabilities") {
			t.Error("busy worker received a job")
		}
		w.WriteHeader(503)
	}))
	defer server.Close()
	d := &HTTPDownloader{Endpoint: server.URL, Warning: func(string) { warned = true }}
	data, stage, err := d.PollDownloadSettings(context.Background(), strings.Repeat("a", 64), "https://douyin.com/synthetic", 100000, time.Now().Add(time.Minute), DefaultVideoSettings())
	if err != nil || len(data) != 0 || stage != "waiting_worker" || warned || calls != 1 {
		t.Fatal("busy worker did not yield safely")
	}
}
func TestVideoSettingsDeadlineUsesAuthoritativeSettings(t *testing.T) {
	c := Command{ExecutionMinutes: 20, Settings: json.RawMessage(`{"execution_minutes":7}`)}
	if videoSettings(c).ExecutionMinutes != 7 {
		t.Fatal("flat execution value overrode settings")
	}
}

func videoStep(t *testing.T, p store.Plan) store.StepRun {
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
	return store.StepRun{}
}
