// Package notify sends Web Push notifications to the owner's devices. It only
// reads what the service has already recorded, so a push failure never
// affects receiving or sending messages.
package notify

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	webpush "github.com/SherClockHolmes/webpush-go"
	"github.com/deusical/smsbot-server/internal/store"
	"github.com/deusical/smsbot-server/internal/transport"
)

const (
	// Messages older than this when first seen are catch-up after an outage
	// or history import, not news.
	freshFor = 10 * time.Minute
	// A phone outage shorter than this is a normal reconnect.
	outageAfter = 2 * time.Minute
)

// Payload is what the service worker shows.
type Payload struct {
	Kind  string `json:"kind"`
	Title string `json:"title"`
	Body  string `json:"body"`
	Tag   string `json:"tag"`
	URL   string `json:"url"`
}

// Sender delivers one payload to one device. gone reports that the push
// service no longer knows the device, which is then forgotten.
type Sender func(ctx context.Context, sub store.PushSubscription, payload []byte, urgent bool) (gone bool, err error)

type Service struct {
	Store     *store.Store
	Transport transport.Transport
	// Subject is the VAPID contact: the dashboard origin.
	Subject  string
	Send     Sender
	Now      func() time.Time
	Interval time.Duration

	keyMu   sync.Mutex
	started bool
	cursor  int64
	jobs    map[string]string
	// notified remembers messages already announced while they are fresh, so
	// a later edit or reaction to one does not announce it again.
	notified  map[string]time.Time
	downSince time.Time
	down      bool
}

func New(s *store.Store, t transport.Transport, subject string) *Service {
	n := &Service{Store: s, Transport: t, Subject: subject, Now: time.Now, Interval: 3 * time.Second, jobs: map[string]string{}, notified: map[string]time.Time{}}
	n.Send = n.webPush
	return n
}

type vapid struct {
	Public  string `json:"public"`
	Private string `json:"private"`
}

// PublicKey is the application server key browsers subscribe with.
func (n *Service) PublicKey() (string, error) {
	k, err := n.keys()
	return k.Public, err
}

func (n *Service) keys() (vapid, error) {
	n.keyMu.Lock()
	defer n.keyMu.Unlock()
	var k vapid
	b, err := n.Store.GetSecretSetting("push_vapid", "push_vapid")
	if err != nil {
		return k, err
	}
	if b != nil {
		return k, json.Unmarshal(b, &k)
	}
	if k.Private, k.Public, err = webpush.GenerateVAPIDKeys(); err != nil {
		return k, err
	}
	if b, err = json.Marshal(k); err != nil {
		return k, err
	}
	return k, n.Store.SetSecretSetting("push_vapid", "push_vapid", b)
}

func (n *Service) webPush(ctx context.Context, sub store.PushSubscription, payload []byte, urgent bool) (bool, error) {
	k, err := n.keys()
	if err != nil {
		return false, err
	}
	urgency := webpush.UrgencyNormal
	if urgent {
		urgency = webpush.UrgencyHigh
	}
	res, err := webpush.SendNotificationWithContext(ctx, payload, &webpush.Subscription{Endpoint: sub.Endpoint, Keys: webpush.Keys{Auth: sub.Auth, P256dh: sub.P256dh}}, &webpush.Options{
		HTTPClient:      &http.Client{Timeout: 15 * time.Second},
		Subscriber:      n.Subject,
		VAPIDPublicKey:  k.Public,
		VAPIDPrivateKey: k.Private,
		TTL:             24 * 60 * 60,
		Urgency:         urgency,
	})
	if err != nil {
		return false, err
	}
	res.Body.Close()
	switch {
	case res.StatusCode == http.StatusNotFound || res.StatusCode == http.StatusGone:
		return true, nil
	case res.StatusCode >= 300:
		return false, errors.New("push_rejected")
	}
	return false, nil
}

// ValidEndpoint accepts only HTTPS push service URLs, so a subscription can
// never point the server at itself or the local network.
func ValidEndpoint(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || len(raw) > 2048 {
		return false
	}
	host := strings.ToLower(u.Hostname())
	return strings.Contains(host, ".") && !strings.HasSuffix(host, ".local") && !strings.HasSuffix(host, ".internal") && !strings.HasSuffix(host, ".ts.net") && strings.Trim(host, "0123456789.:[]") != ""
}

