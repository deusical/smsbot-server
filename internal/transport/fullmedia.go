package transport

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"go.mau.fi/mautrix-gmessages/pkg/libgm/gmproto"
	"smsbot-server/internal/media"
	"sync"
	"time"
)

type fullMediaKey struct{ conversation, message, action string }
type fullMediaWait struct {
	done chan struct{}
	file media.File
	err  error
}
type fullMediaRequests struct {
	mu      sync.Mutex
	pending map[fullMediaKey]*fullMediaWait
}

// Only requested parts are observed here. Historical events remain excluded from
// command routing. The callback does no IO and retains no unsolicited messages.
func (r *fullMediaRequests) deliver(m *gmproto.Message) {
	r.mu.Lock()
	active := len(r.pending) > 0
	r.mu.Unlock()
	if !active {
		return
	}
	_, files, _ := messageParts(m)
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, f := range files {
		if f.RemoteID == "" || len(f.Key) != 32 {
			continue
		}
		key := fullMediaKey{f.ConversationID, f.MessageID, f.ActionID}
		if w := r.pending[key]; w != nil {
			select {
			case <-w.done:
			default:
				w.file = f
				close(w.done)
			}
		}
	}
}
func (r *fullMediaRequests) get(ctx context.Context, f media.File, request func(context.Context, string, string) error) (media.File, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	key := fullMediaKey{f.ConversationID, f.MessageID, f.ActionID}
	r.mu.Lock()
	if r.pending == nil {
		r.pending = make(map[fullMediaKey]*fullMediaWait)
	}
	w := r.pending[key]
	owner := w == nil
	if owner {
		if len(r.pending) >= 16 {
			r.mu.Unlock()
			return media.File{}, errors.New("original_pending")
		}
		w = &fullMediaWait{done: make(chan struct{})}
		r.pending[key] = w
	}
	r.mu.Unlock()
	if owner {
		defer func() { r.mu.Lock(); delete(r.pending, key); r.mu.Unlock() }()
		if err := request(ctx, key.message, key.action); err != nil {
			r.mu.Lock()
			select {
			case <-w.done:
			default:
				w.err = errors.New("original_unavailable")
				close(w.done)
			}
			r.mu.Unlock()
		}
	}
	select {
	case <-ctx.Done():
		return media.File{}, errors.New("original_pending")
	case <-w.done:
		if w.err != nil {
			return media.File{}, w.err
		}
		result := w.file
		result.ID = f.ID
		result.ReadLimit = f.ReadLimit
		return result, nil
	}
}
func (g *Google) fullSizeFile(ctx context.Context, f media.File) (media.File, error) {
	// The phone may already have uploaded the original, without emitting another
	// push. Read back the exact part as well as observing the requested update.
	return g.fullMedia.get(ctx, f, func(ctx context.Context, message, action string) error {
		if _, err := g.client.GetFullSizeImage(ctx, message, action); err != nil {
			return err
		}
		if f.ConversationID != "" && f.Timestamp > 0 {
			go func() {
				ticker := time.NewTicker(time.Second)
				defer ticker.Stop()
				for {
					select {
					case <-ctx.Done():
						return
					case <-ticker.C:
					}
					res, err := g.client.FetchMessages(ctx, f.ConversationID, 50, &gmproto.Cursor{LastItemTimestamp: time.UnixMicro(f.Timestamp).Add(time.Millisecond).UnixMilli()})
					if err != nil {
						return
					}
					for _, m := range res.GetMessages() {
						if m.GetConversationID() == f.ConversationID && m.GetMessageID() == message {
							g.fullMedia.deliver(m)
						}
					}
				}
			}()
		}
		return nil
	})
}

// RefreshFile repairs old imported descriptors using exact message identity,
// never a matching filename or equal message text.
type FileRefresher interface {
	RefreshFile(context.Context, string, string, time.Time, media.File) (media.File, error)
}

func (g *Google) RefreshFile(ctx context.Context, conv, id string, at time.Time, file media.File) (media.File, error) {
	if !g.Ready() {
		return media.File{}, errors.New("original_unavailable")
	}
	cursor := &gmproto.Cursor{LastItemTimestamp: at.Add(time.Millisecond).UnixMilli()}
	for page := 0; page < 4; page++ {
		res, err := g.client.FetchMessages(ctx, conv, 50, cursor)
		if err != nil {
			return media.File{}, errors.New("original_unavailable")
		}
		for _, m := range res.GetMessages() {
			sum := sha256.Sum256([]byte(conv + "\x00" + m.GetMessageID()))
			if m.GetConversationID() != conv || hex.EncodeToString(sum[:]) != id {
				continue
			}
			_, files, ok := messageParts(m)
			if ok {
				for _, f := range files {
					if f.ID == file.ID {
						return f, nil
					}
				}
			}
			return media.File{}, errors.New("original_unavailable")
		}
		cursor = res.GetCursor()
		if cursor == nil {
			break
		}
	}
	return media.File{}, errors.New("original_unavailable")
}
