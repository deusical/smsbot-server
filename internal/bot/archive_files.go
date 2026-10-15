package bot

import (
	"context"
	"errors"
	"io"
	"smsbot-server/internal/media"
	"smsbot-server/internal/transport"
	"time"
)

type archiveFileHost struct{ e *Engine }

func (h archiveFileHost) Authorize(ctx context.Context, c transport.Conversation) (int64, error) {
	verified, err := h.e.lookupConversation(ctx, c.ID)
	if err != nil || verified.ParticipantID != c.ParticipantID || verified.Group {
		return 0, errors.New("archive_access_changed")
	}
	h.e.mu.Lock()
	defer h.e.mu.Unlock()
	settings, err := h.e.InboxSettings()
	if err != nil || !settings.Allows(verified) {
		return 0, errors.New("archive_access_changed")
	}
	return settings.Revision, nil
}
func (h archiveFileHost) Open(ctx context.Context, file media.File) (io.ReadCloser, error) {
	source, ok := h.e.Transport.(transport.StreamingFileSource)
	if !ok {
		return nil, errors.New("original_unavailable")
	}
	return source.OpenFile(ctx, file)
}
func (h archiveFileHost) CommitFile(ctx context.Context, c transport.Conversation, revision int64, message string, expected, updated media.File) error {
	verified, err := h.e.lookupConversation(ctx, c.ID)
	if err != nil || verified.ParticipantID != c.ParticipantID || verified.Group {
		return errors.New("archive_access_changed")
	}
	h.e.mu.Lock()
	defer h.e.mu.Unlock()
	settings, err := h.e.InboxSettings()
	if err != nil || settings.Revision != revision || !settings.Allows(verified) || ctx.Err() != nil {
		return errors.New("archive_access_changed")
	}
	a, err := h.e.Store.Archive(c.ID)
	if err != nil || a.Conversation.ParticipantID != c.ParticipantID {
		return errors.New("archive_access_changed")
	}
	return h.e.Store.UpdateArchivedFile(ctx, message, expected, updated)
}

// Local archived bytes need no phone connection or collection grant. Only the
// authenticated owner endpoint calls this; archived identity and expiry apply.
func (e *Engine) LocalArchiveAttachment(conv, message, attachment string) (media.File, error) {
	a, err := e.Store.Archive(conv)
	if err != nil || a.Conversation.Group || a.Conversation.ParticipantID == "" {
		return media.File{}, errors.New("archive_unavailable")
	}
	settings, err := e.InboxSettings()
	if err != nil {
		return media.File{}, err
	}
	var since time.Time
	if settings.ArchiveRetentionDays > 0 {
		since = e.now().AddDate(0, 0, -settings.ArchiveRetentionDays)
	}
	return e.Store.AttachmentSince(conv, message, attachment, since)
}
