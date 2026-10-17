package transport

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/rs/zerolog"
	"go.mau.fi/mautrix-gmessages/pkg/libgm"
	"go.mau.fi/mautrix-gmessages/pkg/libgm/events"
	"go.mau.fi/mautrix-gmessages/pkg/libgm/gmproto"
	"go.mau.fi/util/exhttp"
	"io"
	"os"
	"regexp"
	"smsbot-server/internal/media"
	"smsbot-server/internal/vault"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Google is opt-in and never instantiated by fixture mode.
type Google struct {
	recovery      recoveryControl
	recoveryWake  chan struct{}
	recoveryProbe func(context.Context) error // offline fault injection; nil in production
	fullMedia     fullMediaRequests
	client        *libgm.Client
	vault         *vault.Vault
	path          string
	ready         atomic.Bool
	mu            sync.Mutex
	listMu        sync.Mutex
	listed        time.Time
	list          []Conversation
	settings      *gmproto.Settings
	conversations map[string]*gmproto.Conversation
	updates       chan any
	queueMu       sync.Mutex
	overflow      atomic.Bool
	healthFailure atomic.Bool
	overflowWake  chan struct{}
	started       time.Time
	saveMu        sync.Mutex
	Diagnostic    func(AuthDiagnostic)
	discovery     conversationPageCache
}

func NewGoogle(v *vault.Vault, path string) *Google {
	return &Google{recoveryWake: make(chan struct{}, 1), vault: v, path: path, updates: make(chan any, 256), overflowWake: make(chan struct{}, 1), conversations: map[string]*gmproto.Conversation{}}
}
func (g *Google) attach(auth *libgm.AuthData) {
	g.client = libgm.NewClient(auth, nil, zerolog.New(io.Discard), exhttp.SensibleClientSettings)
	g.client.SetEventHandler(g.enqueue)
}

// libgm invokes this synchronously on its receive loop. Never wait for the
// consumer, touch storage, or make a phone request here.
func (g *Google) enqueue(event any) {
	_, reason, hard, _, failed := connectionFailure(event)
	if failed {
		g.noteFailure(reason, hard)
	}
	original := event
	switch e := event.(type) {
	case *events.ListenRecovered, *events.PhoneRespondingAgain:
		g.wakeRecovery(true)
		return
	case *events.AccountChange, *events.ListenFatalError, *events.ListenTemporaryError, *events.PingFailed, *events.GaiaLoggedOut, *events.PhoneNotResponding, *events.NoDataReceived, *events.HackySetActiveMayFail:
	case *events.AuthTokenRefreshed, *gmproto.Settings, *gmproto.Conversation:
	case *libgm.WrappedMessage:
		if e == nil || e.Message == nil {
			return
		}

		g.fullMedia.deliver(e.Message)

		// Historical incoming messages cannot run commands or reconcile sends.
		// Retain outgoing status events for transaction reconciliation.
		if e.IsOld {
			switch e.GetMessageStatus().GetStatus() {
			case gmproto.MessageStatusType_OUTGOING_COMPLETE, gmproto.MessageStatusType_OUTGOING_DELIVERED, gmproto.MessageStatusType_OUTGOING_DISPLAYED:
				if e.GetTmpID() == "" {
					return
				}
			default:
				return
			}
		}
	default:
		return
	}
	if failed {
		event = connectionEvent{original}
	}
	g.queueMu.Lock()
	defer g.queueMu.Unlock()
	select {
	case g.updates <- event:
	default:
		g.overflow.Store(true)
		select {
		case g.overflowWake <- struct{}{}:
		default:
		}
	}
}

func (g *Google) beginOverflowRecovery(pause func()) {
	pause()
	// No work from before the gap may be submitted later. Refresh identities
	// through the normal full-detail picker before accepting new commands.
	g.invalidateIdentities()
	g.mu.Lock()
	g.settings = nil // A dropped settings event makes the overflow case different from reconnecting.
	g.mu.Unlock()
	g.diagnose("event_queue_overflow", errors.New("unavailable"))
}

