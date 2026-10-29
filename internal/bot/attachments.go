package bot

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"path"
	"regexp"
	"strings"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/deusical/smsbot-server/internal/media"
	"github.com/deusical/smsbot-server/internal/store"
	"github.com/deusical/smsbot-server/internal/transport"
)

// What an AI command can be shown. Photos, PDFs, audio and video go to the
// provider as they are; text and office documents are turned into text first,
// so they work with any model.
const (
	kindImage    = "image"
	kindPDF      = "pdf"
	kindText     = "text"
	kindDocument = "document"
	kindAudio    = "audio"
	kindVideo    = "video"
)

// A photo is read up to maxPhotoMB. One over the owner's setting
// (defaultPhotoMB unless changed) is scaled to at most imageMaxSide pixels
// first. The other kinds have fixed limits: files sent as they are, documents
// being unpacked, and plain text. At most fileTextChars of a file's text reach
// the model. The *Tokens values are rough allowances reserved in the context
// and spending estimates before the file is read.
const (
	defaultPhotoMB = 5
	maxPhotoMB     = 25
	imageMaxSide   = 2048
	imageTokens    = 2000
	maxBinaryMB    = 20
	maxDocumentMB  = 20
	maxTextBytes   = 2 << 20
	fileTextChars  = 60000
	unzipLimit     = 24 << 20
)

// imageTypes are the photo formats every Chat Completions provider accepts.
var imageTypes = []string{"image/jpeg", "image/png", "image/gif", "image/webp"}

// heicTypes are the iPhone photo formats, which no provider takes: they are
// converted to JPEG here.
var heicTypes = []string{"image/heic", "image/heif"}

const photoFormats = "JPEG, PNG, GIF, WebP and HEIC"

const (
	docxType = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	xlsxType = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	pptxType = "application/vnd.openxmlformats-officedocument.presentationml.presentation"
	odtType  = "application/vnd.oasis.opendocument.text"
	odsType  = "application/vnd.oasis.opendocument.spreadsheet"
	odpType  = "application/vnd.oasis.opendocument.presentation"
)

// extTypes names a file's type from its extension, for the phones and apps
// that send everything as application/octet-stream.
var extTypes = map[string]string{
	".pdf": "application/pdf", ".docx": docxType, ".xlsx": xlsxType, ".pptx": pptxType,
	".odt": odtType, ".ods": odsType, ".odp": odpType,
	".mp3": "audio/mpeg", ".wav": "audio/wav", ".m4a": "audio/mp4", ".aac": "audio/aac",
	".ogg": "audio/ogg", ".oga": "audio/ogg", ".flac": "audio/flac", ".aif": "audio/aiff", ".aiff": "audio/aiff",
	".mp4": "video/mp4", ".mov": "video/quicktime", ".webm": "video/webm", ".mpeg": "video/mpeg", ".mpg": "video/mpeg",
	".svg": "image/svg+xml", ".heic": "image/heic", ".heif": "image/heif",
}

var textExtensions = strings.Fields(`.txt .md .markdown .csv .tsv .json .xml .yaml .yml .toml .ini .cfg .conf .log
	.html .htm .css .js .mjs .ts .tsx .jsx .py .go .java .kt .c .h .cc .cpp .hpp .cs .rs .rb .php .sh .bash .zsh .ps1
	.bat .sql .swift .lua .r .pl .ics .vcf .srt .vtt .tex .rst`)

var textApplicationTypes = []string{
	"application/json", "application/xml", "application/javascript", "application/x-javascript", "application/ecmascript",
	"application/x-yaml", "application/yaml", "application/toml", "application/sql", "application/x-sh", "application/csv",
	"application/x-ndjson", "application/x-httpd-php", "application/x-subrip",
}

