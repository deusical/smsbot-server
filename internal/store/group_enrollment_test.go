package store

import (
	"errors"
	"testing"
)

func TestGroupEnrollmentRules(t *testing.T) {
	s := newCheckpointStore(t)
	const roster, other = "group:r1", "group:r2"
	for name, args := range map[string][3]string{"no conversation": {"", "a", roster}, "no member": {"g", "", roster}, "not a roster": {"g", "a", "demo-peer"}} {
		if err := s.EnrollMember(args[0], args[1], args[2]); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	for _, m := range []string{"a", "b"} {
		if err := s.EnrollMember("g", m, roster); err != nil {
			t.Fatal(err)
		}
	}
	if members, got, err := s.GroupBot("g"); err != nil || len(members) != 2 || got != roster || !s.BotOn("g") || !s.HasEnrollment("g") {
		t.Fatalf("members %v roster %q %v", members, got, err)
	}
	// A group already approved for one roster takes no one under another.
	if err := s.EnrollMember("g", "c", other); !errors.Is(err, ErrGroupNeedsApproval) {
		t.Fatalf("enrolled under an unapproved roster: %v", err)
	}
	// Approval moves the members still there to the new roster and drops the rest.
	if err := s.ApproveGroup("g", other, []string{"a", "c"}); err != nil {
		t.Fatal(err)
	}
	members, got, _ := s.GroupBot("g")
	if len(members) != 1 || members[0] != "a" || got != other {
		t.Fatalf("after approval: %v %q", members, got)
	}
	if r, ok := s.GroupMemberEnrollment("g", "b"); ok || r != "" {
		t.Fatal("a member who left kept their access")
	}
	if err := s.ApproveGroup("g", "not-a-roster", nil); err == nil {
		t.Fatal("approved nothing in particular")
	}
	if err := s.UnenrollMember("g", "a"); err != nil || s.BotOn("g") {
		t.Fatalf("after removing the last member: %v", err)
	}
	// Turning the bot off for a conversation clears its group rows too.
	if err := s.EnrollMember("g", "a", roster); err != nil {
		t.Fatal(err)
	}
	if err := s.Unenroll("g"); err != nil || s.BotOn("g") {
		t.Fatalf("unenroll: %v", err)
	}
}

func TestGroupEnrollmentSurvivesABackup(t *testing.T) {
	source := newCheckpointStore(t)
	if err := source.EnrollMember("g", "a", "group:r1"); err != nil {
		t.Fatal(err)
	}
	if err := source.SetGroupShared("g", true); err != nil {
		t.Fatal(err)
	}
	data, err := source.ExportBackup()
	if err != nil {
		t.Fatal(err)
	}
	fresh := newCheckpointStore(t)
	if err = fresh.RestoreBackup(data); err != nil {
		t.Fatal(err)
	}
	if got, ok := fresh.GroupMemberEnrollment("g", "a"); !ok || got != "group:r1" {
		t.Fatalf("restored %q %t", got, ok)
	}
	if !fresh.GroupShared("g") {
		t.Fatal("a shared group came back unshared")
	}
	// A restore needs fresh state, and group enrollment counts as state.
	if err = fresh.RestoreBackup(data); err == nil {
		t.Fatal("restored over existing group enrollment")
	}
}
