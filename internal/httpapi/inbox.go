package httpapi

import (
	"bytes"
	"io"
	"mime"
	"net/http"
	"smsbot-server/internal/bot"
	"smsbot-server/internal/media"
	"smsbot-server/internal/store"
	"smsbot-server/internal/transport"
	"strconv"
	"time"
)

func (s *Server) inboxRoutes() {
	s.archiveRoutes()
	s.interactionRoutes()
	s.mux.HandleFunc("GET /api/v1/inbox/conversations/{id}/messages/{message}/attachments/{attachment}", s.protected(s.inboxAttachment))
	s.mux.HandleFunc("GET /api/v1/inbox/settings", s.protected(s.inboxSettings))
	s.mux.HandleFunc("PUT /api/v1/inbox/settings", s.protected(s.saveInboxSettings))
	s.mux.HandleFunc("GET /api/v1/inbox/conversations", s.protected(s.inboxConversations))
	s.mux.HandleFunc("GET /api/v1/inbox/typing", s.protected(s.inboxTyping))
	s.mux.HandleFunc("POST /api/v1/inbox/conversations/{id}/read", s.protected(s.inboxRead))
	s.mux.HandleFunc("GET /api/v1/inbox/conversations/{id}/history", s.protected(s.inboxHistory))
	s.mux.HandleFunc("POST /api/v1/inbox/conversations/{id}/history/import", s.protected(s.inboxImport))
	s.mux.HandleFunc("POST /api/v1/inbox/conversations/{id}/compose", s.protected(s.inboxCompose))
}
func (s *Server) inboxSettings(w http.ResponseWriter, r *http.Request) {
	v, err := s.Engine.InboxSettings()
	if err != nil {
		s.bad(w, 500, "settings_unavailable")
		return
	}
	s.json(w, 200, v)
}
func (s *Server) saveInboxSettings(w http.ResponseWriter, r *http.Request) {
	var v bot.InboxSettings
	if !s.decode(w, r, &v) {
		return
	}
	if err := s.Engine.SetInboxSettings(r.Context(), v); err != nil {
		s.bad(w, 409, err.Error())
		return
	}
	s.inboxSettings(w, r)
}
func (s *Server) inboxConversations(w http.ResponseWriter, r *http.Request) {
	var p transport.ConversationPage
	if s.Engine.Transport.Ready() {
		source, ok := s.Engine.Transport.(transport.ConversationPageSource)
		// The dashboard asks for one phone read when it returns to the foreground,
		// for changes the phone may not push. The transport spaces these reads.
		if refresher, can := s.Engine.Transport.(transport.ConversationRefresher); can && ok && r.URL.Query().Get("refresh") == "1" {
			refresher.RequestConversationRefresh()
		}
		if ok {
			var err error
			p, err = source.DiscoverConversations(r.Context(), r.URL.Query().Get("cursor"))
			if err != nil {
				p.Partial = true
			}
		}
	}
	archived, err := s.Engine.ArchiveConversations()
	if err != nil {
		s.bad(w, 500, "archive_unavailable")
		return
	}
	known := map[string]int{}
	for i, c := range p.Conversations {
		known[c.ID] = i
	}
	for _, c := range archived {
		if i, ok := known[c.ID]; ok {
			if p.Conversations[i].ParticipantID == c.ParticipantID {
				p.Conversations[i].Archived = true
			} else {
				c.Writable = false
				p.Conversations[i] = c
			}
		} else {
			p.Conversations = append(p.Conversations, c)
		}
	}
	eligible := []transport.Conversation{}
	for _, c := range p.Conversations {
		if !c.Group && (c.Writable || c.Archived) {
			eligible = append(eligible, c)
		}
	}
	p.Conversations = eligible

	latest, _ := s.Engine.Store.LatestMessageTimes(r.Context())
	settings, settingsErr := s.Engine.InboxSettings()
	var previewIDs []string
	for _, c := range p.Conversations {
		if settingsErr == nil && (c.Archived || settings.Allows(c)) {
			previewIDs = append(previewIDs, c.ID)
		}
	}
	previews, _ := s.Engine.Store.LatestPreviews(r.Context(), previewIDs)
	for i := range p.Conversations {
		p.Conversations[i].Enrolled = s.Engine.Store.Enrolled(p.Conversations[i].ID, p.Conversations[i].ParticipantID)
		p.Conversations[i].LastMessageAt = newestTime(p.Conversations[i].LastMessageAt, latest[p.Conversations[i].ID])
		if v, ok := previews[p.Conversations[i].ID]; ok {
			p.Conversations[i].Preview, p.Conversations[i].PreviewFromMe = v.Text, v.FromMe
			if v.Text == "" && v.Attachment {
				p.Conversations[i].Preview = "Attachment"
			}
		}
	}
	s.json(w, 200, p)
}
func (s *Server) inboxHistory(w http.ResponseWriter, r *http.Request) {
	limit := 50
	var err error
	if q := r.URL.Query().Get("limit"); q != "" {
		limit, err = strconv.Atoi(q)
		if err != nil {
			s.bad(w, 400, "invalid_limit")
			return
		}
	}
	var p store.InboxPage
	_, archiveErr := s.Engine.Store.Archive(r.PathValue("id"))
	if archiveErr == nil {
		p, err = s.localArchivePage(r.PathValue("id"), r.URL.Query().Get("cursor"), r.URL.Query().Get("search"), limit)
	} else {
		p, err = s.Engine.InboxPage(r.Context(), r.PathValue("id"), r.URL.Query().Get("cursor"), r.URL.Query().Get("search"), limit)
	}
	if err != nil {
		s.bad(w, 409, "history_unavailable_or_access_disabled")
		return
	}
	if archiveErr != nil {
		// Only persisted archives support the change feed. Without this distinction,
		// the dashboard stops snapshot polling and repeatedly requests a missing feed.
		s.json(w, 200, map[string]any{"messages": p.Messages, "next_cursor": p.NextCursor})
		return
	}
	s.json(w, 200, p)
}
func (s *Server) inboxImport(w http.ResponseWriter, r *http.Request) {
	var v struct {
		Cursor string `json:"cursor"`
	}
	if !s.decode(w, r, &v) {
		return
	}
	p, err := s.Engine.ImportInboxHistory(r.Context(), r.PathValue("id"), v.Cursor)
	if err != nil {
		s.bad(w, 409, "history_unavailable_or_access_disabled")
		return
	}
	s.json(w, 200, p)
}
func (s *Server) inboxCompose(w http.ResponseWriter, r *http.Request) {
	settings, settingsErr := s.Engine.InboxSettings()
	if settingsErr != nil {
		s.bad(w, 500, "settings_unavailable")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, settings.FileLimits().RequestBytes())
	var v struct {
		Attachments []media.Upload `json:"attachments"`
		Text        string         `json:"text"`
		RequestID   string         `json:"request_id"`
		ReplyTo     string         `json:"reply_to"`
	}
	if !s.decode(w, r, &v) {
		return
	}
	id, err := s.Engine.ComposeInboxFiles(r.Context(), r.PathValue("id"), v.Text, v.RequestID, v.Attachments, v.ReplyTo)
	if err != nil {
		code := "compose_unavailable"
		switch err.Error() {
		case "attachment_size_limit", "too_many_attachments", "attachments_unavailable", "request_id_reused_with_different_message", "bot_unavailable", "inbox_access_disabled", "invalid_message_or_request_id", "request_limit_reached", "archive_identity_changed":
			code = err.Error()
		}
		s.bad(w, 409, code)
		return
	}
	s.json(w, 202, map[string]any{"id": id, "queued": true})
}

