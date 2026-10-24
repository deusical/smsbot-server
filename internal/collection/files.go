package collection

import (
	"context"
	"errors"
	"github.com/deusical/smsbot-server/internal/archive"
	"github.com/deusical/smsbot-server/internal/media"
	"github.com/deusical/smsbot-server/internal/transport"
	"io"
	"time"
)

type FileHost struct{ svc *Service }

func (h FileHost) Authorize(ctx context.Context, c transport.Conversation) (int64, error) {
	ctx = transport.WithPhonePurpose(ctx, "original")
	verified, err := h.svc.lookup(ctx, c.ID)
	if err != nil || verified.ParticipantID != c.ParticipantID || verified.Group {
		return 0, errors.New("archive_access_changed")
	}
	h.svc.mu.Lock()
	defer h.svc.mu.Unlock()
	settings, err := h.svc.Settings()
	if err != nil || !settings.Allows(verified) {
		return 0, errors.New("archive_access_changed")
	}
	return settings.Revision, nil
}
func (h FileHost) Open(ctx context.Context, file media.File) (io.ReadCloser, error) {
	source, ok := h.svc.Transport.(transport.StreamingFileSource)
	if !ok {
		return nil, errors.New("original_unavailable")
	}
	return source.OpenFile(ctx, file)
}
func (h FileHost) CommitFile(ctx context.Context, c transport.Conversation, revision int64, message string, expected, updated media.File) error {
	ctx = transport.WithPhonePurpose(ctx, "original")
	verified, err := h.svc.lookup(ctx, c.ID)
	if err != nil || verified.ParticipantID != c.ParticipantID || verified.Group {
		return errors.New("archive_access_changed")
	}
	h.svc.mu.Lock()
	defer h.svc.mu.Unlock()
	settings, err := h.svc.Settings()
	if err != nil || settings.Revision != revision || !settings.Allows(verified) || ctx.Err() != nil {
		return errors.New("archive_access_changed")
	}
	a, err := h.svc.Store.Archive(c.ID)
	if err != nil || a.Conversation.ParticipantID != c.ParticipantID {
		return errors.New("archive_access_changed")
	}
	return h.svc.Store.UpdateArchivedFile(ctx, message, expected, updated)
}

// Local archived bytes need no phone connection or collection grant. Only the
// authenticated owner endpoint calls this; archived identity and expiry apply.
func (svc *Service) LocalArchiveAttachment(conv, message, attachment string) (media.File, error) {
	a, err := svc.Store.Archive(conv)
	if err != nil || a.Conversation.Group || a.Conversation.ParticipantID == "" {
		return media.File{}, errors.New("archive_unavailable")
	}
	settings, err := svc.Settings()
	if err != nil {
		return media.File{}, err
	}
	var since time.Time
	if settings.ArchiveRetentionDays > 0 {
		since = svc.now().AddDate(0, 0, -settings.ArchiveRetentionDays)
	}
	file, err := svc.Store.AttachmentSince(conv, message, attachment, since)
	if err == nil {
		if legacy, _ := svc.legacyGooglePreview(conv, message, file); legacy {
			return media.File{}, errors.New("original_requires_refresh")
		}
	}
	return file, err
}

// Repair old imported previews before considering any blob an original. Saved
// web uploads are excluded; replacement uses the normal consent/identity CAS.
func (h FileHost) Repair(ctx context.Context, c transport.Conversation, message string, file media.File) (bool, error) {
	ctx = transport.WithPhonePurpose(ctx, "original")
	if h.svc.now().Before(archive.RetryAt(file)) {
		return false, nil
	}
	// Upgrade descriptors written before measured original sizes were tracked.
	if _, ok := h.svc.Transport.(transport.FileRefresher); ok && file.MessageID != "" && !file.SizeUnverified && file.ArchiveState != "saved" {
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
	legacy, at := h.svc.legacyGooglePreview(c.ID, message, file)
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
	refreshed, err := h.svc.Transport.(transport.FileRefresher).RefreshFile(ctx, c.ID, message, at, file)
	if err != nil {
		if parent.Err() != nil {
			return false, err
		}
		// The read's own deadline also needs backoff. Use a fresh bounded
		// verification context to persist it, while honoring caller cancellation.
		persist, persistCancel := context.WithTimeout(parent, 20*time.Second)
		defer persistCancel()
		failed := file
		archive.Backoff(&failed, h.svc.now())
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
