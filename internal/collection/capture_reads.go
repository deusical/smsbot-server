package collection

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"github.com/deusical/smsbot-server/internal/media"
	"github.com/deusical/smsbot-server/internal/store"
	"github.com/deusical/smsbot-server/internal/transport"
	"sync"
)

type captureReads struct {
	mu       sync.Mutex
	flights  map[string]chan struct{}
	verified map[string][32]byte
}

func (c *captureReads) acquire(ctx context.Context, key string) (func(), error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		c.mu.Lock()
		if wait := c.flights[key]; wait != nil {
			c.mu.Unlock()
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-wait:
				continue
			}
		}
		if c.flights == nil {
			c.flights = map[string]chan struct{}{}
		}
		if len(c.flights) >= 1024 {
			c.mu.Unlock()
			return nil, fmt.Errorf("capture_busy")
		}
		done := make(chan struct{})
		c.flights[key] = done
		c.mu.Unlock()
		return func() { c.mu.Lock(); delete(c.flights, key); close(done); c.mu.Unlock() }, nil
	}
}
func (c *captureReads) matches(key string, digest [32]byte) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	old, ok := c.verified[key]
	return ok && old == digest
}
func (c *captureReads) remember(key string, digest [32]byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.verified == nil {
		c.verified = map[string][32]byte{}
	}
	if len(c.verified) >= 1024 {
		for old := range c.verified {
			delete(c.verified, old)
			break
		}
	}
	c.verified[key] = digest
}
func (svc *Service) captureConnectionEpoch() uint64 {
	if source, ok := svc.Transport.(transport.IdentityEpochSource); ok {
		return source.IdentityEpoch()
	}
	return 0
}
func (svc *Service) captureDigest(m transport.Incoming, consent *captureConsent) ([32]byte, error) {
	// File commits do not emit message changes. Include their encrypted local
	// version as well, without retaining the payload in the cache.
	var files string
	if err := svc.Store.DB.QueryRow("SELECT coalesce((SELECT payload FROM message_attachments WHERE message=?),'')", store.Hash(m.ConversationID+"\x00"+m.ID)).Scan(&files); err != nil {
		return [32]byte{}, err
	}
	// The digest and resource hash stay in bounded process memory; no personal
	// content or locator is retained in the cache or diagnostics.
	data, err := json.Marshal(struct {
		Message     transport.Incoming
		Interaction *transport.MessageInteraction
		Attachments []media.File
		Consent     uint64
		Revision    int64
		Connection  uint64
		Driver      string
		Changes     uint64
		Files       [32]byte
	}{m, m.Interaction, m.Attachments, consent.epoch, consent.settings.Revision, svc.captureConnectionEpoch(), fmt.Sprintf("%T:%p", svc.Transport, svc.Transport), svc.Store.ChangeSequence(), sha256.Sum256([]byte(files))})
	return sha256.Sum256(data), err
}
