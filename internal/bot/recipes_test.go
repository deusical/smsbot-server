package bot

import (
	"testing"
)

func TestAndroidCommandPackPreservesOverridesAndStartsDisabled(t *testing.T) {
	e, _ := setup(t)
	raw := []byte(`{"format":"smsbot.commands","version":1,"commands":[{"name":"sample","instructions":"Synthetic instructions","type":"AI","enabled":true,"model":"fixture/model","memory":true,"maxSegments":2,"approval":true,"allowedSenders":[],"profileId":"","generation":{"reasoning":"HIGH","answerLength":"DETAILED","tokens":4096,"temperature":null}}]}`)
	if err := e.ImportRecipeJSON(raw); err != nil {
		t.Fatal(err)
	}
	c, err := e.Configuration()
	if err != nil || len(c.Commands) != 1 || c.Commands[0].Enabled || c.Commands[0].Generation.Reasoning != "high" || c.Commands[0].Generation.Tokens != 4096 || c.Commands[0].Generation.AnswerLength != "detailed" || !c.Commands[0].Memory || !c.Commands[0].Approval {
		t.Fatal("legacy recipe overrides lost")
	}
}
func TestAndroidRecipeRejectsSecretsIdentitiesAndUnsupportedVersions(t *testing.T) {
	for _, raw := range []string{`{"format":"smsbot.commands","version":2,"commands":[]}`, `{"format":"smsbot.commands","version":1,"key":"SYNTHETIC_KEY","commands":[]}`, `{"format":"smsbot.commands","version":1,"commands":[{"name":"sample","type":"FIXED","instructions":"Synthetic","allowedSenders":["SYNTHETIC_ID"]}]}`} {
		e, _ := setup(t)
		if e.ImportRecipeJSON([]byte(raw)) == nil {
			t.Fatal("invalid migration accepted")
		}
	}
}
