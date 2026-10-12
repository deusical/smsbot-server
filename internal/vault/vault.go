package vault

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
)

type Vault struct{ aead cipher.AEAD }

func New(key []byte) (*Vault, error) {
	b, e := aes.NewCipher(key)
	if e != nil {
		return nil, e
	}
	a, e := cipher.NewGCM(b)
	return &Vault{a}, e
}
func Open(keyPath string) (*Vault, error) {
	key, e := loadKey(keyPath)
	if e != nil {
		return nil, e
	}
	return New(key)
}
func Initialize(keyPath string) error {
	if _, e := os.Stat(keyPath); e == nil {
		return errors.New("key already exists")
	}
	if e := os.MkdirAll(filepath.Dir(keyPath), 0700); e != nil {
		return e
	}
	key := make([]byte, 32)
	if _, e := rand.Read(key); e != nil {
		return e
	}
	return saveKey(keyPath, key)
}
func (v *Vault) Seal(scope string, plain []byte) (string, error) {
	n := make([]byte, v.aead.NonceSize())
	if _, e := rand.Read(n); e != nil {
		return "", e
	}
	return base64.RawStdEncoding.EncodeToString(v.aead.Seal(n, n, plain, []byte(scope))), nil
}
func (v *Vault) Open(scope, encoded string) ([]byte, error) {
	b, e := base64.RawStdEncoding.DecodeString(encoded)
	if e != nil || len(b) < v.aead.NonceSize() {
		return nil, errors.New("invalid ciphertext")
	}
	return v.aead.Open(nil, b[:v.aead.NonceSize()], b[v.aead.NonceSize():], []byte(scope))
}
