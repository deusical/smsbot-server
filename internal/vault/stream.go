package vault

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"io"
)

const ChunkSize = 64 << 10
const streamHeaderSize = 84 // magic + scoped encrypted 32-byte random data key
var ErrStream = errors.New("invalid_encrypted_stream")

func (v *Vault) streamKey(scope string, header []byte) (cipher.AEAD, error) {
	if len(header) != streamHeaderSize || string(header[:4]) != "SMA1" {
		return nil, ErrStream
	}
	key, err := v.Open("stream-key:"+scope, string(header[4:]))
	if err != nil {
		return nil, ErrStream
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, ErrStream
	}
	return cipher.NewGCM(block)
}
func nonce(index uint64) []byte {
	b := make([]byte, 12)
	binary.BigEndian.PutUint64(b[4:], index)
	return b
}
func streamAAD(header []byte, index uint64, size uint32) []byte {
	b := make([]byte, len(header)+12)
	copy(b, header)
	binary.BigEndian.PutUint64(b[len(header):], index)
	binary.BigEndian.PutUint32(b[len(header)+8:], size)
	return b
}

// EncryptStream uses a fresh data key per stream, authenticated ordered chunks,
// and an authenticated empty final chunk. Callers publish only on nil error.
func (v *Vault) EncryptStream(w io.Writer, r io.Reader, scope string) (int64, error) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return 0, err
	}
	enc, err := v.Seal("stream-key:"+scope, key)
	if err != nil {
		return 0, err
	}
	header := []byte("SMA1" + enc)
	if len(header) != streamHeaderSize {
		return 0, ErrStream
	}
	a, err := v.streamKey(scope, header)
	if err != nil {
		return 0, err
	}
	if err = streamWrite(w, header); err != nil {
		return 0, err
	}
	buf := make([]byte, ChunkSize)
	var index uint64
	var total int64
	for {
		n, readErr := io.ReadFull(r, buf)
		if readErr != nil && readErr != io.EOF && readErr != io.ErrUnexpectedEOF {
			return total, readErr
		}
		var length [4]byte
		binary.BigEndian.PutUint32(length[:], uint32(n))
		sealed := a.Seal(nil, nonce(index), buf[:n], streamAAD(header, index, uint32(n)))
		if err = streamWrite(w, length[:]); err != nil {
			return total, err
		}
		if err = streamWrite(w, sealed); err != nil {
			return total, err
		}
		total += int64(n)
		index++
		if n == 0 {
			return total, nil
		}
	}
}

type StreamReader struct {
	r      io.Reader
	header []byte
	a      cipher.AEAD
	index  uint64
	buf    []byte
	final  bool
	short  bool
}

func (v *Vault) DecryptStream(r io.Reader, scope string) (*StreamReader, error) {
	header := make([]byte, streamHeaderSize)
	if _, err := io.ReadFull(r, header); err != nil {
		return nil, ErrStream
	}
	a, err := v.streamKey(scope, header)
	if err != nil {
		return nil, err
	}
	return &StreamReader{r: r, header: header, a: a}, nil
}
func (r *StreamReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if len(r.buf) > 0 {
		n := copy(p, r.buf)
		r.buf = r.buf[n:]
		return n, nil
	}
	if r.final {
		return 0, io.EOF
	}
	var length [4]byte
	if _, err := io.ReadFull(r.r, length[:]); err != nil {
		return 0, ErrStream
	}
	size := binary.BigEndian.Uint32(length[:])
	if size > ChunkSize || (r.short && size != 0) {
		return 0, ErrStream
	}
	b := make([]byte, int(size)+r.a.Overhead())
	if _, err := io.ReadFull(r.r, b); err != nil {
		return 0, ErrStream
	}
	plain, err := r.a.Open(nil, nonce(r.index), b, streamAAD(r.header, r.index, size))
	if err != nil {
		return 0, ErrStream
	}
	r.index++
	if size == 0 {
		var extra [1]byte
		n, err := r.r.Read(extra[:])
		if n != 0 || err != io.EOF {
			return 0, ErrStream
		}
		r.final = true
		return 0, io.EOF
	}
	r.short = size < ChunkSize
	r.buf = plain
	return r.Read(p)
}

// SeekStream authenticates the final marker and requested chunks without
// decrypting an entire large file or writing plaintext temporary files.
type SeekStream struct {
	r         io.ReaderAt
	a         cipher.AEAD
	header    []byte
	size, pos int64
	cached    int64
	buf       []byte
}

func (v *Vault) OpenSeekStream(r io.ReaderAt, cipherSize, size int64, scope string) (*SeekStream, error) {
	if size < 0 {
		return nil, ErrStream
	}
	chunks := (size + ChunkSize - 1) / ChunkSize
	if cipherSize != streamHeaderSize+size+(chunks+1)*20 {
		return nil, ErrStream
	}
	header := make([]byte, streamHeaderSize)
	if _, err := r.ReadAt(header, 0); err != nil {
		return nil, ErrStream
	}
	a, err := v.streamKey(scope, header)
	if err != nil {
		return nil, err
	}
	tail := make([]byte, 20)
	if _, err = r.ReadAt(tail, streamHeaderSize+size+chunks*20); err != nil {
		return nil, ErrStream
	}
	if binary.BigEndian.Uint32(tail) != 0 {
		return nil, ErrStream
	}
	if _, err = a.Open(nil, nonce(uint64(chunks)), tail[4:], streamAAD(header, uint64(chunks), 0)); err != nil {
		return nil, ErrStream
	}
	return &SeekStream{r: r, a: a, header: header, size: size, cached: -1}, nil
}
func (s *SeekStream) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if s.pos >= s.size {
		return 0, io.EOF
	}
	index := s.pos / ChunkSize
	if s.cached != index {
		n := min(int64(ChunkSize), s.size-index*ChunkSize)
		b := make([]byte, n+20)
		if _, err := s.r.ReadAt(b, streamHeaderSize+index*(ChunkSize+20)); err != nil {
			return 0, ErrStream
		}
		if int64(binary.BigEndian.Uint32(b)) != n {
			return 0, ErrStream
		}
		plain, err := s.a.Open(nil, nonce(uint64(index)), b[4:], streamAAD(s.header, uint64(index), uint32(n)))
		if err != nil {
			return 0, ErrStream
		}
		s.buf = plain
		s.cached = index
	}
	n := copy(p, s.buf[s.pos%ChunkSize:])
	s.pos += int64(n)
	return n, nil
}
func (s *SeekStream) Seek(offset int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
	case io.SeekCurrent:
		offset += s.pos
	case io.SeekEnd:
		offset += s.size
	default:
		return s.pos, ErrStream
	}
	if offset < 0 {
		return s.pos, ErrStream
	}
	s.pos = offset
	return offset, nil
}

func streamWrite(w io.Writer, b []byte) error {
	n, err := w.Write(b)
	if err == nil && n != len(b) {
		return io.ErrShortWrite
	}
	return err
}
