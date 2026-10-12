package transport

import (
	"context"
	"encoding/base64"
	"errors"
	"go.mau.fi/mautrix-gmessages/pkg/libgm/gmproto"
	"google.golang.org/protobuf/proto"
	"strings"
	"time"
)

type HistoricalMessage struct {
	ID        string
	Direction string
	Text      string
	At        time.Time
}
type HistoryPage struct {
	Messages   []HistoricalMessage
	NextCursor string
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
	return HistoryPage{Messages: []HistoricalMessage{{ID: "fixture-history", Direction: "incoming", Text: "!ask Historical synthetic input must never execute", At: time.Now().Add(-time.Hour)}}}, nil
}
func (g *Google) FetchHistory(ctx context.Context, conv, expectedPeer, cursor string) (HistoryPage, error) {
	if !g.Ready() {
		return HistoryPage{}, errors.New("transport_unavailable")
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
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
	result, err := g.client.FetchMessages(ctx, conv, 50, pointer)
	if err != nil {
		return HistoryPage{}, errors.New("history_fetch_failed")
	}
	c, err = g.client.GetConversation(ctx, conv)
	if err != nil || !eligible(c) || peer(c) != expectedPeer {
		return HistoryPage{}, errors.New("conversation_identity_changed")
	}
	out := HistoryPage{Messages: []HistoricalMessage{}}
	for _, m := range result.GetMessages() {
		if item, ok := historicalText(m, conv); ok {
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
	var text strings.Builder
	for _, part := range m.GetMessageInfo() {
		if part.GetMessageContent() == nil {
			return HistoricalMessage{}, false
		}
		text.WriteString(part.GetMessageContent().GetContent())
		if text.Len() > 12000 {
			return HistoricalMessage{}, false
		}
	}
	if text.Len() == 0 {
		return HistoricalMessage{}, false
	}
	at := time.UnixMicro(m.GetTimestamp())
	if at.After(time.Now().Add(5 * time.Second)) {
		return HistoricalMessage{}, false
	}
	return HistoricalMessage{ID: m.GetMessageID(), Direction: direction, Text: text.String(), At: at}, true
}
