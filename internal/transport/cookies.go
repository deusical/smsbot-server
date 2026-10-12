package transport

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"go.mau.fi/mautrix-gmessages/pkg/libgm"
)

// UpdateCookies is an offline operation. The caller must hold the service state
// lock. It preserves pairing fields and cookies outside the import allowlist.
func (g *Google) UpdateCookies(input string) error {
	if g.client != nil {
		return errors.New("cookie updates require a stopped service")
	}
	cookies, err := ParseCookies(input)
	if err != nil {
		return err
	}
	original, err := os.ReadFile(g.path)
	if err != nil {
		return errors.New("cookie update requires an existing saved pairing")
	}
	plain, err := g.vault.Open("google-session", string(original))
	if err != nil {
		return errors.New("cannot decrypt saved pairing")
	}
	var auth libgm.AuthData
	if json.Unmarshal(plain, &auth) != nil || auth.Browser == nil || auth.Mobile == nil || len(auth.TachyonAuthToken) == 0 {
		return errors.New("saved pairing is invalid or incomplete")
	}
	// Keep every other JSON field, including fields added by a future library.
	var fields map[string]json.RawMessage
	if json.Unmarshal(plain, &fields) != nil {
		return errors.New("saved pairing is invalid")
	}
	if auth.Cookies == nil {
		auth.Cookies = map[string]string{}
	}
	for name, value := range cookies {
		auth.Cookies[name] = value
	}
	// Absence of this optional imported cookie must not retain a stale value.
	if _, ok := cookies["__Secure-1PSIDTS"]; !ok {
		delete(auth.Cookies, "__Secure-1PSIDTS")
	}
	fields["cookies"], err = json.Marshal(auth.Cookies)
	if err != nil {
		return errors.New("cannot serialize cookies")
	}
	plain, err = json.Marshal(fields)
	if err != nil {
		return errors.New("cannot serialize session")
	}
	encoded, err := g.vault.Seal("google-session", plain)
	if err != nil {
		return errors.New("cannot encrypt updated session")
	}
	// Sync the exact old ciphertext before replacing anything. Unique names
	// preserve the evidence from every update attempt.
	backup, err := os.CreateTemp(filepath.Dir(g.path), filepath.Base(g.path)+".before-cookie-update-*.enc")
	if err != nil {
		return errors.New("cannot create encrypted session backup; session unchanged")
	}
	_, err = backup.Write(original)
	if err == nil {
		err = backup.Sync()
	}
	closeErr := backup.Close()
	if err != nil || closeErr != nil {
		return errors.New("cannot save encrypted session backup; session unchanged")
	}
	tmp, err := os.CreateTemp(filepath.Dir(g.path), filepath.Base(g.path)+".cookie-update-*")
	if err != nil {
		return errors.New("cannot create updated session; encrypted backup preserved")
	}
	defer os.Remove(tmp.Name())
	_, err = tmp.WriteString(encoded)
	if err == nil {
		err = tmp.Sync()
	}
	closeErr = tmp.Close()
	if err != nil || closeErr != nil {
		return errors.New("cannot save updated session; encrypted backup preserved")
	}
	if os.Rename(tmp.Name(), g.path) != nil {
		return errors.New("cannot replace updated session; encrypted backup preserved")
	}
	return nil
}
