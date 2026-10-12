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
	"smsbot-server/internal/vault"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Google is opt-in and never instantiated by fixture mode.
type Google struct {
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
	started       time.Time
	saveMu        sync.Mutex
	Diagnostic    func(AuthDiagnostic)
}

func NewGoogle(v *vault.Vault, path string) *Google {
	return &Google{vault: v, path: path, updates: make(chan any, 256), conversations: map[string]*gmproto.Conversation{}}
}
func (g *Google) attach(auth *libgm.AuthData) {
	g.client = libgm.NewClient(auth, nil, zerolog.New(io.Discard), exhttp.SensibleClientSettings)
	g.client.SetEventHandler(func(event any) {
		select {
		case g.updates <- event:
		default:
			g.ready.Store(false)
		}
	})
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
func (g *Google) Ready() bool { return g.ready.Load() && g.client != nil && g.client.IsConnected() }
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
		out = append(out, Conversation{ID: c.GetConversationID(), ParticipantID: peer(c), Label: label, Protocol: c.GetType().String(), Group: c.GetIsGroupChat(), Writable: eligible(c)})
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
	if !g.Ready() {
		return Receipt{}, ErrNotSubmitted
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
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
	payload := &gmproto.MessagePayload{ConversationID: id, ParticipantID: c.GetDefaultOutgoingID(), TmpID: txn, TmpID2: txn, MessageInfo: []*gmproto.MessageInfo{{Data: &gmproto.MessageInfo_MessageContent{MessageContent: &gmproto.MessageContent{Content: text}}}}}
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
	switch w.GetMessageStatus().GetStatus() {
	case gmproto.MessageStatusType_INCOMING_COMPLETE, gmproto.MessageStatusType_INCOMING_DELIVERED, gmproto.MessageStatusType_INCOMING_DISPLAYED:
	default:
		return Incoming{}, false
	}
	at := time.UnixMicro(w.GetTimestamp())
	if at.Before(started) {
		return Incoming{}, false
	}
	var text strings.Builder
	for _, p := range w.GetMessageInfo() {
		if p.GetMessageContent() == nil {
			return Incoming{}, false
		}
		text.WriteString(p.GetMessageContent().GetContent())
	}
	return Incoming{ID: w.GetMessageID(), ConversationID: w.GetConversationID(), ParticipantID: w.GetParticipantID(), Text: text.String(), At: at}, true
}
func (g *Google) Run(ctx context.Context, incoming func(Incoming), pause func(), receipt func(string, string, string, string)) {
	timer := time.NewTicker(30 * time.Second)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			if !g.ready.Load() {
				pause()
			}
			if g.Save() != nil {
				g.diagnose("periodic_save_failed", errors.New("save_failed"))
				g.ready.Store(false)
				pause()
			}
		case event := <-g.updates:
			switch e := event.(type) {
			case *events.AuthTokenRefreshed:
				if g.persistRefresh() != nil {
					pause()
				}
			case *events.ListenFatalError:
				g.diagnose("listen_fatal", e.Error)
				g.ready.Store(false)
				pause()
			case *events.ListenTemporaryError:
				g.diagnose("listen_temporary", e.Error)
				g.ready.Store(false)
				pause()
			case *events.PingFailed:
				g.diagnose("ping_failed", e.Error)
				g.ready.Store(false)
				pause()
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
				if m, ok := normalize(e, g.started); ok {
					incoming(m)
				}
			case *events.GaiaLoggedOut, *events.AccountChange, *events.PhoneNotResponding, *events.NoDataReceived, *events.HackySetActiveMayFail:
				g.diagnose("authentication_or_phone_unavailable", errors.New("unavailable"))
				g.ready.Store(false)
				pause()
			}
		}
	}
}