// classify names how a file is read, from its type and name, and returns the
// type it should be treated as. The kind is "" for files that can't be read.
func classify(mime, name string) (kind, resolved string) {
	mime = strings.ToLower(strings.TrimSpace(mime))
	if i := strings.IndexByte(mime, ';'); i >= 0 {
		mime = strings.TrimSpace(mime[:i])
	}
	if mime == "" || mime == "application/octet-stream" || mime == "binary/octet-stream" {
		ext := strings.ToLower(path.Ext(name))
		if t, ok := extTypes[ext]; ok {
			mime = t
		} else if contains(textExtensions, ext) {
			mime = "text/plain"
		}
	}
	switch {
	case mime == "image/svg+xml":
		return kindText, mime
	case strings.HasPrefix(mime, "image/"):
		return kindImage, mime
	case mime == "application/pdf":
		return kindPDF, mime
	case mime == docxType || mime == xlsxType || mime == pptxType || mime == odtType || mime == odsType || mime == odpType:
		return kindDocument, mime
	case strings.HasPrefix(mime, "audio/"):
		return kindAudio, mime
	case strings.HasPrefix(mime, "video/"):
		return kindVideo, mime
	case strings.HasPrefix(mime, "text/") && mime != "text/rtf" && mime != "text/richtext",
		contains(textApplicationTypes, mime), strings.HasSuffix(mime, "+json"), strings.HasSuffix(mime, "+xml"):
		return kindText, mime
	}
	return "", mime
}

func kindOf(f *store.FileRef) string {
	kind, _ := classify(f.MIME, f.Name)
	return kind
}

// fileNoun is how a file is called in the prompt and in replies.
func fileNoun(f *store.FileRef) string {
	if kindOf(f) == kindImage {
		return "photo"
	}
	return "file"
}

// audioFormat is the name providers use for an audio type, or "" if they have none.
func audioFormat(mime string) string {
	switch mime {
	case "audio/mpeg", "audio/mp3", "audio/x-mpeg", "audio/mpeg3":
		return "mp3"
	case "audio/wav", "audio/x-wav", "audio/wave", "audio/vnd.wave":
		return "wav"
	case "audio/aac", "audio/x-aac":
		return "aac"
	case "audio/mp4", "audio/x-m4a", "audio/m4a":
		return "m4a"
	case "audio/ogg", "audio/vorbis":
		return "ogg"
	case "audio/flac", "audio/x-flac":
		return "flac"
	case "audio/aiff", "audio/x-aiff":
		return "aiff"
	}
	return ""
}

// videoType is the type providers accept a video as, or "" if they don't.
func videoType(mime string) string {
	switch mime {
	case "video/mp4", "video/mpeg", "video/webm":
		return mime
	case "video/quicktime", "video/mov":
		return "video/mov"
	}
	return ""
}