func (s *Server) inboxAttachment(w http.ResponseWriter, r *http.Request) {
	var content io.ReadSeeker
	var info media.Info
	var localClose io.Closer
	file, localErr := s.Engine.LocalArchiveAttachment(r.PathValue("id"), r.PathValue("message"), r.PathValue("attachment"))
	if localErr == nil {
		settings, err := s.Engine.InboxSettings()
		if err != nil {
			s.bad(w, 500, "settings_unavailable")
			return
		}
		if limit := settings.FileLimits().FileBytes(); limit > 0 && file.Size > limit {
			s.bad(w, 413, "attachment_size_limit")
			return
		}
	}
	if localErr == nil && file.BlobID != "" {
		local, err := s.Engine.Archive.OpenLocal(file)
		if err != nil {
			s.bad(w, 409, "archived_file_missing_or_corrupt")
			return
		}
		content = local
		info = file.Info
		localClose = local
	} else if localErr == nil && len(file.Data) > 0 {
		content = bytes.NewReader(file.Data)
		info = file.Info
	}
	if localClose != nil {
		defer localClose.Close()
	}
	if content == nil {
		infoValue, data, err := s.Engine.InboxAttachmentContent(r.Context(), r.PathValue("id"), r.PathValue("message"), r.PathValue("attachment"), r.URL.Query().Get("inline") == "1")
		if err != nil {
			if err.Error() == "attachment_size_limit" {
				s.bad(w, 413, "attachment_size_limit")
			} else {
				s.bad(w, 409, "attachment_unavailable_or_access_disabled")
			}
			return
		}
		info = infoValue
		content = bytes.NewReader(data)
	}
	disposition := "attachment"
	if r.URL.Query().Get("inline") == "1" {
		switch info.MIME {
		case "image/jpeg", "image/png", "image/gif", "image/webp", "image/bmp", "audio/mpeg", "audio/mp3", "audio/mp4", "audio/ogg", "audio/wav", "audio/aac", "video/mp4", "video/webm", "video/ogg":
			disposition = "inline"
		}
	}
	w.Header().Set("Content-Type", info.MIME)
	w.Header().Set("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": media.SafeName(info.Name)}))
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	controller := http.NewResponseController(w)
	_ = controller.SetWriteDeadline(time.Time{})
	http.ServeContent(deadlineResponse{ResponseWriter: w, controller: controller}, r, info.Name, time.Time{}, content)
}