func (g *Google) finishOverflowRecovery() bool {
	g.queueMu.Lock()
	defer g.queueMu.Unlock()
	if len(g.updates) != 0 {
		return false
	}
	g.started = time.Now()
	g.overflow.Store(false)
	return true
}
func ParseCookies(input string) (map[string]string, error) {
	cookies := map[string]string{}
	if json.Unmarshal([]byte(input), &cookies) != nil {
		re := regexp.MustCompile(`(?i)(?:-H|--header|-b|--cookie)\s+['"]([^'"\r\n]+)['"]`)
		for _, m := range re.FindAllStringSubmatch(input, -1) {
			s := m[1]
			if strings.HasPrefix(strings.ToLower(s), "cookie:") {
				s = strings.TrimSpace(s[7:])
			} else if strings.Contains(s, ":") {
				continue
			}
			for _, part := range strings.Split(s, ";") {
				name, value, ok := strings.Cut(strings.TrimSpace(part), "=")
				if ok {
					cookies[name] = value
				}
			}
		}
	}
	allowed := map[string]string{}
	for _, name := range []string{"SID", "HSID", "SSID", "OSID", "APISID", "SAPISID"} {
		if cookies[name] == "" {
			return nil, errors.New("required Google session cookies missing; use local Copy as cURL (bash) for /web/config")
		}
		allowed[name] = cookies[name]
	}
	if x := cookies["__Secure-1PSIDTS"]; x != "" {
		allowed["__Secure-1PSIDTS"] = x
	}
	return allowed, nil
}
func (g *Google) Pair(ctx context.Context, input string, showEmoji func(string)) error {
	if _, e := os.Stat(g.path); e == nil {
		return errors.New("saved pairing exists; use a fresh state directory for a different account")
	}
	cookies, e := ParseCookies(input)
	if e != nil {
		return e
	}
	auth := libgm.NewAuthData()
	auth.SetCookies(cookies)
	g.attach(auth)
	defer g.client.Disconnect()
	if e = g.client.FetchConfig(ctx); e != nil {
		return errors.New("pairing configuration failed")
	}
	emoji, session, e := g.client.StartGaiaPairing(ctx, ctx)
	if e != nil {
		return errors.New("pairing could not start")
	}
	showEmoji(emoji)
	if _, e = g.client.FinishGaiaPairing(ctx, session); e != nil {
		return errors.New("pairing failed or approval timed out")
	}
	return g.Save()
}
func (g *Google) Save() error {
	g.saveMu.Lock()
	defer g.saveMu.Unlock()
	if g.client == nil {
		return nil
	}
	a := g.client.AuthData
	a.CookiesLock.RLock()
	defer a.CookiesLock.RUnlock()
	b, e := json.Marshal(a)
	if e != nil {
		return errors.New("cannot serialize session")
	}
	encoded, e := g.vault.Seal("google-session", b)
	if e != nil {
		return e
	}
	if e = os.WriteFile(g.path+".tmp", []byte(encoded), 0600); e != nil {
		return errors.New("cannot save session")
	}
	return os.Rename(g.path+".tmp", g.path)
}
func (g *Google) Connect(ctx context.Context) error {
	encoded, e := os.ReadFile(g.path)
	if e != nil {
		return errors.New("pair this service first using --pair --cookies-stdin")
	}
	b, e := g.vault.Open("google-session", string(encoded))
	if e != nil {
		return errors.New("cannot decrypt saved pairing")
	}
	var auth libgm.AuthData
	if json.Unmarshal(b, &auth) != nil {
		return errors.New("saved pairing is invalid")
	}
	g.attach(&auth)
	g.started = time.Now()
	deadline, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	if e = g.client.FetchConfig(deadline); e != nil {
		g.diagnose("config_fetch", e)
		return errors.New("session needs reauthentication or network recovery")
	}
	if e = g.client.Connect(ctx); e != nil {
		g.diagnose("paired_connect", e)
		return errors.New("paired connection failed")
	}
	probe, e := g.client.IsBugleDefault(deadline)
	g.diagnose("phone_readiness", e)
	if e != nil || !probe.GetSuccess() {
		g.client.Disconnect()
		return errors.New("phone readiness check failed")
	}
	if e = g.Save(); e != nil {
		g.client.Disconnect()
		return e
	}
	g.ready.Store(true)
	return nil
}
func (g *Google) Close() error {
	g.ready.Store(false)
	if g.client != nil {
		g.client.Disconnect()
	}
	return g.Save()
}
func (g *Google) Ready() bool {
	return g.ready.Load() && !g.healthFailure.Load() && !g.overflow.Load() && g.client != nil && g.client.IsConnected()
}
func peer(c *gmproto.Conversation) string {
	ids := map[string]bool{}
	for _, p := range c.GetParticipants() {
		if p.GetIsMe() || !p.GetIsVisible() {
			continue
		}
		id := p.GetID().GetParticipantID()
		if id == "" {
			return ""
		}
		ids[id] = true
	}
	if len(ids) != 1 {
		return ""
	}
	for id := range ids {
		return id
	}
	return ""
}
func eligible(c *gmproto.Conversation) bool {
	return c != nil && !c.GetIsGroupChat() && !c.GetReadOnly() && peer(c) != "" && c.GetDefaultOutgoingID() != ""
}
func (g *Google) LookupConversation(ctx context.Context, id string) (Conversation, error) {
	if !g.Ready() {
		return Conversation{}, ErrUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	c, err := g.client.GetConversation(ctx, id)
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			reason, hard := "reconnecting", false
			if credentialError(err) {
				reason, hard = "login_required", true
			}
			g.noteFailure(reason, hard)
			g.diagnose("conversation_lookup", err)
		}
		return Conversation{}, ErrUnavailable
	}
	if c.GetConversationID() != id {
		return Conversation{}, errors.New("ineligible_conversation")
	}
	g.mu.Lock()
	g.conversations[id] = c
	g.mu.Unlock()
	label := c.GetName()
	if label == "" {
		label = "Individual conversation"
	}
	return withListState(Conversation{ID: id, ParticipantID: peer(c), Label: label, Protocol: c.GetType().String(), Group: c.GetIsGroupChat(), Writable: eligible(c)}, c), nil
}
func (g *Google) Conversations(ctx context.Context) ([]Conversation, error) {
	g.listMu.Lock()
	defer g.listMu.Unlock()
	if !g.Ready() {
		return nil, errors.New("phone connection unavailable")
	}
	if time.Since(g.listed) < 30*time.Second {
		return append([]Conversation(nil), g.list...), nil
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	res, e := g.client.ListConversations(ctx, &gmproto.ListConversationsRequest{Count: 25, Folder: gmproto.ListConversationsRequest_INBOX})
	if e != nil {
		return nil, errors.New("conversation refresh failed")
	}
	out := []Conversation{}
	ids := make([]string, len(res.GetConversations()))
	for i, summary := range res.GetConversations() {
		ids[i] = summary.GetConversationID()
	}
	details, e := conversationDetails(ctx, ids, g.client.GetConversation)
	if e != nil {
		return nil, errors.New("conversation identity refresh failed")
	}
	for _, c := range details {
		label := c.GetName()
		if label == "" {
			label = "Individual conversation"
		}
		out = append(out, withListState(Conversation{ID: c.GetConversationID(), ParticipantID: peer(c), Label: label, Protocol: c.GetType().String(), Group: c.GetIsGroupChat(), Writable: eligible(c)}, c))
	}
	// Publish identities only after the entire refresh succeeds.
	g.mu.Lock()
	for _, c := range details {
		g.conversations[c.GetConversationID()] = c
	}
	g.mu.Unlock()
	g.list = out
	g.listed = time.Now()
	return append([]Conversation(nil), out...), nil
}

// Fetch independent details with bounded concurrency while preserving inbox order.
func conversationDetails(ctx context.Context, ids []string, fetch func(context.Context, string) (*gmproto.Conversation, error)) ([]*gmproto.Conversation, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	results := make([]*gmproto.Conversation, len(ids))
	errs := make([]error, len(ids))
	jobs := make(chan int, len(ids))
	for i := range ids {
		jobs <- i
	}
	close(jobs)
	var workers sync.WaitGroup
	for worker := 0; worker < min(4, len(ids)); worker++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for i := range jobs {
				if ctx.Err() != nil {
					return
				}
				c, err := fetch(ctx, ids[i])
				if err == nil && (c == nil || ids[i] == "" || c.GetConversationID() != ids[i]) {
					err = errors.New("conversation identity mismatch")
				}
				if err != nil {
					errs[i] = err
					cancel()
					return
				}
				results[i] = c
			}
		}()
	}
	workers.Wait()
	for _, err := range errs {
		if err != nil {
			return nil, err
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return results, nil
}
func (g *Google) Send(ctx context.Context, id, txn, text string) (Receipt, error) {
	return g.sendParts(ctx, id, txn, text, nil)
}
func (g *Google) SendFiles(ctx context.Context, id, txn, text string, files []media.File) (Receipt, error) {
	return g.sendParts(ctx, id, txn, text, files)
}
func (g *Google) sendParts(ctx context.Context, id, txn, text string, files []media.File) (Receipt, error) {
	if !g.Ready() {
		return Receipt{}, ErrNotSubmitted
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	parts := []*gmproto.MessageInfo{}
	if text != "" {
		parts = append(parts, &gmproto.MessageInfo{Data: &gmproto.MessageInfo_MessageContent{MessageContent: &gmproto.MessageContent{Content: text}}})
	}
	for _, file := range files {
		if ctx.Err() != nil || file.RemoteID == "" || len(file.Key) != 32 {
			return Receipt{}, ErrNotSubmitted
		}
		uploaded := outboundMedia(file)
		parts = append(parts, &gmproto.MessageInfo{Data: &gmproto.MessageInfo_MediaContent{MediaContent: uploaded}})
	}
	if ctx.Err() != nil {
		return Receipt{}, ErrNotSubmitted
	}

	c, e := g.client.GetConversation(ctx, id)
	if e != nil {
		return Receipt{}, ErrNotSubmitted
	}
	g.mu.Lock()
	known := g.conversations[id]
	settings := g.settings
	g.mu.Unlock()
	if !eligible(c) || known == nil || peer(c) != peer(known) || c.GetDefaultOutgoingID() != known.GetDefaultOutgoingID() {
		return Receipt{}, ErrNotSubmitted
	}
	sim := c.GetSimCard().GetSIMData().GetSIMPayload()
	if sim == nil && len(settings.GetSIMCards()) == 1 {
		sim = settings.GetSIMCards()[0].GetSIMData().GetSIMPayload()
	}
	if sim == nil {
		return Receipt{}, ErrNotSubmitted
	}
	if !g.Ready() {
		return Receipt{}, ErrNotSubmitted
	}
	payload := &gmproto.MessagePayload{ConversationID: id, ParticipantID: c.GetDefaultOutgoingID(), TmpID: txn, TmpID2: txn, MessageInfo: parts}
	res, e := g.client.SendMessage(ctx, &gmproto.SendMessageRequest{ConversationID: id, SIMPayload: sim, TmpID: txn, MessagePayload: payload})
	if e != nil {
		return Receipt{}, ErrUnknown
	}
	if res.GetStatus() != gmproto.SendMessageResponse_SUCCESS {
		return Receipt{}, ErrNotSubmitted
	}
	return Receipt{State: "submitted", MessageID: txn}, nil
}
func normalize(w *libgm.WrappedMessage, started time.Time) (Incoming, bool) {
	if w == nil || w.Message == nil || w.IsOld || w.GetMessageID() == "" || len(w.GetMessageInfo()) == 0 {
		return Incoming{}, false
	}
	outgoing := false
	status := ""
	switch w.GetMessageStatus().GetStatus() {
	case gmproto.MessageStatusType_INCOMING_COMPLETE, gmproto.MessageStatusType_INCOMING_DELIVERED, gmproto.MessageStatusType_INCOMING_DISPLAYED:
	case gmproto.MessageStatusType_OUTGOING_COMPLETE:
		outgoing = true
		status = "submitted"
	case gmproto.MessageStatusType_OUTGOING_DELIVERED:
		outgoing = true
		status = "delivered"
	case gmproto.MessageStatusType_OUTGOING_DISPLAYED:
		outgoing = true
		status = "read"
	default:
		return Incoming{}, false
	}
	at := time.UnixMicro(w.GetTimestamp())
	if at.Before(started) {
		return Incoming{}, false
	}
	text, files, ok := messageParts(w.Message)
	if !ok {
		return Incoming{}, false
	}
	return Incoming{ID: w.GetMessageID(), ConversationID: w.GetConversationID(), ParticipantID: w.GetParticipantID(), Text: text, Attachments: files, At: at, Outgoing: outgoing, TransactionID: w.GetTmpID(), Status: status}, true
}
func (g *Google) Run(ctx context.Context, incoming func(Incoming), pause func(), receipt func(string, string, string, string), interrupted ...func() error) {
	timer := time.NewTicker(30 * time.Second)
	defer timer.Stop()
	recoveryTimer := time.NewTicker(time.Second)
	defer recoveryTimer.Stop()
	results := make(chan recoveryResult, 1)
	interruptionHandled := false
	var pausedEpoch uint64
	handleFailure := func() {
		failed, hard, epoch := g.failureSnapshot()
		if !failed {
			interruptionHandled = false
			return
		}
		if !interruptionHandled {
			interruptionHandled = true
			if len(interrupted) > 0 {
				if interrupted[0]() != nil {
					g.noteFailure("storage_error", true)
					pause()
				}
			} else {
				pause()
			}
		}
		if hard && pausedEpoch != epoch {
			pausedEpoch = epoch
			pause()
		}
	}
	recoveringOverflow := false
	for {
		handleFailure()
		g.startRecovery(ctx, time.Now(), results)
		if g.overflow.Load() {
			if !recoveringOverflow {
				g.beginOverflowRecovery(pause)
				recoveringOverflow = true
			}
			if g.finishOverflowRecovery() {
				recoveringOverflow = false
				g.diagnose("event_queue_drained", nil)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-recoveryTimer.C:
			if g.client != nil && !g.client.IsConnected() && !g.healthFailure.Load() {
				g.noteFailure("reconnecting", false)
			}
		case <-g.recoveryWake:
		case result := <-results:
			recovered, hard := g.finishRecovery(result, time.Now())
			if recovered {
				interruptionHandled = false
				g.diagnose("connection_recovered", nil)
			} else if hard {
				pause()
				g.diagnose("recovery_blocked", result.err)
			} else if result.err != nil {
				g.diagnose("recovery_retry", result.err)
			}
		case <-g.overflowWake:
			// The next iteration pauses and drains; the callback stays nonblocking.
		case <-timer.C:

			if g.Save() != nil {
				g.diagnose("periodic_save_failed", errors.New("save_failed"))
				g.noteFailure("storage_error", true)
				pause()
			}
		case event := <-g.updates:
			if wrapped, ok := event.(connectionEvent); ok {
				event = wrapped.event
			} else {
				if _, reason, hard, _, failed := connectionFailure(event); failed {
					g.noteFailure(reason, hard)
				}
			}
			if name, _, _, err, failed := connectionFailure(event); failed {
				handleFailure()
				g.diagnose(name, err)
				continue
			}
			switch e := event.(type) {
			case *events.AuthTokenRefreshed:
				if g.persistRefresh() != nil {
					g.noteFailure("storage_error", true)
					pause()
				}
			case *gmproto.Settings:
				g.mu.Lock()
				g.settings = e
				g.mu.Unlock()
			case *gmproto.Conversation:
				g.mu.Lock()
				old := g.conversations[e.GetConversationID()]
				changed := old != nil && (!eligible(e) || peer(old) != peer(e) || old.GetDefaultOutgoingID() != e.GetDefaultOutgoingID())
				g.mu.Unlock()
				if changed {
					pause()
				}
			case *libgm.WrappedMessage:
				status := e.GetMessageStatus().GetStatus()
				state := ""
				switch status {
				case gmproto.MessageStatusType_OUTGOING_COMPLETE:
					state = "submitted"
				case gmproto.MessageStatusType_OUTGOING_DELIVERED:
					state = "delivered"
				case gmproto.MessageStatusType_OUTGOING_DISPLAYED:
					state = "read"
				}
				if state != "" && e.GetTmpID() != "" {
					receipt(e.GetConversationID(), e.GetTmpID(), state, status.String())
				}
				if m, ok := normalize(e, g.started); ok && !g.overflow.Load() && !g.healthFailure.Load() {
					incoming(m)
				} else if !g.overflow.Load() {
					if item, ok := historicalText(e.Message, e.GetConversationID()); ok {
						incoming(Incoming{ID: item.ID, ConversationID: e.GetConversationID(), ParticipantID: e.GetParticipantID(), Text: item.Text, Attachments: item.Attachments, At: item.At, Historical: true, Outgoing: item.Direction == "outgoing", TransactionID: item.TransactionID, Status: item.Status})
					}
				}
			case *events.AccountChange:
				// libgm synthesizes an account snapshot during startup. It is not
				// an account switch; actual changes still require owner recovery.
				if e.IsFake {
					g.diagnose("account_snapshot", nil)
					continue
				}

			}
		}
	}
}

// withListState copies the phone's ordering hints. The library reports the last
// message time in microseconds; smaller values are treated as milliseconds.
func withListState(out Conversation, c *gmproto.Conversation) Conversation {
	out.Unread = c.GetUnread()
	out.Pinned = c.GetPinned()
	if ts := c.GetLastMessageTimestamp(); ts > 0 {
		var t time.Time
		if ts > 1e14 {
			t = time.UnixMicro(ts)
		} else {
			t = time.UnixMilli(ts)
		}
		out.LastMessageAt = t.UTC().Format(time.RFC3339)
	}
	return out
}
