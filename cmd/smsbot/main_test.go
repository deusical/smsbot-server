package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gofrs/flock"
	"smsbot-server/internal/vault"
)

func TestCookieUpdateCLI(t *testing.T) {
	root := t.TempDir()
	data, key := filepath.Join(root, "state"), filepath.Join(root, "master.key")
	if err := vault.Initialize(key); err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(data, 0700)
	v, err := vault.Open(key)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := v.Seal("google-session", []byte(`{"browser":{},"mobile":{},"tachyon_token":"Zml4dHVyZQ==","cookies":{}}`))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(data, "google-session.enc")
	if err := os.WriteFile(path, []byte(encoded), 0600); err != nil {
		t.Fatal(err)
	}
	args := []string{"--data", data, "--key", key, "--update-cookies", "--cookies-stdin"}
	input := `{"SID":"fixture","HSID":"fixture","SSID":"fixture","OSID":"fixture","APISID":"fixture","SAPISID":"fixture"}`
	lock := flock.New(filepath.Join(data, "service.lock"))
	if ok, err := lock.TryLock(); err != nil || !ok {
		t.Fatal("cannot acquire test lock")
	}
	err = runWithInput(args, strings.NewReader(input))
	lock.Unlock()
	if err == nil || err.Error() != "state directory is already in use" {
		t.Fatal("running service did not block cookie update")
	}
	current, _ := os.ReadFile(path)
	if string(current) != encoded {
		t.Fatal("locked update changed session")
	}
	if err := runWithInput(args, strings.NewReader(strings.Repeat("x", 65537))); err == nil {
		t.Fatal("oversized cookie input accepted")
	}
	current, _ = os.ReadFile(path)
	if string(current) != encoded {
		t.Fatal("oversized input changed session")
	}
	if err := runWithInput(args, strings.NewReader(input)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(data, "smsbot.db")); !os.IsNotExist(err) {
		t.Fatal("offline update touched application database")
	}
}

func TestCookieUpdateModeValidation(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"--update-cookies"}, "require --cookies-stdin"},
		{[]string{"--cookies-stdin"}, "requires --pair or --update-cookies"},
		{[]string{"--update-cookies", "--cookies-stdin", "--pair"}, "mutually exclusive"},
		{[]string{"--update-cookies", "--cookies-stdin", "--fixture"}, "mutually exclusive"},
		{[]string{"--update-cookies", "--cookies-stdin", "--init"}, "mutually exclusive"},
	} {
		if err := runWithInput(tc.args, strings.NewReader("")); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatal("invalid mode combination did not fail before opening state")
		}
	}
}

func TestVersionFlagPrintsWithoutState(t *testing.T) {
	if err := runWithInput([]string{"--version"}, strings.NewReader("")); err != nil {
		t.Fatal(err)
	}
}
