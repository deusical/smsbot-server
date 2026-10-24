package transport

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/deusical/smsbot-server/internal/vault"
	"github.com/rs/zerolog"
	"go.mau.fi/mautrix-gmessages/pkg/libgm"
	"go.mau.fi/mautrix-gmessages/pkg/libgm/gmproto"
	"go.mau.fi/util/exhttp"
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
	// Display/discovery hints have no TTL and never authorize phone operations.
	discoveryDetails map[string]*gmproto.Conversation
	updates          chan any
	queueMu          sync.Mutex
	overflow         atomic.Bool
	healthFailure    atomic.Bool
	overflowWake     chan struct{}
	started          time.Time
	saveMu           sync.Mutex
	Diagnostic       func(AuthDiagnostic)
	// RequestLog, when set, receives every request the adapter sends to the phone.
	RequestLog func(PhoneRequestLog)
	// Reconnected, when set, runs after a connection outage ends (see OutagesSince).
	Reconnected   func()
	outages       outageLog
	discovery     conversationPageCache
	phoneRequests phoneRequests
	phonePurposes phoneRequests
	relayMetrics  relayMetrics
	activity      activityState
	identityEpoch atomic.Uint64
}

func NewGoogle(v *vault.Vault, path string) *Google {
	return &Google{
		recoveryWake:  make(chan struct{}, 1),
		vault:         v,
		path:          path,
		updates:       make(chan any, 256),
		overflowWake:  make(chan struct{}, 1),
		conversations: map[string]*gmproto.Conversation{},
	}
}
func (g *Google) attach(auth *libgm.AuthData) {
	settings := exhttp.SensibleClientSettings
	settings.TransportOverride = func(cs exhttp.ClientSettings) http.RoundTripper {
		return &relayTransport{next: cs.Configure(&http.Transport{}), metrics: &g.relayMetrics}
	}
	g.client = libgm.NewClient(auth, nil, zerolog.New(io.Discard), settings)
	g.client.SetEventHandler(g.enqueue)
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
	ctx = WithPhonePurpose(ctx, "readiness")
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
	g.countPhone(ctx, phoneProbe)
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
