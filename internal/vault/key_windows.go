package vault

import (
	"bytes"
	"encoding/base64"
	"errors"
	"os"
	"os/exec"
)

func protect(b []byte, decrypt bool) ([]byte, error) {
	method := "Protect"
	if decrypt {
		method = "Unprotect"
	}
	script := `Add-Type -AssemblyName System.Security; $b=[Convert]::FromBase64String([Console]::In.ReadToEnd()); $r=[Security.Cryptography.ProtectedData]::` + method + `($b,$null,[Security.Cryptography.DataProtectionScope]::CurrentUser); [Console]::Write([Convert]::ToBase64String($r))`
	c := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script)
	c.Stdin = bytes.NewBufferString(base64.StdEncoding.EncodeToString(b))
	out, e := c.Output()
	if e != nil {
		return nil, errors.New("windows key protection failed")
	}
	return base64.StdEncoding.DecodeString(string(out))
}
func loadKey(path string) ([]byte, error) {
	b, e := os.ReadFile(path)
	if e != nil {
		return nil, e
	}
	return protect(b, true)
}
func saveKey(path string, b []byte) error {
	b, e := protect(b, false)
	if e != nil {
		return e
	}
	f, e := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if e != nil {
		return e
	}
	defer f.Close()
	_, e = f.Write(b)
	return e
}
