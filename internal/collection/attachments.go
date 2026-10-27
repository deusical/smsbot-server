package collection

import (
	"context"
	"errors"
	"github.com/deusical/smsbot-server/internal/media"
	"github.com/deusical/smsbot-server/internal/transport"
	"io"
	"net/http"
	"strings"
	"time"
)

// InboxAttachment rechecks current consent and exact participant identity before
// and after fetching private content. Remote locators and keys remain server-side.
func (svc *Service) InboxAttachment(ctx context.Context, conv, message, attachment string) (media.Info, []byte, error) {
	return svc.InboxAttachmentContent(ctx, conv, message, attachment, false)
}
func (svc *Service) InboxAttachmentContent(ctx context.Context, conv, message, attachment string, preview bool) (media.Info, []byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	svc.mu.Lock()
	s, err := svc.Settings()
	svc.mu.Unlock()
	if err != nil || !s.Enabled {
		return media.Info{}, nil, errors.New("inbox_access_disabled")
	}
	c, err := svc.lookup(ctx, conv)
	if err != nil || !s.Allows(c) {
		return media.Info{}, nil, errors.New("inbox_access_disabled")
	}
	svc.mu.Lock()
	current, err := svc.Settings()
	if err == nil && (current.Revision != s.Revision || !current.Allows(c)) {
		err = errors.New("inbox_access_disabled")
	}
	var since time.Time
	if err == nil {
		days, daysErr := svc.messageRetention()
		err = daysErr
		if err == nil && days > 0 {
			since = svc.now().AddDate(0, 0, -days)
		}
	}
	var file media.File
	if err == nil {
		file, err = svc.Store.AttachmentSince(conv, message, attachment, since)
	}
	svc.mu.Unlock()
	if err != nil {
		return media.Info{}, nil, errors.New("attachment_unavailable")
	}
	previous := file
	repaired := false
	if legacy, at := svc.legacyGooglePreview(conv, message, file); legacy {
		if preview {
			file.PreviewData = file.Data
			file.Data = nil
			file.Size = 0
			file.ArchiveState = "pending"
		} else {
			source := svc.Transport.(transport.FileRefresher)
			file, err = source.RefreshFile(ctx, conv, message, at, file)
			if err != nil {
				return media.Info{}, nil, err
			}
			repaired = true
		}
	}
	file.ReadLimit = current.FileLimits().FileBytes()
	if file.ReadLimit > 0 && file.Size > file.ReadLimit {
		return media.Info{}, nil, errors.New("attachment_size_limit")
	}
	data := file.Data
	if preview && len(file.PreviewData) > 0 {
		data = file.PreviewData
	}
	if len(data) == 0 {
		source, ok := svc.Transport.(transport.FileSource)
		if !ok {
			return media.Info{}, nil, errors.New("attachment_unavailable")
		}
		data, err = source.DownloadFile(ctx, file)
		if err != nil && preview && strings.HasPrefix(file.MIME, "image/") && file.ThumbnailID != "" && ctx.Err() == nil {
			thumbnail := file
			thumbnail.RemoteID = file.ThumbnailID
			thumbnail.Key = file.ThumbnailKey
			thumbnail.Size = 0
			data, err = source.DownloadFile(ctx, thumbnail)
			if err == nil {
				file.Info.MIME = media.MIME(http.DetectContentType(data))
			}
		}
		if err != nil {
			return media.Info{}, nil, err
		}
	}
	verified, err := svc.lookup(ctx, conv)
	if err != nil || verified.ParticipantID != c.ParticipantID {
		return media.Info{}, nil, errors.New("conversation_identity_changed")
	}
	svc.mu.Lock()
	defer svc.mu.Unlock()
	current, err = svc.Settings()
	if err != nil || current.Revision != s.Revision || !current.Allows(verified) || ctx.Err() != nil {
		return media.Info{}, nil, errors.New("inbox_access_disabled")
	}
	if limit := current.FileLimits().FileBytes(); limit > 0 && int64(len(data)) > limit {
		return media.Info{}, nil, errors.New("attachment_size_limit")
	}
	if !preview {
		file.Size = int64(len(data))
		file.SizeUnverified = false
	}
	if repaired || (!preview && (previous.Size != file.Size || previous.SizeUnverified)) {
		if err := svc.Store.UpdateArchivedFile(ctx, message, previous, file); err != nil {
			return media.Info{}, nil, errors.New("attachment_changed")
		}
	}
	file.Info.Size = int64(len(data))
	return file.Info, data, nil
}

// QuotedFile reads an attachment an AI command quoted, at most limit bytes. It
// takes the dashboard's paths: a saved original first, otherwise a download
// from the phone with the same consent and identity checks.
func (svc *Service) QuotedFile(ctx context.Context, conv, message, attachment string, limit int64) ([]byte, error) {
	ctx = transport.WithPhonePurpose(ctx, "original")
	file, err := svc.LocalArchiveAttachment(conv, message, attachment)
	if err == nil && file.Size > limit {
		return nil, errors.New("attachment_size_limit")
	}
	var data []byte
	switch {
	case err == nil && file.BlobID != "" && svc.Archive != nil:
		local, openErr := svc.Archive.OpenLocal(file)
		if openErr != nil {
			return nil, errors.New("attachment_unavailable")
		}
		defer local.Close()
		data, err = io.ReadAll(io.LimitReader(local, limit+1))
		if err != nil {
			return nil, errors.New("attachment_unavailable")
		}
	case err == nil && len(file.Data) > 0:
		data = file.Data
	default:
		if _, data, err = svc.InboxAttachmentContent(ctx, conv, message, attachment, false); err != nil {
			return nil, err
		}
	}
	if int64(len(data)) > limit {
		return nil, errors.New("attachment_size_limit")
	}
	return data, nil
}

// Legacy Google imports stored inline image previews as originals. Local web
// sends remain authoritative. No size/dimension heuristic decides originality.
func (svc *Service) legacyGooglePreview(conv, message string, file media.File) (bool, time.Time) {
	if _, ok := svc.Transport.(transport.FileRefresher); !ok || file.MessageID != "" || file.RemoteID != "" || !strings.HasPrefix(file.MIME, "image/") {
		return false, time.Time{}
	}
	origin, at, err := svc.Store.MessageOrigin(conv, message)
	if err != nil || origin == "web" {
		return false, time.Time{}
	}
	stamp, err := time.Parse(time.RFC3339Nano, at)
	return err == nil, stamp
}
