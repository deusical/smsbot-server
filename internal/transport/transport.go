package transport

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"github.com/deusical/smsbot-server/internal/media"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"
)

// Member is a person in a group conversation other than the owner.
type Member struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
}

// GroupRoster names a group by exactly who is in it. Anything that acts for a
// group, such as sending to it, checks it, so that a message never reaches
// people the owner didn't see in the group.
func GroupRoster(ids []string) string {
	sorted := append([]string(nil), ids...)
	sort.Strings(sorted)
	sum := sha256.Sum256([]byte(strings.Join(sorted, "\x00")))
	return "group:" + hex.EncodeToString(sum[:16])
}

type Conversation struct {
	Archived bool   `json:"archived,omitempty"`
	ID       string `json:"id"`
	// ParticipantID is the other person in an individual conversation. A group
	// has one fixed identity of its own, "group:" and its conversation ID, so
	// that people joining or leaving doesn't disturb reading and archiving it.
	ParticipantID string `json:"participant_id"`
	// Members and Roster are a group's people now, and a name for exactly that
	// set (GroupRoster). Acting for a group requires the Roster to be the one
	// the owner saw.
	Members  []Member `json:"members,omitempty"`
	Roster   string   `json:"roster,omitempty"`
	Label    string   `json:"label"`
	Protocol string   `json:"protocol"`
	Group    bool     `json:"group"`
	Writable bool     `json:"writable"`
	Enrolled bool     `json:"enrolled"`
	// For a group, Enrolled is true if anyone is enrolled. EnrolledMembers are
	// who may use the bot there, and BotRoster the roster approved for them;
	// the bot is silent if the group's Roster is not that one.
	EnrolledMembers []string `json:"enrolled_members,omitempty"`
	BotRoster       string   `json:"bot_roster,omitempty"`
	// SharedAI is set for a group whose bot users share one AI conversation.
	SharedAI bool `json:"shared_ai,omitempty"`
	// LastMessageAt (RFC3339), Unread and Pinned mirror the phone list for ordering.
	// They are volatile display hints and never part of identity checks.
	LastMessageAt string `json:"last_message_at,omitempty"`
	Unread        bool   `json:"unread,omitempty"`
	Pinned        bool   `json:"pinned,omitempty"`
	// Preview is the newest saved message for conversations the owner collects.
	// It is filled per request for the owner list and never stored or logged.
	Preview       string `json:"preview,omitempty"`
	PreviewFromMe bool   `json:"preview_from_me,omitempty"`
}
type Incoming struct {
	Interaction    *MessageInteraction `json:"-"`
	Attachments    []media.File        `json:"-"`
	ID             string              `json:"id"`
	ConversationID string              `json:"conversation_id"`
	ParticipantID  string              `json:"participant_id"`
	Text           string              `json:"text"`
	At             time.Time           `json:"at"`
	Historical     bool                `json:"historical"`
	Outgoing       bool                `json:"outgoing"`
	TransactionID  string              `json:"transaction_id,omitempty"`
	Status         string              `json:"status,omitempty"`
}
type ConversationLookup interface {
	LookupConversation(context.Context, string) (Conversation, error)
}

// CachedLookup returns the phone record already held for a conversation
// without a phone round trip. Use it only to decide what to show; anything
// that acts must check again with Lookup.
type CachedLookup interface {
	CachedConversation(string) (Conversation, bool)
}
type Receipt struct {
	State     string
	MessageID string
}
type Transport interface {
	Conversations(context.Context) ([]Conversation, error)
	Send(context.Context, string, string, string) (Receipt, error)
	Ready() bool
}

var ErrUnavailable = errors.New("phone connection unavailable")

var ErrUnknown = errors.New("submission outcome unknown")
var ErrNotSubmitted = errors.New("transport did not submit")

// Fixture has no network implementation; it cannot contact Google or a recipient.
type Fixture struct {
	mu     sync.Mutex
	Online bool
	Fail   string
	Sent   map[string]string
	// Offline stand-ins for phone activity, typing and read marking.
	activity uint64
	typing   map[string]time.Time
	Read     map[string]int
	// groupMembers are who is in the demo group; nil means the default pair.
	groupMembers []string
}

