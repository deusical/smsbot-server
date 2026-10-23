package archive

import (
	"bytes"
	"context"
	"github.com/deusical/smsbot-server/internal/store"
	"github.com/deusical/smsbot-server/internal/transport"
	"testing"
	"time"
)

func TestInteractionMetadataArchiveRoundTrip(t *testing.T) {
	s, manager := setup(t)
	seed(t, s, nil)
	v := &transport.MessageInteraction{UpstreamID: "message", Protocol: "RCS", OwnerID: "synthetic-self", ReplyTo: "synthetic-missing", ObservedAt: time.Now(), Reactions: []transport.Reaction{{Emoji: "👍", Participants: []string{"synthetic-self"}}}}
	if err := s.SaveObservedFiles("chat", "message", "", "incoming", "Synthetic edited", "phone", "read", time.Now().Add(-time.Hour), nil, v); err != nil {
		t.Fatal(err)
	}
	var encrypted bytes.Buffer
	if _, err := manager.Export(context.Background(), &encrypted, Scope{}, nil); err != nil {
		t.Fatal(err)
	}
	dst, recovery := setup(t)
	if _, err := recovery.Restore(context.Background(), bytes.NewReader(encrypted.Bytes()), nil); err != nil {
		t.Fatal(err)
	}
	snapshot, err := dst.Interaction("chat", store.Hash("chat\x00message"))
	if err != nil || snapshot == nil || snapshot.ReplyTo != v.ReplyTo || len(snapshot.Reactions) != 1 {
		t.Fatal("archive lost interactions", err)
	}
	var readable bytes.Buffer
	if _, err := manager.Export(context.Background(), &readable, Scope{Plaintext: true}, nil); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(readable.Bytes(), []byte(`"reply_to":"synthetic-missing"`)) {
		t.Fatal("plaintext export lost reply metadata")
	}
}
