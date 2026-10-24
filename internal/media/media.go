// Package media defines bounded attachment payloads shared by transport and storage.
package media

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"mime"
	"net/http"
	"path"
	"strings"
	"unicode"
)

const MaxFileBytes = 10 << 20
const MaxTotalBytes = 20 << 20
const MaxFiles = 4

// A zero limit disables that application limit.
type Limits struct {
	MaxFiles    int64 `json:"max_files"`
	MaxFileMiB  int64 `json:"max_file_mb"`
	MaxTotalMiB int64 `json:"max_total_mb"`
}

func DefaultLimits() Limits { return Limits{MaxFiles, MaxFileBytes >> 20, MaxTotalBytes >> 20} }
func (l Limits) Validate() error {
	if l.MaxFiles < 0 || l.MaxFiles > (1<<53)-1 || l.MaxFileMiB < 0 || l.MaxTotalMiB < 0 || l.MaxFileMiB > (math.MaxInt64-1)>>20 || l.MaxTotalMiB > (math.MaxInt64-1)>>20 {
		return errors.New("invalid_attachment_limits")
	}
	return nil
}
func (l Limits) Check(files []File) error {
	if l.MaxFiles > 0 && int64(len(files)) > l.MaxFiles {
		return errors.New("too_many_attachments")
	}
	var total int64
	for _, file := range files {
		size := int64(len(file.Data))
		if l.MaxFileMiB > 0 && size > l.FileBytes() {
			return errors.New("attachment_size_limit")
		}
		total += size
		if l.MaxTotalMiB > 0 && total > l.MaxTotalMiB<<20 {
			return errors.New("attachment_size_limit")
		}
	}
	return nil
}
func (l Limits) FileBytes() int64 { return l.MaxFileMiB << 20 }

// JSON bodies include base64 expansion plus bounded metadata overhead. With an
// unlimited count or unconstrained total the owner opts out of the body ceiling.
func (l Limits) RequestBytes() int64 {
	if l.MaxFiles == 0 {
		return math.MaxInt64
	}
	total := l.MaxTotalMiB << 20
	if total == 0 && l.MaxFileMiB > 0 && l.FileBytes() <= math.MaxInt64/l.MaxFiles {
		total = l.FileBytes() * l.MaxFiles
	}
	if total == 0 || total > (math.MaxInt64-(1<<20))/2 || l.MaxFiles > (math.MaxInt64-(1<<20)-total*2)/2048 {
		return math.MaxInt64
	}
	return total*2 + l.MaxFiles*2048 + (1 << 20)
}

// Info is the only attachment representation returned in message lists.
type Info struct {
	PreviewOnly  bool   `json:"preview_only,omitempty"`
	ArchiveState string `json:"archive_state,omitempty"`
	ArchiveError string `json:"archive_error,omitempty"`
	ID           string `json:"id"`
	Name         string `json:"name"`
	MIME         string `json:"mime"`
	Size         int64  `json:"size"`
}

// File is stored only inside encrypted payloads. Remote keys never reach clients.
type File struct {
	Info
	// Google inline media can be a tiny preview, never an original.
	PreviewData         []byte `json:"preview_data,omitempty"`
	MessageID           string `json:"upstream_message_id,omitempty"`
	ActionID            string `json:"upstream_action_id,omitempty"`
	ConversationID      string `json:"upstream_conversation_id,omitempty"`
	Timestamp           int64  `json:"upstream_timestamp,omitempty"`
	SizeUnverified      bool   `json:"size_unverified,omitempty"`
	BlobID              string `json:"blob_id,omitempty"`
	Digest              string `json:"digest,omitempty"`
	RetryAt             string `json:"retry_at,omitempty"`
	RetryPolicy         string `json:"retry_policy,omitempty"`
	Failures            int    `json:"failures,omitempty"`
	RetryBackoffVersion int    `json:"retry_backoff_version,omitempty"`
	Data                []byte `json:"data,omitempty"`
	RemoteID            string `json:"remote_id,omitempty"`
	Key                 []byte `json:"key,omitempty"`
	ThumbnailID         string `json:"thumbnail_id,omitempty"`
	ThumbnailKey        []byte `json:"thumbnail_key,omitempty"`
	ReadLimit           int64  `json:"-"`
}

// Upload deliberately has no remote reference or key fields.
type Upload struct {
	Name string `json:"name"`
	MIME string `json:"mime"`
	Data []byte `json:"data"`
}

func SafeName(name string) string {
	name = path.Base(strings.ReplaceAll(name, "\\", "/"))
	name = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, name)
	runes := []rune(name)
	if len(runes) > 180 {
		name = string(runes[:180])
	}
	if name == "" || name == "." || name == ".." {
		return "attachment"
	}
	return name
}

func MIME(value string) string {
	m, _, err := mime.ParseMediaType(value)
	if err != nil || !strings.Contains(m, "/") {
		return "application/octet-stream"
	}
	switch strings.ToLower(m) {
	case "image/jpg":
		return "image/jpeg"
	case "image/x-ms-bmp":
		return "image/bmp"
	case "audio/mp3", "audio/mpg":
		return "audio/mpeg"
	default:
		return strings.ToLower(m)
	}
}

func Prepare(uploads []Upload) ([]File, error) {
	return PrepareWithLimits(uploads, DefaultLimits())
}
func PrepareWithLimits(uploads []Upload, limits Limits) ([]File, error) {
	if err := limits.Validate(); err != nil {
		return nil, err
	}
	if limits.MaxFiles > 0 && int64(len(uploads)) > limits.MaxFiles {
		return nil, errors.New("too_many_attachments")
	}
	var out []File
	total := int64(0)
	for i, u := range uploads {
		total += int64(len(u.Data))
		if len(u.Data) == 0 || (limits.MaxFileMiB > 0 && int64(len(u.Data)) > limits.FileBytes()) || (limits.MaxTotalMiB > 0 && total > limits.MaxTotalMiB<<20) {
			return nil, errors.New("attachment_size_limit")
		}
		m := MIME(u.MIME)
		if m == "application/octet-stream" {
			m = MIME(mime.TypeByExtension(path.Ext(u.Name)))
		}
		if m == "application/octet-stream" {
			m = http.DetectContentType(u.Data)
		}
		h := sha256.New()
		var index [8]byte
		binary.LittleEndian.PutUint64(index[:], uint64(i))
		h.Write(index[:])
		h.Write(u.Data)
		out = append(out, File{Info: Info{ID: hex.EncodeToString(h.Sum(nil)), Name: SafeName(u.Name), MIME: MIME(m), Size: int64(len(u.Data))}, Data: append([]byte(nil), u.Data...)})
	}
	return out, nil
}

func Intent(text string, files []File) string {
	if len(files) == 0 {
		h := sha256.Sum256([]byte(text))
		return hex.EncodeToString(h[:])
	}
	b, _ := json.Marshal(struct {
		Text  string
		Files []File
	}{text, files})
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func Infos(files []File) []Info {
	var out []Info
	for _, f := range files {
		out = append(out, f.Info)
	}
	return out
}
