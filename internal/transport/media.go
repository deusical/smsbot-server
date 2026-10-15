package transport

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"smsbot-server/internal/media"
	"strings"
	"sync"

	"go.mau.fi/mautrix-gmessages/pkg/libgm"
	"go.mau.fi/mautrix-gmessages/pkg/libgm/gmproto"
)

type FilePreparer interface {
	PrepareFiles(context.Context, []media.File) ([]media.File, error)
}

func (g *Google) PrepareFiles(ctx context.Context, files []media.File) ([]media.File, error) {
	out := append([]media.File(nil), files...)
	for i, file := range out {
		if ctx.Err() != nil || !g.Ready() {
			return nil, errors.New("attachment_upload_failed")
		}
		uploaded, err := g.client.UploadMedia(file.Data, file.Name, file.MIME)
		if err != nil {
			return nil, errors.New("attachment_upload_failed")
		}
		out[i].RemoteID = uploaded.GetMediaID()
		out[i].Key = append([]byte(nil), uploaded.GetDecryptionKey()...)
	}
	if ctx.Err() != nil {
		return nil, errors.New("attachment_upload_failed")
	}
	return out, nil
}
func (f *Fixture) PrepareFiles(ctx context.Context, files []media.File) ([]media.File, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return files, nil
}

type FileSender interface {
	SendFiles(context.Context, string, string, string, []media.File) (Receipt, error)
}
type FileSource interface {
	DownloadFile(context.Context, media.File) ([]byte, error)
}

func messageParts(m *gmproto.Message) (string, []media.File, bool) {
	var text strings.Builder
	var files []media.File
	for i, part := range m.GetMessageInfo() {
		if content := part.GetMessageContent(); content != nil {
			text.WriteString(content.GetContent())
		} else if content := part.GetMediaContent(); content != nil {
			mime := content.GetMimeType()
			if mime == "" {
				mime = libgm.FormatToMediaType[content.GetFormat()].Format
			}
			file := media.File{Info: media.Info{ID: fmt.Sprintf("part-%d", i), Name: media.SafeName(content.GetMediaName()), MIME: media.MIME(mime), Size: content.GetSize()}, RemoteID: content.GetMediaID(), Key: append([]byte(nil), content.GetDecryptionKey()...), ThumbnailID: content.GetThumbnailMediaID(), ThumbnailKey: append([]byte(nil), content.GetThumbnailDecryptionKey()...)}
			if file.Size < 0 {
				file.Size = 0
			}
			if file.RemoteID == "" && len(content.GetMediaData()) > 0 {
				file.Size = int64(len(content.GetMediaData()))
			}
			if file.RemoteID == "" {
				file.Data = append([]byte(nil), content.GetMediaData()...)
				if len(file.Data) > 0 {
					file.Size = int64(len(file.Data))
				}
			}
			files = append(files, file)
		} else {
			return "", nil, false
		}
	}
	return text.String(), files, true
}

func outboundMedia(file media.File) *gmproto.MediaContent {
	format := libgm.MimeToMediaType[file.MIME].Type
	if format == 0 {
		format = libgm.MimeToMediaType[strings.SplitN(file.MIME, "/", 2)[0]].Type
	}
	if format == 0 {
		format = gmproto.MediaFormats_APP_UNSPECIFIED
	}
	return &gmproto.MediaContent{Format: format, MediaID: file.RemoteID, MediaName: file.Name, Size: file.Size, DecryptionKey: file.Key, MimeType: file.MIME}
}

func (g *Google) DownloadFile(ctx context.Context, file media.File) ([]byte, error) {
	if !g.Ready() || ctx.Err() != nil {
		return nil, errors.New("attachment_unavailable")
	}
	if file.ReadLimit > 0 && file.Size > file.ReadLimit {
		return nil, errors.New("attachment_size_limit")
	}
	if len(file.Data) > 0 {
		return file.Data, nil
	}
	if file.RemoteID == "" || len(file.Key) != 32 {
		return nil, errors.New("attachment_unavailable")
	}
	r, err := g.client.DownloadMedia(file.RemoteID, file.Key)
	if err != nil {
		return nil, errors.New("attachment_unavailable")
	}
	defer r.Close()
	// Closing the stream also bounds reads when the browser cancels its request.
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			r.Close()
		case <-done:
		}
	}()
	defer close(done)
	var reader io.Reader = r
	if file.ReadLimit > 0 {
		reader = io.LimitReader(r, file.ReadLimit+1)
	}
	b, err := io.ReadAll(reader)
	if err != nil || ctx.Err() != nil {
		return nil, errors.New("attachment_unavailable")
	}
	if file.ReadLimit > 0 && int64(len(b)) > file.ReadLimit {
		return nil, errors.New("attachment_size_limit")
	}
	return b, nil
}

func (f *Fixture) SendFiles(ctx context.Context, conv, txn, text string, files []media.File) (Receipt, error) {
	return f.Send(ctx, conv, txn, text)
}
func (f *Fixture) DownloadFile(ctx context.Context, file media.File) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(file.Data) == 0 {
		return nil, errors.New("attachment_unavailable")
	}
	return append([]byte(nil), file.Data...), nil
}

// StreamingFileSource never substitutes a thumbnail for an original.
type StreamingFileSource interface {
	OpenFile(context.Context, media.File) (io.ReadCloser, error)
}
type cancelReadCloser struct {
	io.ReadCloser
	once sync.Once
	done chan struct{}
}

func (r *cancelReadCloser) Close() error {
	var err error
	r.once.Do(func() { close(r.done); err = r.ReadCloser.Close() })
	return err
}
func (g *Google) OpenFile(ctx context.Context, file media.File) (io.ReadCloser, error) {
	if !g.Ready() || ctx.Err() != nil {
		return nil, errors.New("attachment_unavailable")
	}
	if len(file.Data) > 0 {
		return io.NopCloser(bytes.NewReader(file.Data)), nil
	}
	if file.RemoteID == "" || len(file.Key) != 32 {
		return nil, errors.New("attachment_unavailable")
	}
	// libgm's setup request is bounded by its HTTP client's timeout. Once the
	// response stream exists cancellation closes it, including blocked reads.
	r, err := g.client.DownloadMedia(file.RemoteID, file.Key)
	if err != nil {
		return nil, errors.New("attachment_unavailable")
	}
	wrapped := &cancelReadCloser{ReadCloser: r, done: make(chan struct{})}
	go func() {
		select {
		case <-ctx.Done():
			wrapped.Close()
		case <-wrapped.done:
		}
	}()
	if ctx.Err() != nil {
		wrapped.Close()
		return nil, ctx.Err()
	}
	return wrapped, nil
}
func (f *Fixture) OpenFile(ctx context.Context, file media.File) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(file.Data) == 0 {
		return nil, errors.New("attachment_unavailable")
	}
	return io.NopCloser(bytes.NewReader(file.Data)), nil
}