// cleanName reduces an attachment's name to something safe to put in a prompt.
func cleanName(name string) string {
	if i := strings.LastIndexAny(name, `/\`); i >= 0 {
		name = name[i+1:]
	}
	var b strings.Builder
	for _, r := range name {
		if r >= 0x20 && r != 0x7f && r != '"' {
			b.WriteRune(r)
		}
	}
	if r := []rune(strings.TrimSpace(b.String())); len(r) > 80 {
		return string(r[:80])
	}
	return strings.TrimSpace(b.String())
}

// fileOf describes the file a message carries: the first attachment this
// service can read, or else the first one, so it can be explained.
func fileOf(files []media.File) (media.Info, bool) {
	for _, f := range files {
		if kind, _ := classify(f.MIME, f.Name); kind != "" {
			return f.Info, true
		}
	}
	if len(files) > 0 {
		return files[0].Info, true
	}
	return media.Info{}, false
}

func refOf(info media.Info) store.FileRef {
	return store.FileRef{Name: cleanName(info.Name), MIME: info.MIME, Size: info.Size}
}

// quotedFile returns the file on a saved message, if any.
func (e *Engine) quotedFile(conv, message string) (*store.FileRef, error) {
	files, err := e.Store.MessageFiles(conv, message)
	if err != nil {
		return nil, err
	}
	info, ok := fileOf(files)
	if !ok {
		return nil, nil
	}
	ref := refOf(info)
	ref.Message, ref.Attachment = message, info.ID
	return &ref, nil
}

// ownFile is the file attached to a command message. A command may carry one:
// it, rather than a quoted file, is what the AI is shown.
func ownFile(m transport.Incoming) *store.FileRef {
	info, ok := fileOf(m.Attachments)
	if !ok {
		return nil
	}
	ref := refOf(info)
	ref.Wire = m.ID
	return &ref
}

// ownFilePrompt words the prompt for a command that came with its own file.
func ownFilePrompt(input string, f *store.FileRef) string {
	noun := fileNoun(f)
	if input == "" {
		return fmt.Sprintf("The person sent a %s with no message. Describe or respond to the attached %s.", noun, noun)
	}
	return fmt.Sprintf("(A %s is attached.) %s", noun, input)
}

// filesSaved reports whether files in conv are saved, which is how a command's
// own file reaches the AI: the conversation must be archived or open in the
// dashboard.
func (e *Engine) filesSaved(conv string) bool {
	s, err := e.Collection.Settings()
	return err == nil && s.AllowsID(conv)
}

// waitForFile finds a command's own file once it has been saved. Saving runs
// beside command handling, so it may need a moment.
func (e *Engine) waitForFile(ctx context.Context, conv, wire string) (message, attachment string, err error) {
	for attempt := 0; attempt < 20; attempt++ {
		id, _, _, ok, err := e.Store.QuotedMessage(conv, wire)
		if err != nil {
			return "", "", err
		}
		if ok {
			file, err := e.quotedFile(conv, id)
			if err != nil {
				return "", "", err
			}
			if file != nil {
				return id, file.Attachment, nil
			}
		}
		select {
		case <-ctx.Done():
			return "", "", ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	return "", "", errors.New("image_not_saved")
}

// fileUnreadable explains why a file can't be sent at all, or returns "" when
// it can. What the model or provider will accept is not judged here: a file is
// tried, and the chat is told if the provider refuses it. A photo over limitMB
// is shrunk later, except a WebP, which can't be. A HEIC photo is converted.
func fileUnreadable(f store.FileRef, limitMB int) string {
	kind, mime := classify(f.MIME, f.Name)
	tooBig := func(mb int64) bool { return f.Size > mb<<20 }
	switch kind {
	case kindImage:
		switch {
		case !contains(imageTypes, mime) && !contains(heicTypes, mime):
			return "I can only read " + photoFormats + " photos."
		case tooBig(maxPhotoMB):
			return fmt.Sprintf("That photo is too large for the AI; the limit is %d MB.", maxPhotoMB)
		case mime == "image/webp" && tooBig(int64(limitMB)):
			return fmt.Sprintf("That WebP photo is over %d MB and can't be shrunk; the limit is set in Settings.", limitMB)
		}
	case kindPDF:
		switch {
		case tooBig(maxBinaryMB):
			return fmt.Sprintf("That PDF is over %d MB, which is the most I can send.", maxBinaryMB)
		}
	case kindAudio:
		switch {
		case audioFormat(mime) == "":
			return "I can only listen to MP3, WAV, AAC, M4A, OGG, FLAC and AIFF audio."
		case tooBig(maxBinaryMB):
			return fmt.Sprintf("That audio is over %d MB, which is the most I can send.", maxBinaryMB)
		}
	case kindVideo:
		switch {
		case videoType(mime) == "":
			return "I can only watch MP4, MPEG, MOV and WebM video."
		case tooBig(maxBinaryMB):
			return fmt.Sprintf("That video is over %d MB, which is the most I can send.", maxBinaryMB)
		}
	case kindText:
		if f.Size > maxTextBytes {
			return fmt.Sprintf("That file is over %d MB, which is the most text I can read.", maxTextBytes>>20)
		}
	case kindDocument:
		if tooBig(maxDocumentMB) {
			return fmt.Sprintf("That document is over %d MB, which is the most I can open.", maxDocumentMB)
		}
	default:
		return "I can't read that kind of file. I can read photos, PDFs, text and code files, Word, Excel and PowerPoint documents, and (with a capable model) audio and video."
	}
	return ""
}

// fileTokens is a rough allowance for what a file adds to the prompt, reserved
// before it is read.
func fileTokens(f *store.FileRef) int {
	if f == nil {
		return 0
	}
	size := int(min(f.Size, 1<<30))
	chars := func(n int) int {
		if size > 0 {
			n = min(n, size)
		}
		return min(n, fileTextChars) / 3
	}
	switch kindOf(f) {
	case kindImage:
		return imageTokens
	case kindText:
		return chars(size)
	case kindDocument:
		return chars(size * 8)
	case kindPDF:
		return min(1000+size/6, 60000)
	case kindAudio:
		return min(1000+size/400, 60000)
	case kindVideo:
		return min(2000+size/300, 100000)
	}
	return 0
}

// fileRetryDelay is the pause before reading a file again after a temporary
// error. It is a variable so tests need not wait.
var fileRetryDelay = 2 * time.Second

// transientFileError reports a failure that may pass: the file changed while
// it was being saved elsewhere, or the phone hasn't produced it yet.
func transientFileError(err error) bool {
	switch err.Error() {
	case "attachment_changed", "original_pending", "attachment_unavailable":
		return true
	}
	return false
}

// fileRefusalReply is what the chat is told when the provider refuses a request
// that carried a file, which usually means the model can't read that kind.
func fileRefusalReply(err error) string {
	return "The AI provider wouldn't take that file with this model. Try a model that reads that kind of file. (" + safeProviderCode(err) + ")"
}

// providerRefused reports an HTTP 4xx answer from the provider, as opposed to
// the provider being down or the connection failing.
func providerRefused(err error) bool {
	return err != nil && strings.HasPrefix(err.Error(), "provider_http_4") && err.Error() != "provider_http_429"
}

var fileCodePattern = regexp.MustCompile(`^[a-z_]{1,40}$`)

// fileFailureReply is what the chat is told when a file can't be loaded. The
// code is one of this service's own fixed error names, never upstream text.
func fileFailureReply(err error) string {
	code := "image_unavailable"
	if fileCodePattern.MatchString(err.Error()) {
		code = err.Error()
	}
	why := "I couldn't get that file from your phone. Try sending it again in a moment."
	switch code {
	case "inbox_access_disabled", "image_not_saved":
		why = "I couldn't read that file; I can only read files in conversations you archive."
	case "attachment_size_limit", "image_too_large", "file_too_large":
		why = "That file is too large for the AI. For a photo, you can raise Shrink photos over in Settings."
	case "image_unsupported":
		why = "I can only read " + photoFormats + " photos."
	case "image_unreadable":
		why = "I couldn't read that photo."
	case "file_unreadable":
		why = "I couldn't read that file; it may be damaged or not what its name says."
	case "file_empty":
		why = "I couldn't find any text in that file."
	}
	return why + " (" + code + ")"
}

// readLimit is the most bytes of a file of this kind that are read.
func readLimit(kind string) int64 {
	switch kind {
	case kindImage:
		return maxPhotoMB << 20
	case kindText:
		return maxTextBytes
	case kindDocument:
		return maxDocumentMB << 20
	}
	return maxBinaryMB << 20
}

// loadFile reads a planned file for one generation: photos are shrunk when over
// limitMB, text and documents become text. Caller doesn't hold the control lock:
// the file may be downloaded from the phone.
func (e *Engine) loadFile(ctx context.Context, conv string, ref *store.FileRef, limitMB int) (*Attachment, error) {
	if ref == nil {
		return nil, nil
	}
	kind, mime := classify(ref.MIME, ref.Name)
	if kind == "" {
		return nil, errors.New("file_unreadable")
	}
	message, attachment := ref.Message, ref.Attachment
	if ref.Wire != "" {
		var err error
		if message, attachment, err = e.waitForFile(ctx, conv, ref.Wire); err != nil {
			return nil, err
		}
	}
	var data []byte
	var err error
	for attempt := 0; ; attempt++ {
		data, err = e.Collection.QuotedFile(ctx, conv, message, attachment, readLimit(kind))
		if err == nil || attempt >= 3 || !transientFileError(err) {
			break
		}
		// Another worker may be saving the same file, or the phone is slow.
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(fileRetryDelay):
		}
	}
	if err != nil {
		return nil, err
	}
	file := &Attachment{Kind: kind, MIME: mime, Name: ref.Name}
	switch kind {
	case kindImage:
		mime = http.DetectContentType(data)
		if media.IsHEIC(data) {
			mime = "image/heic"
		}
		if !contains(imageTypes, mime) && mime != "image/heic" {
			return nil, errors.New("image_unsupported")
		}
		// A HEIC photo is always converted: no provider reads it as it is.
		if limit := int64(limitMB) << 20; mime == "image/heic" || int64(len(data)) > limit {
			if mime == "image/webp" {
				return nil, errors.New("image_too_large")
			}
			if data, err = media.Shrink(data, imageMaxSide); err != nil {
				return nil, err
			}
			mime = "image/jpeg"
			if int64(len(data)) > limit {
				return nil, errors.New("image_too_large")
			}
		}
		file.MIME, file.Data = mime, data
	case kindPDF:
		if !bytes.Contains(data[:min(len(data), 1024)], []byte("%PDF-")) {
			return nil, errors.New("file_unreadable")
		}
		file.Data = data
	case kindAudio:
		file.Data = data
	case kindVideo:
		file.MIME, file.Data = videoType(mime), data
	case kindText:
		if file.Text, err = plainText(data); err != nil {
			return nil, err
		}
	case kindDocument:
		if file.Text, err = documentText(mime, data); err != nil {
			return nil, err
		}
	}
	if kind == kindText || kind == kindDocument {
		if file.Text = clipText(file.Text); file.Text == "" {
			return nil, errors.New("file_empty")
		}
	}
	return file, nil
}

// plainText reads a text file, which may be UTF-8 or UTF-16 and may carry a
// byte order mark. Anything that looks binary is refused.
func plainText(data []byte) (string, error) {
	var s string
	switch {
	case bytes.HasPrefix(data, []byte{0xEF, 0xBB, 0xBF}):
		s = string(data[3:])
	case bytes.HasPrefix(data, []byte{0xFF, 0xFE}), bytes.HasPrefix(data, []byte{0xFE, 0xFF}):
		big := data[0] == 0xFE
		units := make([]uint16, 0, len(data)/2)
		for i := 2; i+1 < len(data); i += 2 {
			if big {
				units = append(units, uint16(data[i])<<8|uint16(data[i+1]))
			} else {
				units = append(units, uint16(data[i+1])<<8|uint16(data[i]))
			}
		}
		s = string(utf16.Decode(units))
	default:
		s = string(data)
	}
	if strings.ContainsRune(s, 0) {
		return "", errors.New("file_unreadable")
	}
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "�")
		if strings.Count(s, "�")*20 > utf8.RuneCountInString(s) {
			return "", errors.New("file_unreadable")
		}
	}
	return s, nil
}

// clipText tidies extracted text and keeps at most fileTextChars of it.
func clipText(s string) string {
	s = tidyText(s)
	if r := []rune(s); len(r) > fileTextChars {
		return string(r[:fileTextChars]) + "\n[The file continues; the rest was cut off.]"
	}
	return s
}

var (
	trailingSpace = regexp.MustCompile(`[ \t\r]+\n`)
	blankRun      = regexp.MustCompile(`\n{3,}`)
)

func tidyText(s string) string {
	s = trailingSpace.ReplaceAllString(s, "\n")
	return strings.TrimSpace(blankRun.ReplaceAllString(s, "\n\n"))
}
