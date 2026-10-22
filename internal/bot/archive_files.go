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
	ctx = transport.WithPhonePurpose(ctx, "original")
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
	ctx = transport.WithPhonePurpose(ctx, "original")
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
	file, err := e.Store.AttachmentSince(conv, message, attachment, since)
	if err == nil {
		if legacy, _ := e.legacyGooglePreview(conv, message, file); legacy {
			return media.File{}, errors.New("original_requires_refresh")
		}
	}
	return file, err
}

// Repair old imported previews before considering any blob an original. Saved
// web uploads are excluded; replacement uses the normal consent/identity CAS.
func (h archiveFileHost) Repair(ctx context.Context, c transport.Conversation, message string, file media.File) (bool, error) {
	ctx = transport.WithPhonePurpose(ctx, "original")
	if retry, _ := time.Parse(time.RFC3339Nano, file.RetryAt); h.e.now().Before(retry) {
		return false, nil
	}
	// Upgrade descriptors written before measured original sizes were tracked.
	if _, ok := h.e.Transport.(transport.FileRefresher); ok && file.MessageID != "" && !file.SizeUnverified && file.ArchiveState != "saved" {
		revision, err := h.Authorize(ctx, c)
		if err != nil {
			return false, err
		}
		updated := file
		updated.SizeUnverified = true
		updated.RetryAt = ""
		updated.Failures = 0
		return true, h.CommitFile(ctx, c, revision, message, file, updated)
	}
	legacy, at := h.e.legacyGooglePreview(c.ID, message, file)
	if !legacy {
		return false, nil
	}
	parent := ctx
	ctx, cancel := context.WithTimeout(parent, 20*time.Second)
	defer cancel()
	revision, err := h.Authorize(ctx, c)
	if err != nil {
		return false, err
	}
	refreshed, err := h.e.Transport.(transport.FileRefresher).RefreshFile(ctx, c.ID, message, at, file)
	if err != nil {
		if parent.Err() != nil {
			return false, err
		}
		// The read's own deadline also needs backoff. Use a fresh bounded
		// verification context to persist it, while honoring caller cancellation.
		persist, persistCancel := context.WithTimeout(parent, 20*time.Second)
		defer persistCancel()
		failed := file
		failed.Failures++
		failed.RetryAt = h.e.now().Add(time.Duration(1<<min(failed.Failures, 8)) * 5 * time.Second).UTC().Format(time.RFC3339Nano)
		failed.ArchiveState = "failed"
		failed.ArchiveError = "original_unavailable"
		if persistErr := h.CommitFile(persist, c, revision, message, file, failed); persistErr != nil {
			return false, persistErr
		}
		return false, errors.New("original_unavailable")
	}
	refreshed.RetryAt = ""
	refreshed.Failures = 0
	if err = h.CommitFile(ctx, c, revision, message, file, refreshed); err != nil {
		return false, err
	}
	return true, nil
}
