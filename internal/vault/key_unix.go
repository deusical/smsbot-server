//go:build !windows

package vault

import (
	"errors"
	"os"
)

func loadKey(path string) ([]byte, error) {
	s, e := os.Stat(path)
	if e != nil {
		return nil, e
	}
	if s.Mode().Perm()&0077 != 0 {
		return nil, errors.New("key permissions must be 0600 or stricter")
	}
	return os.ReadFile(path)
}
func saveKey(path string, b []byte) error {
	f, e := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if e != nil {
		return e
	}
	defer f.Close()
	_, e = f.Write(b)
	return e
}