// newestTime returns the later of two RFC3339 timestamps, ignoring invalid input.
// Saved local messages are fresher than the phone list, which is cached briefly.
func newestTime(a, b string) string {
	ta, errA := time.Parse(time.RFC3339Nano, a)
	tb, errB := time.Parse(time.RFC3339Nano, b)
	switch {
	case errA != nil && errB != nil:
		return ""
	case errA != nil:
		return tb.UTC().Format(time.RFC3339Nano)
	case errB != nil || !tb.After(ta):
		return ta.UTC().Format(time.RFC3339Nano)
	}
	return tb.UTC().Format(time.RFC3339Nano)
}

func (s *Server) inboxTyping(w http.ResponseWriter, r *http.Request) {
	s.json(w, 200, map[string]any{"conversations": s.Engine.TypingConversations()})
}

// inboxRead marks a viewed conversation read on the phone through the newest
// message time the owner saw. It is idempotent and throttled per conversation.
func (s *Server) inboxRead(w http.ResponseWriter, r *http.Request) {
	var v struct {
		Through string `json:"through"`
	}
	if !s.decode(w, r, &v) {
		return
	}
	through, err := time.Parse(time.RFC3339Nano, v.Through)
	if err != nil {
		s.bad(w, 400, "invalid_time")
		return
	}
	if err = s.Engine.MarkConversationRead(r.Context(), r.PathValue("id"), through); err != nil {
		code := "read_unavailable"
		switch err.Error() {
		case "read_receipts_disabled", "inbox_access_disabled", "archive_identity_changed", "newer_message", "invalid_time", "ineligible_conversation", "conversation_identity_changed":
			code = err.Error()
		}
		s.bad(w, 409, code)
		return
	}
	s.json(w, 200, map[string]bool{"ok": true})
}
