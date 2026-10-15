package vault

import (
	"bytes"
	"io"
	"testing"
)

func TestStreamAuthenticationAndSeeking(t *testing.T) {
	v, _ := New(make([]byte, 32))
	plain := bytes.Repeat([]byte("synthetic-stream"), 10000)
	var encrypted bytes.Buffer
	n, err := v.EncryptStream(&encrypted, bytes.NewReader(plain), "test")
	if err != nil || n != int64(len(plain)) {
		t.Fatal(n, err)
	}
	encoded := encrypted.Bytes()
	if bytes.Contains(encoded, plain[:100]) {
		t.Fatal("plaintext leaked")
	}
	reader, err := v.DecryptStream(bytes.NewReader(encoded), "test")
	if err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(reader)
	if err != nil || !bytes.Equal(b, plain) {
		t.Fatal("stream roundtrip", err)
	}
	seek, err := v.OpenSeekStream(bytes.NewReader(encoded), int64(len(encoded)), int64(len(plain)), "test")
	if err != nil {
		t.Fatal(err)
	}
	for _, offset := range []int64{0, 65533, 65536, int64(len(plain) - 31), 10} {
		if _, err = seek.Seek(offset, io.SeekStart); err != nil {
			t.Fatal(err)
		}
		b := make([]byte, min(int64(100), int64(len(plain))-offset))
		if _, err = io.ReadFull(seek, b); err != nil || !bytes.Equal(b, plain[offset:offset+int64(len(b))]) {
			t.Fatal("seek mismatch", offset, err)
		}
	}
	for _, change := range []string{"header", "body", "tail", "truncated", "reordered", "trailing"} {
		t.Run(change, func(t *testing.T) {
			bad := append([]byte(nil), encoded...)
			switch change {
			case "header":
				bad[10] ^= 1
			case "body":
				bad[1000] ^= 1
			case "tail":
				bad[len(bad)-1] ^= 1
			case "truncated":
				bad = bad[:len(bad)-20]
			case "trailing":
				bad = append(bad, 1)
			case "reordered":
				a := append([]byte(nil), bad[84:84+ChunkSize+20]...)
				copy(bad[84:84+ChunkSize+20], bad[84+ChunkSize+20:84+2*(ChunkSize+20)])
				copy(bad[84+ChunkSize+20:], a)
			}
			r, err := v.DecryptStream(bytes.NewReader(bad), "test")
			if err == nil {
				_, err = io.Copy(io.Discard, r)
			}
			if err == nil {
				t.Fatal("tampered stream accepted")
			}
		})
	}
	if _, err = v.DecryptStream(bytes.NewReader(encoded), "other-scope"); err == nil {
		t.Fatal("scope not authenticated")
	}
}
