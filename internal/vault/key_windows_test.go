package vault

import (
	"bytes"
	"encoding/base64"
	"os/exec"
	"testing"
)

// powershellProtect is how earlier versions protected the key; key files written
// by it must still open, and keys written now must still open with it.
func powershellProtect(t *testing.T, b []byte, method string) []byte {
	t.Helper()
	script := `Add-Type -AssemblyName System.Security; $b=[Convert]::FromBase64String([Console]::In.ReadToEnd()); $r=[Security.Cryptography.ProtectedData]::` + method + `($b,$null,[Security.Cryptography.DataProtectionScope]::CurrentUser); [Console]::Write([Convert]::ToBase64String($r))`
	c := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script)
	c.Stdin = bytes.NewBufferString(base64.StdEncoding.EncodeToString(b))
	out, err := c.Output()
	if err != nil {
		t.Skip("PowerShell DPAPI unavailable:", err)
	}
	decoded, err := base64.StdEncoding.DecodeString(string(out))
	if err != nil {
		t.Fatal(err)
	}
	return decoded
}

func TestKeyProtectionMatchesEarlierPowerShellFormat(t *testing.T) {
	key := bytes.Repeat([]byte{0x5a}, 32)
	old := powershellProtect(t, key, "Protect")
	if got, err := protect(old, true); err != nil || !bytes.Equal(got, key) {
		t.Fatalf("earlier key file did not open: %v", err)
	}
	current, err := protect(key, false)
	if err != nil || bytes.Equal(current, key) {
		t.Fatalf("protect: %v", err)
	}
	if got := powershellProtect(t, current, "Unprotect"); !bytes.Equal(got, key) {
		t.Fatal("current key file does not open with the earlier method")
	}
	if _, err := protect([]byte("not a dpapi blob"), true); err == nil {
		t.Fatal("garbage unprotected")
	}
}