func (n *Service) Run(ctx context.Context) {
	t := time.NewTicker(n.Interval)
	defer t.Stop()
	for {
		_ = n.Tick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Tick checks once for news. The first tick only records where things stand.
func (n *Service) Tick(ctx context.Context) error {
	subs, err := n.Store.PushSubscriptions()
	if err != nil {
		return err
	}
	first := !n.started
	if first {
		if n.cursor, err = n.Store.LatestMessageChange(); err != nil {
			return err
		}
		n.started = true
	}
	if err = n.messages(ctx, subs); err != nil {
		return err
	}
	if err = n.alerts(ctx, subs, first); err != nil {
		return err
	}
	n.connection(ctx, subs)
	return nil
}

func (n *Service) messages(ctx context.Context, subs []store.PushSubscription) error {
	for {
		batch, next, err := n.Store.MessageChangesAfter(n.cursor, 200)
		if err != nil || next == n.cursor {
			return err
		}
		n.cursor = next
		for id, at := range n.notified {
			if n.Now().Sub(at) > 2*freshFor {
				delete(n.notified, id)
			}
		}
		for _, m := range batch {
			at, err := time.Parse(time.RFC3339Nano, m.At)
			if _, seen := n.notified[m.ID]; err != nil || seen || n.Now().Sub(at) > freshFor {
				continue
			}
			n.notified[m.ID] = at
			enrolled := false
			if _, ok := n.Store.EnrolledParticipant(m.Conversation); ok {
				enrolled = true
			}
			title := n.label(m.Conversation)
			for _, sub := range subs {
				if !wantsMessage(sub.Prefs, m.Conversation, enrolled) {
					continue
				}
				body := "New message"
				if sub.Prefs.ShowText {
					body = preview(m.Text, m.Attachments)
				}
				n.deliver(ctx, sub, Payload{Kind: "message", Title: title, Body: body, Tag: "conversation:" + m.Conversation, URL: "/?open=conversation&id=" + url.QueryEscape(m.Conversation)}, true)
			}
		}
	}
}

func wantsMessage(p store.NotificationPrefs, conv string, enrolled bool) bool {
	if slices.Contains(p.Muted, conv) {
		return false
	}
	switch p.Messages {
	case "all":
		return true
	case "bot_on":
		return enrolled
	case "chosen":
		return slices.Contains(p.Conversations, conv)
	}
	return false
}

func preview(text string, attachments bool) string {
	text = strings.Join(strings.Fields(text), " ")
	if utf8.RuneCountInString(text) > 180 {
		text = string([]rune(text)[:179]) + "…"
	}
	switch {
	case text == "" && attachments:
		return "Sent an attachment"
	case text == "":
		return "New message"
	case attachments:
		return text + " (attachment)"
	}
	return text
}

func (n *Service) alerts(ctx context.Context, subs []store.PushSubscription, first bool) error {
	jobs, err := n.Store.AlertJobs()
	if err != nil {
		return err
	}
	current := map[string]string{}
	for _, j := range jobs {
		current[j.ID] = j.State
		if n.jobs[j.ID] == j.State || first {
			continue
		}
		p := Payload{Kind: "alert", Tag: "job:" + j.ID, URL: "/?open=activity", Body: j.Command + " in " + n.label(j.Conversation)}
		approval := j.State == "awaiting_approval"
		switch j.State {
		case "awaiting_approval":
			p.Title = "Approval needed"
		case "unknown_submission":
			p.Title = "Send outcome uncertain"
		default:
			p.Title = "Reply failed"
		}
		for _, sub := range subs {
			if approval && sub.Prefs.Approvals || !approval && sub.Prefs.Problems {
				n.deliver(ctx, sub, p, approval)
			}
		}
	}
	n.jobs = current
	return nil
}

func (n *Service) connection(ctx context.Context, subs []store.PushSubscription) {
	if !n.Transport.Ready() {
		if n.downSince.IsZero() {
			n.downSince = n.Now()
		}
		if n.down || n.Now().Sub(n.downSince) < outageAfter {
			return
		}
		n.down = true
		n.connectionAlert(ctx, subs, Payload{Kind: "alert", Title: "Phone disconnected", Body: "SmsBot cannot reach your phone. Messages and replies wait until it reconnects.", Tag: "connection", URL: "/?open=home"})
		return
	}
	if n.down {
		n.connectionAlert(ctx, subs, Payload{Kind: "alert", Title: "Phone reconnected", Body: "SmsBot can reach your phone again.", Tag: "connection", URL: "/?open=home"})
	}
	n.down, n.downSince = false, time.Time{}
}

func (n *Service) connectionAlert(ctx context.Context, subs []store.PushSubscription, p Payload) {
	for _, sub := range subs {
		if sub.Prefs.Connection {
			n.deliver(ctx, sub, p, false)
		}
	}
}

// Test sends a sample notification to one device.
func (n *Service) Test(ctx context.Context, sub store.PushSubscription) error {
	b, err := json.Marshal(Payload{Kind: "test", Title: "SmsBot notifications are on", Body: "This device will be notified as you configured.", Tag: "test", URL: "/?open=settings"})
	if err != nil {
		return err
	}
	gone, err := n.Send(ctx, sub, b, true)
	if gone {
		_, _ = n.Store.DeletePushSubscription(sub.ID)
		return errors.New("device_unsubscribed")
	}
	return err
}

func (n *Service) deliver(ctx context.Context, sub store.PushSubscription, p Payload, urgent bool) {
	b, err := json.Marshal(p)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	// Failures are dropped: a missed notification must never hold up the
	// loop, and neither the content nor the endpoint is logged.
	if gone, _ := n.Send(ctx, sub, b, urgent); gone {
		_, _ = n.Store.DeletePushSubscription(sub.ID)
	}
}

func (n *Service) label(conv string) string {
	if a, err := n.Store.Archive(conv); err == nil && a.Conversation.Label != "" {
		return a.Conversation.Label
	}
	if c, ok := n.Transport.(transport.CachedLookup); ok {
		if v, ok := c.CachedConversation(conv); ok && v.Label != "" {
			return v.Label
		}
	}
	return "New message"
}
