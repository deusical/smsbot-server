package httpapi

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"path"
	"strings"

	"github.com/deusical/smsbot-server/internal/media"
)

// maxHEICPreview is the largest HEIC photo converted for display.
const maxHEICPreview = 25 << 20

// heicSlots bounds the photos converted at once; each decode takes a while and
// some memory.
var heicSlots = make(chan struct{}, 2)

func isHEICType(mime string) bool { return mime == "image/heic" || mime == "image/heif" }

// previewHEIC answers an inline request for a HEIC photo, which most browsers
// can't show, with a JPEG of it. The file is sniffed, not trusted by its type: the
// phone's thumbnail of a HEIC photo is already a JPEG or similar. The original
// stays a download, unchanged.
func previewHEIC(content io.ReadSeeker, info media.Info) (io.ReadSeeker, media.Info, error) {
	data, err := io.ReadAll(io.LimitReader(content, maxHEICPreview+1))
	if err != nil || len(data) > maxHEICPreview {
		return nil, info, errors.New("image_unreadable")
	}
	if !media.IsHEIC(data) {
		// Already an ordinary image: show it as what it is.
		if kind := http.DetectContentType(data); strings.HasPrefix(kind, "image/") && !isHEICType(kind) {
			info.MIME = kind
			return bytes.NewReader(data), info, nil
		}
		return nil, info, errors.New("image_unreadable")
	}
	heicSlots <- struct{}{}
	defer func() { <-heicSlots }()
	jpeg, err := media.Shrink(data, 2048)
	if err != nil {
		return nil, info, err
	}
	info.MIME = "image/jpeg"
	info.Name = strings.TrimSuffix(info.Name, path.Ext(info.Name)) + ".jpg"
	return bytes.NewReader(jpeg), info, nil
}
