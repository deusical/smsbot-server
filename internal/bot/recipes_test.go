package bot

import "testing"

func TestRecipeImportRejectsForeignFormatsSecretsAndUnknownFields(t *testing.T) {
	for _, raw := range []string{
		`{"format":"smsbot.commands","version":1,"commands":[]}`,
		`{"commands":[],"key":"SYNTHETIC_KEY"}`,
		`not json`,
	} {
		e, _ := setup(t)
		if e.ImportRecipeJSON([]byte(raw)) == nil {
			t.Fatalf("invalid recipe accepted: %s", raw)
		}
	}
}
