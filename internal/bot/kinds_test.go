package bot

import (
	"encoding/json"
	"os"
	"testing"
)

func TestLegacyKindRoundTrip(t *testing.T) {
	for _, typ := range []string{"fixed", "ai", "download"} {
		c := Command{Type: typ, Instructions: "hello {input}", Memory: true, ExecutionMinutes: 15}
		if err := normalizeCommand(&c); err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(c)
		if err != nil {
			t.Fatal(err)
		}
		var loaded Command
		if err = json.Unmarshal(raw, &loaded); err != nil {
			t.Fatal(err)
		}
		if err = normalizeCommand(&loaded); err != nil {
			t.Fatal(err)
		}
		if loaded.Type != typ || loaded.Kind != commandKind(c) || loaded.Instructions != c.Instructions || !loaded.Memory {
			t.Fatalf("lost legacy settings for %s", typ)
		}
	}
}

func TestKindSettingsRejectUnknownFields(t *testing.T) {
	c := Command{Kind: "media.video", Settings: json.RawMessage(`{"encoder":"unsafe"}`)}
	if normalizeCommand(&c) == nil {
		t.Fatal("unknown settings accepted")
	}
}

func TestThreadCapability(t *testing.T) {
	if !kindThreads(Command{Type: "ai"}) || !kindThreads(Command{Kind: "ai.generate"}) || kindThreads(Command{Type: "fixed"}) || kindThreads(Command{Type: "download"}) {
		t.Fatal("unexpected thread capability")
	}
}

func TestLegacyRecipeFixture(t *testing.T) {
	e, _ := setup(t)
	raw, err := os.ReadFile("testdata/legacy-recipe.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = e.ImportRecipeJSON(raw); err != nil {
		t.Fatal(err)
	}
	c, err := e.Configuration()
	if err != nil {
		t.Fatal(err)
	}
	if c.Commands[0].Kind != "fixed.reply" || c.Commands[0].Enabled {
		t.Fatal("legacy import changed semantics")
	}
}
func TestRepresentationPreservesMemoryAndLegacyEdits(t *testing.T) {
	e, _ := setup(t)
	configureAI(t, e, true, false)
	before, err := e.Store.MemoryEpoch("c1", "")
	if err != nil {
		t.Fatal(err)
	}
	c, err := e.Configuration()
	if err != nil {
		t.Fatal(err)
	}
	if err = e.SetConfiguration(c); err != nil {
		t.Fatal(err)
	}
	after, _ := e.Store.MemoryEpoch("c1", "")
	if before != after {
		t.Fatal("representation invalidated memory")
	}
	c.Commands[0].Memory = false
	if err = e.SetConfiguration(c); err != nil {
		t.Fatal(err)
	}
	loaded, _ := e.Configuration()
	if loaded.Commands[0].Memory {
		t.Fatal("legacy edit lost")
	}
}
func TestKindPreviewsUnchanged(t *testing.T) {
	e, _ := setup(t)
	c := DefaultConfiguration()
	c.Settings.DefaultModel = "fixture/model"
	for _, tc := range []struct{ typ, input, want, model string }{
		{"fixed", "world", "Hello world", "fixture/model"},
		{"ai", "question", "Simulated AI response. No provider request was made.", "fixture/model"},
		{"download", "", "Include one Douyin or Xiaohongshu video link after the command.", "Local"},
		{"download", "https://douyin.com/video/1", "Video link recognized. A live command saves the original on the server and sends a video under 20 MB. No download or send was performed by this preview.", "Local"},
	} {
		c.Commands = []Command{{Name: "sample", Type: tc.typ, Instructions: "Hello {input}"}}
		if err := e.SetConfiguration(c); err != nil {
			t.Fatal(err)
		}
		r, err := e.Playground("sample", tc.input)
		if err != nil {
			t.Fatal(err)
		}
		if r["text"] != tc.want || r["model"] != tc.model {
			t.Fatalf("preview changed: %v", r)
		}
	}
}

func TestLegacyRenamedAndDuplicatedEdits(t *testing.T) {
	e, _ := setup(t)
	c := DefaultConfiguration()
	c.Commands = []Command{{Name: "hello", Type: "fixed", Instructions: "Hello {input}"}}
	if err := e.SetConfiguration(c); err != nil {
		t.Fatal(err)
	}
	c, _ = e.Configuration()
	duplicate := c.Commands[0]
	duplicate.Name = "copy"
	duplicate.Instructions = "Goodbye {input}"
	duplicate.Settings = json.RawMessage(`{ "instructions" : "Hello {input}" }`)
	c.Commands[0].Name = "renamed"
	c.Commands[0].Instructions = "Changed {input}"
	c.Commands = append(c.Commands, duplicate)
	if err := e.SetConfiguration(c); err != nil {
		t.Fatal(err)
	}
	c, _ = e.Configuration()
	if c.Commands[0].Instructions != "Changed {input}" || c.Commands[1].Instructions != "Goodbye {input}" {
		t.Fatal("legacy edits lost")
	}
}
func TestKindDefinitions(t *testing.T) {
	defs := KindDefinitions()
	if len(defs) != 3 {
		t.Fatal("missing kinds")
	}
	for _, d := range defs {
		if d.ID == "" || d.Version != 1 || d.RetryClass == "" || d.ResourceClass == "" || len(d.Schema.Properties) == 0 {
			t.Fatalf("incomplete definition: %+v", d)
		}
	}
}