var defaultGroupMembers = []string{"demo-member-a", "demo-member-b"}

// SetGroupMembers changes who is in the demo group, as people joining or
// leaving would on a phone.
func (f *Fixture) SetGroupMembers(ids ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.groupMembers = append([]string(nil), ids...)
}

func (f *Fixture) demoGroup() Conversation {
	f.mu.Lock()
	ids := f.groupMembers
	f.mu.Unlock()
	if ids == nil {
		ids = defaultGroupMembers
	}
	members := make([]Member, len(ids))
	names := map[string]string{"demo-member-a": "Alex", "demo-member-b": "Sam", "demo-member-c": "Jo"}
	for i, id := range ids {
		members[i] = Member{ID: id, Name: names[id]}
	}
	sort.Slice(members, func(i, j int) bool { return members[i].ID < members[j].ID })
	return Conversation{ID: "demo-group", ParticipantID: "group:demo-group", Label: "Group example", Protocol: "RCS", Group: true, Writable: true, Members: members, Roster: GroupRoster(ids)}
}
func NewFixture() *Fixture     { return &Fixture{Online: true, Sent: map[string]string{}} }
func (f *Fixture) Ready() bool { f.mu.Lock(); defer f.mu.Unlock(); return f.Online }
func (f *Fixture) Conversations(context.Context) ([]Conversation, error) {
	return []Conversation{{ID: "demo-chat", ParticipantID: "demo-peer", Label: "Demo conversation", Protocol: "RCS", Writable: true}, {ID: "demo-sms", ParticipantID: "demo-sms-peer", Label: "SMS example", Protocol: "SMS", Writable: true, Pinned: true}, f.demoGroup()}, nil
}
func (f *Fixture) Send(ctx context.Context, conv, txn, text string) (Receipt, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if e := ctx.Err(); e != nil {
		return Receipt{}, ErrNotSubmitted
	}
	if !f.Online {
		return Receipt{}, ErrNotSubmitted
	}
	if f.Fail == "unknown" {
		return Receipt{}, ErrUnknown
	}
	if f.Fail != "" {
		return Receipt{}, ErrNotSubmitted
	}
	if conv == "demo-group" {
		// As on the phone, a group is sent to only as the people the owner saw.
		ids := f.groupMembers
		if ids == nil {
			ids = defaultGroupMembers
		}
		if want, given := expectedRoster(ctx); !given || want == "" || want != GroupRoster(ids) {
			return Receipt{}, ErrNotSubmitted
		}
	}
	f.Sent[txn] = text
	return Receipt{State: "submitted", MessageID: txn}, nil
}

// Lookup returns the live identity of one conversation, bounded to 20 seconds.
// It uses a direct lookup when the transport supports one.
func Lookup(ctx context.Context, t Transport, id string) (Conversation, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if lookup, ok := t.(ConversationLookup); ok {
		return lookup.LookupConversation(ctx, id)
	}
	list, err := t.Conversations(ctx)
	if err != nil {
		return Conversation{}, err
	}
	for _, c := range list {
		if c.ID == id {
			return c, nil
		}
	}
	return Conversation{}, errors.New("conversation_unavailable")
}

// Same reports whether two records of a conversation are identical, including
// a group's members.
func (c Conversation) Same(o Conversation) bool { return reflect.DeepEqual(c, o) }

// Collectable reports whether the conversation's messages may be read and saved
// once the owner allows it: its exact identity is known and it is an individual
// conversation that can be sent to, or a group.
func (c Conversation) Collectable() bool {
	return c.ParticipantID != "" && (c.Writable || c.Group)
}

type rosterKey struct{}

// WithRoster says exactly who must be in a group for a send to it to go: the
// Roster the owner saw. A send to a group without it is refused.
func WithRoster(ctx context.Context, roster string) context.Context {
	return context.WithValue(ctx, rosterKey{}, roster)
}

func expectedRoster(ctx context.Context) (string, bool) {
	r, ok := ctx.Value(rosterKey{}).(string)
	return r, ok
}
