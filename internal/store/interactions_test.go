package store

import (
	"bytes"
	"smsbot-server/internal/transport"
	"testing"
	"time"
)

func TestInteractionSnapshotsChangesBackupAndScope(t *testing.T) {
	s := inboxStore(t)
	conv := "synthetic-chat"
	at := time.Now()
	v := &transport.MessageInteraction{UpstreamID: "synthetic-wire", Protocol: "RCS", OwnerID: "synthetic-self", ObservedAt: at, Reactions: []transport.Reaction{}}
	save := func(snapshot *transport.MessageInteraction) {
		t.Helper()
		if err := s.SaveObservedFiles(conv, "synthetic-wire", "", "incoming", "Synthetic body", "phone", "", at, nil, snapshot); err != nil {
			t.Fatal(err)
		}
	}
	save(v)
	id := Hash(conv + "\x00synthetic-wire")
	var cipher string
	s.DB.QueryRow("SELECT payload FROM message_interactions WHERE message=?", id).Scan(&cipher)
	if bytes.Contains([]byte(cipher), []byte("synthetic-wire")) {
		t.Fatal("wire ID persisted in plaintext")
	}
	before, _ := s.ChangeWatermark(conv)
	// Repeated snapshots do not create metadata changes or content revisions.
	v.ObservedAt = at.Add(time.Second)
	save(v)
	after, _ := s.ChangeWatermark(conv)
	if after != before {
		t.Fatal("unchanged snapshot generated changes")
	}
	v.Reactions = []transport.Reaction{{Emoji: "👍", Participants: []string{"synthetic-self"}}}
	v.ObservedAt = at.Add(2 * time.Second)
	save(v)
	changes, err := s.Changes(conv, "0", "")
	if err != nil || len(changes.Messages) != 1 || len(changes.Messages[0].Reactions) != 1 || !changes.Messages[0].Reactions[0].Mine || changes.Messages[0].Revision != 0 {
		t.Fatal("reaction-only change missing", err)
	}
	backup, err := s.ExportBackup()
	if err != nil {
		t.Fatal(err)
	}
	dst := inboxStore(t)
	if err = dst.RestoreBackup(backup); err != nil {
		t.Fatal(err)
	}
	got, err := dst.Interaction(conv, id)
	if err != nil || got == nil || got.UpstreamID != v.UpstreamID || len(got.Reactions) != 1 {
		t.Fatal("backup lost metadata", err)
	}
	if wrong, err := dst.Interaction("other", id); err != nil || wrong != nil {
		t.Fatal("cross-conversation metadata exposed")
	}
	v.Reactions = []transport.Reaction{}
	v.ObservedAt = at.Add(3 * time.Second)
	save(v)
	save(nil)
	got, _ = s.Interaction(conv, id)
	if len(got.Reactions) != 0 {
		t.Fatal("empty snapshot did not clear")
	}
	v.Reactions = []transport.Reaction{{Emoji: "❤️", Participants: []string{"synthetic-self"}}}
	v.ObservedAt = at
	save(v)
	got, _ = s.Interaction(conv, id)
	if len(got.Reactions) != 0 {
		t.Fatal("stale snapshot overwrote newer one")
	}
}

func TestQuoteResolutionAndMissingOriginal(t *testing.T) {
	s := inboxStore(t)
	at := time.Now()
	conv := "synthetic-quotes"
	if err := s.SaveObserved(conv, "original", "", "incoming", "Synthetic original", "phone", "", at); err != nil {
		t.Fatal(err)
	}
	v := &transport.MessageInteraction{UpstreamID: "reply", Protocol: "RCS", ReplyTo: "original", ObservedAt: at}
	if err := s.SaveObservedFiles(conv, "reply", "", "outgoing", "Synthetic reply", "phone", "", at, nil, v); err != nil {
		t.Fatal(err)
	}
	p, err := s.OwnerMessagePage(conv, "", "", 50)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range p.Messages {
		if m.Quote != nil && m.Quote.Available && m.Quote.Text == "Synthetic original" {
			found = true
		}
	}
	if !found {
		t.Fatal("quote not resolved")
	}
	v.ReplyTo = "missing"
	v.ObservedAt = at.Add(time.Second)
	s.SaveObservedFiles(conv, "reply", "", "outgoing", "Synthetic reply", "phone", "", at, nil, v)
	p, _ = s.OwnerMessagePage(conv, "", "", 50)
	for _, m := range p.Messages {
		if m.Quote != nil && m.Quote.Available {
			t.Fatal("missing target fabricated")
		}
	}
}

func TestMutationEvidenceSurvivesBackupWithoutReplay(t *testing.T) {
	s := inboxStore(t)
	intent := &MutationIntent{Target: "synthetic-target", WireTarget: "synthetic-wire", Protocol: "RCS", Emoji: "👍", Operation: "add", Version: "synthetic-version"}
	if _, err := s.Enqueue("synthetic-action-job", "synthetic-chat", "reaction", Plan{Manual: true, InboxOwnerOnly: true, Participant: "synthetic-peer", Mutation: intent}); err != nil {
		t.Fatal(err)
	}
	if err := s.StartMutation("synthetic-action-job", *intent); err != nil {
		t.Fatal(err)
	}
	backup, err := s.ExportBackup()
	if err != nil {
		t.Fatal(err)
	}
	dst := inboxStore(t)
	if err = dst.RestoreBackup(backup); err != nil {
		t.Fatal(err)
	}
	retained, err := dst.Mutation("synthetic-action-job")
	if err != nil || retained.WireTarget != intent.WireTarget || retained.Started.IsZero() {
		t.Fatal("mutation evidence lost", err)
	}
	jobs, err := dst.Jobs()
	if err != nil || len(jobs) != 1 || jobs[0].State != "unknown_submission" {
		t.Fatal("restored mutation replayable", err)
	}
	pending, err := dst.ActiveMutation("synthetic-chat", intent.Target)
	if err != nil || pending != "synthetic-action-job" {
		t.Fatal("restored uncertain target unlocked", err)
	}
}
