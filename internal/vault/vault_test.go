package vault

import (
	"bytes"
	"testing"
)

func TestEncryption(t *testing.T) {
	v, e := New(make([]byte, 32))
	if e != nil {
		t.Fatal(e)
	}
	text := []byte("synthetic secret")
	a, e := v.Seal("a", text)
	if e != nil {
		t.Fatal(e)
	}
	b, _ := v.Seal("a", text)
	if a == b {
		t.Fatal("nonce reused")
	}
	plain, e := v.Open("a", a)
	if e != nil || !bytes.Equal(plain, text) {
		t.Fatal("round trip failed")
	}
	if _, e = v.Open("b", a); e == nil {
		t.Fatal("scope substitution accepted")
	}
	if _, e = v.Open("a", a[:len(a)-4]); e == nil {
		t.Fatal("tampering accepted")
	}
}
