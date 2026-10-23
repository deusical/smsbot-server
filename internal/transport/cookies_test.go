package transport

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/deusical/smsbot-server/internal/vault"
)

const syntheticCookies = `{"SID":"fixture-new","HSID":"fixture-new","SSID":"fixture-new","OSID":"fixture-new","APISID":"fixture-new","SAPISID":"fixture-new"}`
const syntheticSession = `{"browser":{"user_id":"fixture-browser"},"mobile":{"user_id":"fixture-mobile"},"tachyon_token":"Zml4dHVyZS10b2tlbg==","tachyon_expiry":"2030-01-01T00:00:00Z","request_crypto":{"aes_key":"Zml4dHVyZQ==","hmac_key":"Zml4dHVyZQ=="},"future_field":{"value":"fixture-opaque"},"cookies":{"SID":"fixture-old","__Secure-1PSIDTS":"fixture-old","server-cookie":"fixture-preserve"}}`

func cookieUpdateFixture(t *testing.T, plain string) (*Google, []byte) {
	t.Helper()
	v, err := vault.New(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	g := NewGoogle(v, filepath.Join(t.TempDir(), "google-session.enc"))
	encoded, err := v.Seal("google-session", []byte(plain))
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(g.path, []byte(encoded), 0600); err != nil {
		t.Fatal(err)
	}
	return g, []byte(encoded)
}

func TestUpdateCookiesPreservesPairingAndEncryptedEvidence(t *testing.T) {
	for _, optional := range []bool{false, true} {
		t.Run(map[bool]string{false: "optional-absent", true: "optional-present"}[optional], func(t *testing.T) {
			g, original := cookieUpdateFixture(t, syntheticSession)
			input := syntheticCookies
			if optional {
				input = strings.TrimSuffix(input, "}") + `,"__Secure-1PSIDTS":"fixture-new-ts"}`
			}
			if err := g.UpdateCookies(input); err != nil {
				t.Fatal(err)
			}
			encoded, err := os.ReadFile(g.path)
			if err != nil {
				t.Fatal(err)
			}
			plain, err := g.vault.Open("google-session", string(encoded))
			if err != nil {
				t.Fatal(err)
			}
			var before, after map[string]json.RawMessage
			json.Unmarshal([]byte(syntheticSession), &before)
			json.Unmarshal(plain, &after)
			var cookies map[string]string
			if err := json.Unmarshal(after["cookies"], &cookies); err != nil {
				t.Fatal(err)
			}
			delete(before, "cookies")
			delete(after, "cookies")
			if !reflect.DeepEqual(before, after) {
				t.Fatal("pairing or opaque session fields changed")
			}
			for _, name := range []string{"SID", "HSID", "SSID", "OSID", "APISID", "SAPISID"} {
				if cookies[name] != "fixture-new" {
					t.Fatal("imported cookie was not updated")
				}
			}
			if cookies["server-cookie"] != "fixture-preserve" || (optional && cookies["__Secure-1PSIDTS"] != "fixture-new-ts") || (!optional && cookies["__Secure-1PSIDTS"] != "") {
				t.Fatal("cookie preservation or optional removal failed")
			}
			backups, _ := filepath.Glob(g.path + ".before-cookie-update-*.enc")
			if len(backups) != 1 {
				t.Fatal("expected one encrypted evidence backup")
			}
			backup, err := os.ReadFile(backups[0])
			if err != nil || !bytes.Equal(backup, original) {
				t.Fatal("backup did not preserve exact original ciphertext")
			}
			for _, data := range [][]byte{encoded, backup} {
				if bytes.Contains(data, []byte("fixture-")) {
					t.Fatal("plaintext leaked to session files")
				}
			}
			if err := g.UpdateCookies(input); err != nil {
				t.Fatal(err)
			}
			backup, _ = os.ReadFile(backups[0])
			if !bytes.Equal(backup, original) {
				t.Fatal("second update overwrote original evidence")
			}
		})
	}
}

func TestUpdateCookiesFailureLeavesSessionUntouched(t *testing.T) {
	for _, tc := range []struct{ name, plain, input string }{
		{"invalid-input", syntheticSession, `{"SID":"fixture-sensitive"}`},
		{"invalid-json", `fixture-sensitive`, syntheticCookies},
		{"incomplete-pairing", `{"cookies":{}}`, syntheticCookies},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g, original := cookieUpdateFixture(t, tc.plain)
			err := g.UpdateCookies(tc.input)
			if err == nil || strings.Contains(err.Error(), "fixture-sensitive") {
				t.Fatal("expected sanitized failure")
			}
			current, _ := os.ReadFile(g.path)
			if !bytes.Equal(current, original) {
				t.Fatal("failure changed original session")
			}
			backups, _ := filepath.Glob(g.path + ".before-cookie-update-*.enc")
			if len(backups) != 0 {
				t.Fatal("validation failure created backup")
			}
		})
	}
	t.Run("missing-session", func(t *testing.T) {
		g, _ := cookieUpdateFixture(t, syntheticSession)
		os.Remove(g.path)
		if g.UpdateCookies(syntheticCookies) == nil {
			t.Fatal("missing session accepted")
		}
	})
	t.Run("corrupt-ciphertext", func(t *testing.T) {
		g, _ := cookieUpdateFixture(t, syntheticSession)
		original := []byte("fixture-corrupt-ciphertext")
		if err := os.WriteFile(g.path, original, 0600); err != nil {
			t.Fatal(err)
		}
		if err := g.UpdateCookies(syntheticCookies); err == nil || strings.Contains(err.Error(), "fixture-corrupt") {
			t.Fatal("expected sanitized decryption failure")
		}
		current, _ := os.ReadFile(g.path)
		if !bytes.Equal(current, original) {
			t.Fatal("corrupt session was overwritten")
		}
	})
	t.Run("backup-failure", func(t *testing.T) {
		g, original := cookieUpdateFixture(t, syntheticSession)
		// A long base name allows the original file but exceeds filesystem name
		// limits when the evidence-backup suffix is added.
		longPath := filepath.Join(filepath.Dir(g.path), strings.Repeat("s", 240))
		if err := os.Rename(g.path, longPath); err != nil {
			t.Fatal(err)
		}
		g.path = longPath
		if g.UpdateCookies(syntheticCookies) == nil {
			t.Fatal("backup failure accepted")
		}
		current, _ := os.ReadFile(g.path)
		if !bytes.Equal(current, original) {
			t.Fatal("backup failure changed original session")
		}
	})
}
