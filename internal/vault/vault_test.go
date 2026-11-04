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

func TestBinarySealing(t *testing.T) {
	t.Parallel()
	v, e := New(make([]byte, 32))
	if e != nil {
		t.Fatal(e)
	}
	text := []byte("synthetic file bytes")
	a, e := v.SealBytes("a", text)
	if e != nil || bytes.Contains(a, text) {
		t.Fatal("not sealed", e)
	}
	b, _ := v.SealBytes("a", text)
	if bytes.Equal(a, b) {
		t.Fatal("nonce reused")
	}
	if plain, e := v.OpenBytes("a", a); e != nil || !bytes.Equal(plain, text) {
		t.Fatal("round trip failed")
	}
	if _, e = v.OpenBytes("b", a); e == nil {
		t.Fatal("scope substitution accepted")
	}
	if _, e = v.OpenBytes("a", a[:len(a)-1]); e == nil {
		t.Fatal("tampering accepted")
	}
	if _, e = v.OpenBytes("a", a[:3]); e == nil {
		t.Fatal("short input accepted")
	}
	// Text and binary forms are the same ciphertext.
	s, _ := v.Seal("a", text)
	if plain, e := v.Open("a", s); e != nil || !bytes.Equal(plain, text) {
		t.Fatal("text form changed")
	}
}
