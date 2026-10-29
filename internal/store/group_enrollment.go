package store

import (
	"strings"

	"github.com/deusical/smsbot-server/internal/transport"
)

// A group has no single person to enroll. The owner chooses which of its
// members may use the bot there, and approves the group's roster (exactly who
// is in it) for them. If anyone joins or leaves, the roster no longer matches
// and the bot stays silent in that group until the owner approves it again.
// Individual conversations keep their own table, unchanged.

func (s *Store) ensureGroupEnrollment() error {
	_, err := s.DB.Exec(`CREATE TABLE IF NOT EXISTS group_enrollment(conversation TEXT NOT NULL,participant TEXT NOT NULL,roster TEXT NOT NULL,PRIMARY KEY(conversation,participant));
 CREATE TABLE IF NOT EXISTS group_shared(conversation TEXT PRIMARY KEY)`)
	return err
}

// A group can share one AI conversation between everyone who uses the bot in it,
// the owner included, instead of each person having their own. The owner
// switches that on per group; it is off until then.

// SharedMember stands for the whole group as the owner of its shared AI
// conversation, in place of any one person.
const SharedMember = "shared:group"

// GroupShared reports whether the group conv has one AI conversation for all.
func (s *Store) GroupShared(conv string) bool {
	var n int
	return s.DB.QueryRow("SELECT count(*) FROM group_shared WHERE conversation=?", conv).Scan(&n) == nil && n > 0
}

// SetGroupShared switches a group between one AI conversation each and one for
// everyone. Either way the group's AI conversations are forgotten and work
// waiting for it is cancelled, so nobody inherits someone else's.
func (s *Store) SetGroupShared(conv string, shared bool) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if shared {
		_, err = tx.Exec("INSERT OR IGNORE INTO group_shared VALUES(?)", conv)
	} else {
		_, err = tx.Exec("DELETE FROM group_shared WHERE conversation=?", conv)
	}
	if err != nil {
		return err
	}
	if _, err = tx.Exec(cancelPending, conv); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	return s.ClearMemory(conv, "")
}

const cancelPending = "UPDATE jobs SET state='cancelled',code='unenrolled',plan='' WHERE conversation=? AND state IN ('queued','generating','awaiting_approval')"

// GroupMemberEnrollment returns the roster approved for a member of a group,
// and whether that member is enrolled.
func (s *Store) GroupMemberEnrollment(conv, member string) (roster string, ok bool) {
	return roster, s.DB.QueryRow("SELECT roster FROM group_enrollment WHERE conversation=? AND participant=?", conv, member).Scan(&roster) == nil
}

// GroupBot lists a group's enrolled members and the roster approved for them.
// The roster is "" when nobody is enrolled.
func (s *Store) GroupBot(conv string) (members []string, roster string, err error) {
	rows, err := s.DB.Query("SELECT participant,roster FROM group_enrollment WHERE conversation=? ORDER BY participant", conv)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	for rows.Next() {
		var member string
		if err = rows.Scan(&member, &roster); err != nil {
			return nil, "", err
		}
		members = append(members, member)
	}
	return members, roster, rows.Err()
}

// OwnerMember stands for the owner in a group's bot jobs. The owner is never in
// a group's member list and needs no enrolling: their own commands from the
// phone run in any group they can send to, under the people in it at the time.
const OwnerMember = "owner:self"

// ErrGroupChanged reports that the group's people differ from the roster
// already approved, so the new roster has to be approved first.
type groupError string

func (e groupError) Error() string { return string(e) }

const ErrGroupNeedsApproval = groupError("group_needs_approval")

// EnrollMember lets a member of a group use the bot there, under roster. If the
// group already has members approved under a different roster, that has to be
// approved first.
func (s *Store) EnrollMember(conv, member, roster string) error {
	if conv == "" || member == "" || !strings.HasPrefix(roster, "group:") {
		return groupError("invalid_enrollment")
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var stale int
	if err = tx.QueryRow("SELECT count(*) FROM group_enrollment WHERE conversation=? AND roster<>?", conv, roster).Scan(&stale); err != nil {
		return err
	}
	if stale > 0 {
		return ErrGroupNeedsApproval
	}
	if _, err = tx.Exec("INSERT INTO group_enrollment VALUES(?,?,?) ON CONFLICT(conversation,participant) DO UPDATE SET roster=excluded.roster", conv, member, roster); err != nil {
		return err
	}
	return tx.Commit()
}

// UnenrollMember stops a member using the bot in a group. Work waiting for the
// group is cancelled.
func (s *Store) UnenrollMember(conv, member string) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec("DELETE FROM group_enrollment WHERE conversation=? AND participant=?", conv, member); err != nil {
		return err
	}
	if _, err = tx.Exec(cancelPending, conv); err != nil {
		return err
	}
	return tx.Commit()
}

// ApproveGroup accepts the group's current roster for the members who are
// still in it, and drops the others. Work waiting for the group is cancelled.
func (s *Store) ApproveGroup(conv, roster string, present []string) error {
	if !strings.HasPrefix(roster, "group:") {
		return groupError("invalid_enrollment")
	}
	here := map[string]bool{}
	for _, id := range present {
		here[id] = true
	}
	members, _, err := s.GroupBot(conv)
	if err != nil {
		return err
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, member := range members {
		if !here[member] {
			if _, err = tx.Exec("DELETE FROM group_enrollment WHERE conversation=? AND participant=?", conv, member); err != nil {
				return err
			}
		}
	}
	if _, err = tx.Exec("UPDATE group_enrollment SET roster=? WHERE conversation=?", roster, conv); err != nil {
		return err
	}
	if _, err = tx.Exec(cancelPending, conv); err != nil {
		return err
	}
	return tx.Commit()
}

// BotOn reports whether the bot is enrolled in conv, for a person or a group.
func (s *Store) BotOn(conv string) bool {
	var n int
	return s.DB.QueryRow("SELECT (SELECT count(*) FROM enrollment WHERE conversation=?)+(SELECT count(*) FROM group_enrollment WHERE conversation=?)", conv, conv).Scan(&n) == nil && n > 0
}

// GroupBotJob reports whether a plan is the bot answering a member of a group,
// as opposed to the owner sending to one.
func (p Plan) GroupBotJob() bool { return p.Roster != "" && !p.InboxOwnerOnly }

// MemberOf is who a bot job's AI memory belongs to: the group member it is for,
// or the whole group when it shares one conversation. It is "" for any other job.
func (p Plan) MemberOf() string {
	switch {
	case !p.GroupBotJob():
		return ""
	case p.Shared:
		return SharedMember
	}
	return p.Participant
}

// ThreadOwner is who an AI thread started by this job belongs to.
func (p Plan) ThreadOwner() string {
	if m := p.MemberOf(); m != "" {
		return m
	}
	return p.Participant
}

// ShowEnrollment fills in whether the bot is on in a listed conversation: for
// a person, whether they are the one enrolled; for a group, who is enrolled.
func (s *Store) ShowEnrollment(c *transport.Conversation) {
	if !c.Group {
		c.Enrolled = s.Enrolled(c.ID, c.ParticipantID)
		return
	}
	members, roster, err := s.GroupBot(c.ID)
	c.Enrolled = err == nil && len(members) > 0
	c.SharedAI = s.GroupShared(c.ID)
	c.EnrolledMembers, c.BotRoster = members, roster
}
