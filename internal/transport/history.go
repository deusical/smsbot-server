package transport

import (
	"context"
	"encoding/base64"
	"errors"
	"go.mau.fi/mautrix-gmessages/pkg/libgm/gmproto"
	"google.golang.org/protobuf/proto"
	"smsbot-server/internal/media"
	"time"
)

type HistoricalMessage struct {
	Interaction   *MessageInteraction
	Attachments   []media.File
	ID            string
	Direction     string
	Text          string
	At            time.Time
	TransactionID string
	Status        string
}
type HistoryPage struct {
	Messages   []HistoricalMessage
	NextCursor string
}
type ConversationPage struct {
	Conversations []Conversation `json:"conversations"`
	NextCursor    string         `json:"next_cursor"`
	Partial       bool           `json:"partial,omitempty"`
	// Stale marks a cached page read before the latest phone activity; the
	// next request after a short spacing interval reads the phone again.
	Stale bool `json:"stale,omitempty"`
}
type ConversationPageSource interface {
	DiscoverConversations(context.Context, string) (ConversationPage, error)
}

func (f *Fixture) DiscoverConversations(ctx context.Context, cursor string) (ConversationPage, error) {
	if cursor != "" {
		return ConversationPage{}, errors.New("invalid_conversation_cursor")
	}
	list, err := f.Conversations(ctx)
	return ConversationPage{Conversations: list}, err
}
func (g *Google) DiscoverConversations(ctx context.Context, cursor string) (ConversationPage, error) {
	if !g.Ready() {
		return ConversationPage{}, errors.New("transport_unavailable")
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	var pointer *gmproto.Cursor
	if cursor != "" {
		if len(cursor) > 4096 {
			return ConversationPage{}, errors.New("invalid_conversation_cursor")
		}
		b, err := base64.RawURLEncoding.DecodeString(cursor)
		pointer = &gmproto.Cursor{}
		if err != nil || proto.Unmarshal(b, pointer) != nil {
			return ConversationPage{}, errors.New("invalid_conversation_cursor")
		}
	}
	return g.discovery.get(ctx, cursor, func(ctx context.Context) (ConversationPage, error) {
		g.countPhone(phoneList)
		res, err := g.client.ListConversations(ctx, &gmproto.ListConversationsRequest{Count: 25, Folder: gmproto.ListConversationsRequest_INBOX, Cursor: pointer})
		if err != nil {
			return ConversationPage{}, err
		}
		ids := make([]string, len(res.GetConversations()))
		for i, c := range res.GetConversations() {
			ids[i] = c.GetConversationID()
		}
		conversations, partial := discoveryConversations(ctx, ids, g.DiscoveryLookup)
		summaries := res.GetConversations()
		summaryByID := make(map[string]*gmproto.Conversation, len(summaries))
		for _, s := range summaries {
			if s != nil && s.GetConversationID() != "" {
				summaryByID[s.GetConversationID()] = s
			}
		}
		for i, conv := range conversations {
			if s := summaryByID[conv.ID]; s != nil {
				conversations[i] = withListState(conv, s)
			}
		}
		out := ConversationPage{Conversations: conversations, Partial: partial}
		if res.GetCursor() != nil {
			b, err := proto.Marshal(res.GetCursor())
			if err != nil {
				return out, err
			}
			if len(b) > 0 {
				out.NextCursor = base64.RawURLEncoding.EncodeToString(b)
			}
		}
		return out, nil
	})
}

type HistorySource interface {
	FetchHistory(context.Context, string, string, string) (HistoryPage, error)
}

func (f *Fixture) FetchHistory(ctx context.Context, conv, expectedPeer, cursor string) (HistoryPage, error) {
	if err := ctx.Err(); err != nil {
		return HistoryPage{}, err
	}
	if cursor != "" {
		return HistoryPage{}, errors.New("invalid_history_cursor")
	}
	return HistoryPage{Messages: []HistoricalMessage{
		{ID: "fixture-history", Direction: "incoming", Text: "!ask Historical synthetic input must never execute", At: time.Now().Add(-time.Hour)},
		{ID: "fixture-file-history", Direction: "incoming", At: time.Now().Add(-2 * time.Hour), Attachments: []media.File{{Info: media.Info{ID: "fixture-history-file", Name: "synthetic-history.txt", MIME: "text/plain", Size: 25}, Data: []byte("Synthetic historical file")}}},
	}}, nil
}
func (g *Google) FetchHistory(ctx context.Context, conv, expectedPeer, cursor string) (HistoryPage, error) {
	if !g.Ready() {
		return HistoryPage{}, errors.New("transport_unavailable")
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	g.countPhone(phoneLookup)
	c, err := g.client.GetConversation(ctx, conv)
	if err != nil || !eligible(c) || peer(c) != expectedPeer {
		return HistoryPage{}, errors.New("ineligible_conversation")
	}
	var pointer *gmproto.Cursor
	if cursor != "" {
		if len(cursor) > 4096 {
			return HistoryPage{}, errors.New("invalid_history_cursor")
		}
		b, err := base64.RawURLEncoding.DecodeString(cursor)
		if err != nil {
			return HistoryPage{}, errors.New("invalid_history_cursor")
		}
		pointer = &gmproto.Cursor{}
		if proto.Unmarshal(b, pointer) != nil {
			return HistoryPage{}, errors.New("invalid_history_cursor")
		}
	}
	observed := time.Now().UTC()
	g.countPhone(phoneMessages)
	result, err := g.client.FetchMessages(ctx, conv, 50, pointer)
	if err != nil {
		return HistoryPage{}, errors.New("history_fetch_failed")
	}
	g.countPhone(phoneLookup)
	c, err = g.client.GetConversation(ctx, conv)
	if err != nil || !eligible(c) || peer(c) != expectedPeer {
		return HistoryPage{}, errors.New("conversation_identity_changed")
	}
	out := HistoryPage{Messages: []HistoricalMessage{}}
	for _, m := range result.GetMessages() {
		if item, ok := historicalText(m, conv); ok {
			item.Interaction = interactionSnapshot(m, c.GetDefaultOutgoingID(), observed)
			out.Messages = append(out.Messages, item)
		}
	}
	if result.GetCursor() != nil {
		b, err := proto.Marshal(result.GetCursor())
		if err != nil {
			return HistoryPage{}, errors.New("history_cursor_invalid")
		}
		if len(b) > 0 {
			out.NextCursor = base64.RawURLEncoding.EncodeToString(b)
		}
	}
	return out, nil
}
func historicalText(m *gmproto.Message, conv string) (HistoricalMessage, bool) {
	if m == nil || m.GetMessageID() == "" || m.GetConversationID() != conv || len(m.GetMessageInfo()) == 0 || m.GetTimestamp() <= 0 {
		return HistoricalMessage{}, false
	}
	direction := ""
	switch m.GetMessageStatus().GetStatus() {
	case gmproto.MessageStatusType_INCOMING_COMPLETE, gmproto.MessageStatusType_INCOMING_DELIVERED, gmproto.MessageStatusType_INCOMING_DISPLAYED:
		direction = "incoming"
	case gmproto.MessageStatusType_OUTGOING_COMPLETE, gmproto.MessageStatusType_OUTGOING_DELIVERED, gmproto.MessageStatusType_OUTGOING_DISPLAYED:
		direction = "outgoing"
	default:
		return HistoricalMessage{}, false
	}
	text, files, ok := messageParts(m)
	if !ok || (text == "" && len(files) == 0) {
		return HistoricalMessage{}, false
	}
	at := time.UnixMicro(m.GetTimestamp())
	if at.After(time.Now().Add(5 * time.Second)) {
		return HistoricalMessage{}, false
	}
	status := ""
	if direction == "outgoing" {
		status = "submitted"
		if m.GetMessageStatus().GetStatus() == gmproto.MessageStatusType_OUTGOING_DELIVERED {
			status = "delivered"
		}
		if m.GetMessageStatus().GetStatus() == gmproto.MessageStatusType_OUTGOING_DISPLAYED {
			status = "read"
		}
	}
	return HistoricalMessage{ID: m.GetMessageID(), Direction: direction, Text: text, Attachments: files, At: at, TransactionID: m.GetTmpID(), Status: status}, true
}

// A range scan starts immediately above the viewed section, rather than paging
// through years of newer history to recheck an old section.
type HistoryRangeSource interface{ HistoryCursorBefore(time.Time) string }

func (g *Google) HistoryCursorBefore(until time.Time) string {
	b, _ := proto.Marshal(&gmproto.Cursor{LastItemTimestamp: until.UnixMilli()})
	return base64.RawURLEncoding.EncodeToString(b)
}
