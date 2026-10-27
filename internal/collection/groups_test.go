package collection

import (
	"context"
	"testing"
	"time"

	"github.com/deusical/smsbot-server/internal/transport"
)

func demoGroup(t *testing.T, f *transport.Fixture) transport.Conversation {
	t.Helper()
	list, _ := f.Conversations(context.Background())
	for _, c := range list {
		if c.ID == "demo-group" {
			return c
		}
	}
	t.Fatal("no demo group")
	return transport.Conversation{}
}

func TestGroupsAreCollectedOnlyWhenChosenOrAskedFor(t *testing.T) {
	_, f := setup(t)
	g := demoGroup(t, f)
	individual := transport.Conversation{ID: "demo-chat", ParticipantID: "demo-peer", Writable: true}
	for name, tc := range map[string]struct {
		s     Settings
		c     transport.Conversation
		allow bool
	}{
		"all, no groups":          {Settings{Enabled: true, Scope: "all"}, g, false},
		"all, groups":             {Settings{Enabled: true, Scope: "all", Groups: true}, g, true},
		"all, individual":         {Settings{Enabled: true, Scope: "all"}, individual, true},
		"selected group":          {Settings{Enabled: true, Scope: "selected", Conversations: []Binding{{Conversation: "demo-group", Participant: "group:demo-group"}}}, g, true},
		"selected, other group":   {Settings{Enabled: true, Scope: "selected", Conversations: []Binding{{Conversation: "other", Participant: "group:other"}}}, g, false},
		"selected, wrong binding": {Settings{Enabled: true, Scope: "selected", Conversations: []Binding{{Conversation: "demo-group", Participant: "demo-member-a"}}}, g, false},
		"disabled":                {Settings{Scope: "all", Groups: true}, g, false},
		"group with no identity":  {Settings{Enabled: true, Scope: "all", Groups: true}, transport.Conversation{ID: "x", Group: true}, false},
		"unwritable individual":   {Settings{Enabled: true, Scope: "all"}, transport.Conversation{ID: "x", ParticipantID: "p"}, false},
	} {
		if got := tc.s.Allows(tc.c); got != tc.allow {
			t.Errorf("%s: %t", name, got)
		}
	}
}

func TestAGroupMemberSpeaksForTheGroupButNotForAnIndividual(t *testing.T) {
	member := transport.Incoming{ParticipantID: "demo-member-b"}
	if !speaksFor("group:demo-group", member) || speaksFor("demo-peer", member) || !speaksFor("demo-peer", transport.Incoming{ParticipantID: "demo-peer"}) || !speaksFor("demo-peer", transport.Incoming{Outgoing: true}) {
		t.Fatal("wrong sender attribution")
	}
}

func TestGroupMessagesAreSavedWithTheirSender(t *testing.T) {
	svc, _ := setup(t)
	s, _ := svc.Settings()
	s.Enabled, s.Scope, s.Groups, s.LoadOlder = true, "all", true, false
	if err := svc.SetSettings(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	for i, sender := range []string{"demo-member-a", "demo-member-b"} {
		err := svc.Observe(context.Background(), transport.Incoming{
			ID: "g" + string(rune('1'+i)), ConversationID: "demo-group", ParticipantID: sender, Text: "hello from " + sender, At: time.Now(),
			Interaction: &transport.MessageInteraction{UpstreamID: "g" + string(rune('1'+i)), Protocol: "RCS", Sender: sender, Reactions: []transport.Reaction{}},
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	page, err := svc.Store.OwnerMessagePage("demo-group", "", "", 50)
	if err != nil || len(page.Messages) != 2 {
		t.Fatalf("saved %d messages: %v", len(page.Messages), err)
	}
	if err = svc.Store.EnrichMessages("demo-group", page.Messages); err != nil {
		t.Fatal(err)
	}
	// Newest first.
	if page.Messages[0].Sender != "demo-member-b" || page.Messages[1].Sender != "demo-member-a" || page.Messages[0].Direction != "incoming" {
		t.Fatalf("senders: %+v", page.Messages)
	}
}

func TestAGroupIsNotSavedUnlessAllowed(t *testing.T) {
	svc, _ := setup(t)
	s, _ := svc.Settings()
	s.Enabled, s.Scope, s.LoadOlder = true, "all", false
	if err := svc.SetSettings(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	if err := svc.Observe(context.Background(), transport.Incoming{ID: "g1", ConversationID: "demo-group", ParticipantID: "demo-member-a", Text: "hi", At: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if page, _ := svc.Store.OwnerMessagePage("demo-group", "", "", 50); len(page.Messages) != 0 {
		t.Fatal("a group was saved without being allowed")
	}
}
