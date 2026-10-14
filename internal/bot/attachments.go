package bot

import (
	"context"
	"errors"
	"net/http"
	"smsbot-server/internal/media"
	"smsbot-server/internal/transport"
	"strings"
	"time"
)

// InboxAttachment rechecks current consent and exact participant identity before
// and after fetching private content. Remote locators and keys remain server-side.
func (e *Engine) InboxAttachment(ctx context.Context, conv, message, attachment string) (media.Info, []byte, error) {
	return e.InboxAttachmentContent(ctx, conv, message, attachment, false)
}
func (e *Engine) InboxAttachmentContent(ctx context.Context, conv, message, attachment string, preview bool) (media.Info, []byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	e.mu.Lock()
	s, err := e.InboxSettings()
	e.mu.Unlock()
	if err != nil || !s.Enabled {
		return media.Info{}, nil, errors.New("inbox_access_disabled")
	}
	c, err := e.lookupConversation(ctx, conv)
	if err != nil || !s.Allows(c) {
		return media.Info{}, nil, errors.New("inbox_access_disabled")
	}
	e.mu.Lock()
	current, err := e.InboxSettings()
	if err == nil && (current.Revision != s.Revision || !current.Allows(c)) {
		err = errors.New("inbox_access_disabled")
	}
	var since time.Time
	if err == nil {
		cfg, cfgErr := e.Configuration()
		err = cfgErr
		if err == nil && cfg.Settings.RetentionDays > 0 {
			since = e.now().AddDate(0, 0, -cfg.Settings.RetentionDays)
		}
	}
	var file media.File
	if err == nil {
		file, err = e.Store.AttachmentSince(conv, message, attachment, since)
	}
	e.mu.Unlock()
	if err != nil {
		return media.Info{}, nil, errors.New("attachment_unavailable")
	}
	file.ReadLimit = current.FileLimits().FileBytes()
	if file.ReadLimit > 0 && file.Size > file.ReadLimit {
		return media.Info{}, nil, errors.New("attachment_size_limit")
	}
	data := file.Data
	if len(data) == 0 {
		source, ok := e.Transport.(transport.FileSource)
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
	verified, err := e.lookupConversation(ctx, conv)
	if err != nil || verified.ParticipantID != c.ParticipantID {
		return media.Info{}, nil, errors.New("conversation_identity_changed")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	current, err = e.InboxSettings()
	if err != nil || current.Revision != s.Revision || !current.Allows(verified) || ctx.Err() != nil {
		return media.Info{}, nil, errors.New("inbox_access_disabled")
	}
	if limit := current.FileLimits().FileBytes(); limit > 0 && int64(len(data)) > limit {
		return media.Info{}, nil, errors.New("attachment_size_limit")
	}
	file.Info.Size = int64(len(data))
	return file.Info, data, nil
}
