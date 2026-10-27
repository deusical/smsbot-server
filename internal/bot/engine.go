package bot

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/deusical/smsbot-server/internal/collection"
	"github.com/deusical/smsbot-server/internal/media"
	"github.com/deusical/smsbot-server/internal/store"
	"github.com/deusical/smsbot-server/internal/transport"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Engine routes commands, generates replies and dispatches sends. It reads
// collection consent through Collection but owns none of collection's work.
type Engine struct {
	Collection *collection.Service
	Store      *store.Store
	Transport  transport.Transport
	Provider   Provider
	// mu is the control lock shared with Collection: send decisions serialize
	// with consent changes and protected commits.
	mu         *sync.Mutex
	preflight  preflightRetry
	dispatchMu sync.Mutex
	now        func() time.Time
	pending    map[string]context.CancelFunc
}

// New wires an engine without touching storage or starting work. control is
// the lock shared with the collection service. Run Init before use.
func New(s *store.Store, t transport.Transport, c *collection.Service, control *sync.Mutex) *Engine {
	var provider Provider = NewOpenRouter()
	if _, offline := t.(*transport.Fixture); offline {
		provider = SimulatedProvider{}
	}
	return &Engine{Collection: c, Store: s, Transport: t, Provider: provider, mu: control, now: time.Now, pending: map[string]context.CancelFunc{}}
}

// Init performs the engine's one-time startup work: offline fixture mode
// seeds its simulated model catalog.
func (e *Engine) Init() error {
	if _, offline := e.Transport.(*transport.Fixture); !offline {
		return nil
	}
	var models []Model
	if err := e.Store.GetPublicSetting("model_catalog", &models); err != nil {
		return err
	}
	if len(models) > 0 {
		return nil
	}
	models, _ = e.Provider.Models(context.Background())
	return e.Store.SetPublicSetting("model_catalog", models)
}

// CollectionSettingsSaved reacts to new collection settings. The collection
// service calls it under the control lock: turning automatic replies off
// cancels in-flight generation and upload work (queued jobs are cancelled in
// the same transaction that saved the settings).
func (e *Engine) CollectionSettingsSaved(s collection.Settings) {
	if s.AutomaticReplies {
		return
	}
	for _, cancel := range e.pending {
		cancel()
	}
}

func (e *Engine) lookupConversation(ctx context.Context, id string) (transport.Conversation, error) {
	return transport.Lookup(ctx, e.Transport, id)
}

func (e *Engine) Pause(value bool) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if value {
		for _, cancel := range e.pending {
			cancel()
		}
	}
	return e.Store.Pause(value)
}

// ConnectionInterrupted cancels unsent work without changing the owner's Live/Pause choice.
// Submitted or uncertain intents retain their state and are never retried here.
func (e *Engine) ConnectionInterrupted() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, cancel := range e.pending {
		cancel()
	}
	return e.Store.CancelUnsentJobs("connection_interrupted")
}

func (e *Engine) Unenroll(id string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	for job, cancel := range e.pending {
		j, _, err := e.Store.JobPlan(job)
		if err == nil && j.ConversationID == id {
			cancel()
		}
	}
	if err := e.Store.Unenroll(id); err != nil {
		return err
	}
	return e.Store.ClearMemory(id)
}
func (e *Engine) Enroll(id, peer string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.Store.Enroll(id, peer)
}
func (e *Engine) Cancel(id string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if cancel := e.pending[id]; cancel != nil {
		cancel()
	}
	return e.Store.Cancel(id)
}
func (e *Engine) Approve(id string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	j, p, err := e.Store.JobPlan(id)
	if err != nil {
		return errors.New("approval_unavailable")
	}
	if j.State != "awaiting_approval" {
		return errors.New("approval_unavailable")
	}
	at, err := time.Parse(time.RFC3339Nano, j.CreatedAt)
	if err != nil || e.now().Sub(at) > 2*time.Minute {
		e.Store.Complete(id, "expired", "approval_expired")
		return errors.New("approval_expired")
	}
	paused, err := e.Store.Paused()
	if err != nil {
		return err
	}
	if paused || !e.Store.Enrolled(j.ConversationID, p.Participant) {
		return errors.New("approval_unavailable")
	}
	p.Approved = true
	if err = e.Store.SavePlan(id, p); err != nil {
		return err
	}
	ok, err := e.Store.Transition(id, "awaiting_approval", "queued")
	if err == nil && !ok {
		return errors.New("approval_unavailable")
	}
	return err
}
func (e *Engine) validConversation(ctx context.Context, conv, peer string) (bool, error) {
	if _, ok := e.Transport.(transport.ConversationLookup); ok {
		c, err := e.lookupConversation(ctx, conv)
		return err == nil && c.ParticipantID == peer && !c.Group && c.Writable, err
	}
	list, err := e.Transport.Conversations(ctx)
	if err != nil {
		return false, err
	}
	for _, c := range list {
		if c.ID == conv && c.ParticipantID == peer && !c.Group && c.Writable {
			return true, nil
		}
	}
	return false, nil
}

// Accept turns a fresh command into a queued job. Each step can decline the
// message; only an accepted command is enqueued and saved.
func (e *Engine) Accept(ctx context.Context, m transport.Incoming) (bool, error) {
	text := strings.TrimSpace(m.Text)
	parent, candidate, err := e.commandCandidate(m, text)
	if err != nil || !candidate {
		return false, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	cfg, peer, ok, err := e.admit(ctx, m, parent)
	if err != nil || !ok {
		return false, err
	}
	id := store.Hash(m.ConversationID + "\x00" + m.ID)
	exists, err := e.Store.JobExists(id)
	if err != nil || exists {
		return false, err
	}
	name, input, ok := parseCommand(text)
	if !ok && text == "" && parent != nil {
		// A photo with no text, replying to an AI answer, continues that thread.
		name, input, ok = "ask", "", true
	}
	if !ok {
		return false, nil
	}
	if name != "ask" && name != "deepseek" {
		// Only an ask can continue an AI thread.
		parent = nil
	}
	limitOK, err := e.Store.Limits(e.now().UTC(), cfg.Settings.HourlyLimit, cfg.Settings.DailyLimit, 0, 0)
	if err != nil || !limitOK {
		return false, err
	}
	p := store.Plan{Participant: peer, MaxChars: cfg.Settings.MaxChars, Owner: m.Outgoing}
	reply, builtin, ok, err := e.builtinReply(m.ConversationID, name, input, cfg)
	if err != nil || !ok {
		return false, err
	}
	if builtin {
		p.Reply = reply
	} else if name, ok, err = e.planCommand(ctx, m, &p, cfg, name, input, parent); err != nil || !ok {
		return false, err
	}
	return e.record(m, id, name, text, p)
}

// commandCandidate reports whether a message could be a command: text starting
// with "!", or a native RCS reply to one of the bot's AI answers (which may be
// just a photo).
func (e *Engine) commandCandidate(m transport.Incoming, text string) (*store.AIParent, bool, error) {
	if text == "" && ownImage(m) == nil {
		return nil, false, nil
	}
	var parent *store.AIParent
	if m.Interaction != nil && m.Interaction.ReplyTo != "" && strings.Contains(strings.ToUpper(m.Interaction.Protocol), "RCS") {
		var err error
		parent, err = e.Store.FindAIParent(m.ConversationID, m.Interaction.ReplyTo)
		if err != nil {
			return nil, false, err
		}
	}
	return parent, strings.HasPrefix(text, "!") || parent != nil, nil
}

// admit applies every rule about who may run a command, and when. It returns
// the configuration and the enrolled peer the command is planned for.
func (e *Engine) admit(ctx context.Context, m transport.Incoming, parent *store.AIParent) (Configuration, string, bool, error) {
	var cfg Configuration
	age := e.now().Sub(m.At)
	if len(m.Attachments) > 0 && ownImage(m) == nil || m.ID == "" || m.Historical || age > 2*time.Minute || age < -5*time.Second || len(m.Text) > 12000 {
		return cfg, "", false, nil
	}
	paused, err := e.Store.Paused()
	if err != nil || paused || !e.Transport.Ready() {
		return cfg, "", false, err
	}
	if cfg, err = e.Configuration(); err != nil {
		return cfg, "", false, err
	}
	peer := m.ParticipantID
	if m.Outgoing {
		var ok bool
		if peer, ok, err = e.ownerPeer(m, cfg, parent); err != nil || !ok {
			return cfg, "", false, err
		}
	}
	if !e.Store.Enrolled(m.ConversationID, peer) {
		return cfg, "", false, nil
	}
	inbox, err := e.Collection.Settings()
	if err != nil || !inbox.AutomaticReplies {
		return cfg, "", false, err
	}
	valid, err := e.validConversation(ctx, m.ConversationID, peer)
	if err != nil || !valid || quiet(cfg.Settings, e.now()) {
		return cfg, "", false, err
	}
	return cfg, peer, true, nil
}

// ownerPeer handles a command the owner sent. The owner's own messages carry
// the owner's participant, so the enrolled peer stands in for every identity
// check that follows. The bot's own replies never run commands.
func (e *Engine) ownerPeer(m transport.Incoming, cfg Configuration, parent *store.AIParent) (string, bool, error) {
	if !cfg.Settings.OwnerCommands {
		return "", false, nil
	}
	peer, ok := e.Store.EnrolledParticipant(m.ConversationID)
	if !ok {
		return "", false, nil
	}
	var ours bool
	var err error
	if parent != nil {
		// A native reply is an explicit follow-up. A genuine owner message can
		// repeat an earlier answer, so only exact provenance identifies an echo.
		ours, err = e.Store.SentByBotIdentity(m.ConversationID, m.TransactionID, m.ID)
	} else {
		ours, err = e.Store.SentByBot(m.ConversationID, m.TransactionID, m.Text)
	}
	if err != nil || ours {
		return "", false, err
	}
	return peer, true, nil
}

// parseCommand splits "!name input". Text without "!" is a plain follow-up
// to an AI answer, handled as an ask.
func parseCommand(text string) (name, input string, ok bool) {
	parts := strings.Fields(text)
	if len(parts) == 0 {
		return "", "", false
	}
	if !strings.HasPrefix(parts[0], "!") {
		return "ask", text, true
	}
	return strings.TrimPrefix(parts[0], "!"), strings.TrimSpace(strings.TrimPrefix(text, parts[0])), true
}

// builtinReply answers the built-in commands, which take no input. builtin is
// false for any other name; ok is false when a built-in was misused.
func (e *Engine) builtinReply(conv, name, input string, cfg Configuration) (reply string, builtin, ok bool, err error) {
	switch name {
	case "ping", "help", "status", "new", "more":
	default:
		return "", false, true, nil
	}
	if input != "" {
		return "", true, false, nil
	}
	switch name {
	case "ping":
		return "pong", true, true, nil
	case "help":
		names := []string{"!ping", "!help", "!status", "!new"}
		for _, cmd := range cfg.Commands {
			if cmd.Enabled && allowed(cmd, conv) {
				names = append(names, "!"+cmd.Name)
			}
		}
		return "Commands: " + strings.Join(names, ", "), true, true, nil
	case "status":
		return "Bot active. Default model: " + cfg.Settings.DefaultModel, true, true, nil
	case "new":
		if err = e.Store.ClearMemory(conv); err != nil {
			return "", true, false, err
		}
		e.cancelClearedThreadRuns()
		return "Conversation memory cleared.", true, true, nil
	default: // more
		return "Paired replies are sent in full, subject to the configured character limit.", true, true, nil
	}
}

// planCommand fills the plan for a configured fixed or AI command and returns
// its canonical name.
func (e *Engine) planCommand(ctx context.Context, m transport.Incoming, p *store.Plan, cfg Configuration, name, input string, parent *store.AIParent) (string, bool, error) {
	cmd, model, instructions, g, ok := resolve(cfg, name)
	if !ok || !allowed(cmd, m.ConversationID) {
		return "", false, nil
	}
	name = cmd.Name
	// Only the canonical AI ask uses explicit threads. Other command memory stays unchanged.
	threaded := cmd.Name == "ask" && cmd.Type == "ai"
	if parent != nil && (!threaded || !cmd.Memory) {
		return "", false, nil
	}
	p.Memory = cmd.Memory
	if p.Memory {
		var err error
		if p.MemoryEpoch, err = e.Store.MemoryEpoch(m.ConversationID); err != nil {
			return "", false, err
		}
		if !threaded {
			if p.Context, err = e.Store.Memory(memoryScope(m.ConversationID, "!"+name, model), e.now()); err != nil {
				return "", false, err
			}
		}
	}
	p.MemoryHours = cfg.Settings.MemoryHours
	p.Approval = cmd.Approval
	p.Input = input
	if cmd.Type == "fixed" {
		p.Reply = strings.ReplaceAll(instructions, "{input}", input)
		return name, true, nil
	}
	ok, err := e.planAI(ctx, m, p, cfg, cmd, model, instructions, g, input, threaded, parent)
	return name, ok, err
}

// planAI selects the model for an AI command and, for !ask, its thread.
func (e *Engine) planAI(ctx context.Context, m transport.Incoming, p *store.Plan, cfg Configuration, cmd Command, model, instructions string, g Generation, input string, threaded bool, parent *store.AIParent) (bool, error) {
	if model == "" {
		return false, nil
	}
	selectedModel, err := e.model(model)
	if err != nil {
		return false, err
	}
	if !e.ProviderConfiguredFor(selectedModel) {
		return false, nil
	}
	if err = validateModel(g, selectedModel); err != nil {
		return false, err
	}
	p.Model = model
	p.Instructions = instructions
	p.Generation, _ = json.Marshal(g)
	own := ownImage(m)
	if own != nil {
		reply := photoUnreadable(*own, selectedModel, cfg.Settings.AIPhotoMaxMB)
		if reply == "" && !e.photoSaved(m.ConversationID) {
			reply = "I can only read photos in conversations you archive. Turn that on in Settings, or reply to the photo with !ask once it's saved."
		}
		if reply != "" {
			p.Model = ""
			p.Memory = false
			p.Reply = reply
			return true, nil
		}
		input = ownPhotoPrompt(input)
		p.Input = input
		p.Image = &store.ImageRef{Wire: m.ID}
	}
	if parent == nil {
		if quoted, photo, ok := e.quotedInput(m, input, own != nil); ok {
			// A bare "!ask" on a quoted message responds to that message.
			p.Input, input = quoted, quoted
			if photo != nil && own == nil {
				if reply := photoUnreadable(*photo, selectedModel, cfg.Settings.AIPhotoMaxMB); reply != "" {
					p.Model = ""
					p.Memory = false
					p.Reply = reply
					return true, nil
				}
				p.Image = &photo.ImageRef
			}
		}
	}
	if !threaded {
		return true, nil
	}
	if input == "" {
		p.Model = ""
		p.Memory = false
		p.Reply = "Send !ask followed by a question. Reply to an AI answer to continue it."
		return true, nil
	}
	if cmd.Memory {
		if err = e.planAIThread(m, p, parent, selectedModel, g, cfg.Settings); err != nil {
			return false, err
		}
	}
	return true, nil
}

// record enqueues the job and saves the command message under it.
func (e *Engine) record(m transport.Incoming, id, name, text string, p store.Plan) (bool, error) {
	accepted, err := e.Store.Enqueue(id, m.ConversationID, "!"+name, p)
	if err != nil || !accepted {
		return accepted, err
	}
	if m.Outgoing {
		// The phone's copy of the owner's message is usually saved already,
		// under the dashboard send when it came from there.
		known, err := e.Store.MessageKnown(m.ConversationID, m.ID, "outgoing", m.TransactionID)
		if err != nil || known {
			return true, err
		}
		return true, e.Store.SaveMessage(id, m.ConversationID, "outgoing", text)
	}
	return true, e.Store.SaveMessage(id, m.ConversationID, "incoming", text)
}

const quoteLimit = 2000

// quotedInput adds the messages an AI command natively replies to, so a reply
// like "!ask what does this mean" can refer to them. The chain is followed up
// through each message's own reply, within the same conversation only; without
// it the question is sent alone. With no question, the AI is asked to respond
// to the last message. A photo on the message replied to directly is returned
// as a reference; its bytes are read only to generate.
func (e *Engine) quotedInput(m transport.Incoming, input string, ownPhoto bool) (string, *quotedPhoto, bool) {
	if m.Interaction == nil || m.Interaction.ReplyTo == "" || !strings.Contains(strings.ToUpper(m.Interaction.Protocol), "RCS") {
		return input, nil, false
	}
	chain, more, photo, err := e.replyChain(m, ownPhoto)
	if err != nil || len(chain) == 0 {
		return input, nil, false
	}
	var quote string
	last := chain[len(chain)-1]
	if len(chain) == 1 && !more {
		shown := "The photo is attached"
		if !last.attached {
			shown = "The photo isn't shown"
		}
		switch {
		case !last.photo:
			quote = "This question replies to an earlier message in the chat, sent by " + last.author + ":\n\"\"\"\n" + last.text + "\n\"\"\"\n\n"
		case last.text == "":
			quote = "This question replies to a photo in the chat, sent by " + last.author + ". " + shown + ".\n\n"
		default:
			quote = "This question replies to a photo in the chat, sent by " + last.author + ". " + shown + ", with this text:\n\"\"\"\n" + last.text + "\n\"\"\"\n\n"
		}
	} else {
		quote = "This question replies to a chain of messages in the chat, oldest first"
		if more {
			quote += " (earlier ones are left out)"
		}
		quote += ". It replies to the last one.\n\n"
		for i, q := range chain {
			quote += fmt.Sprintf("%d. Sent by %s", i+1, q.author)
			if q.photo {
				quote += ", a photo"
				if q.attached {
					quote += " (attached)"
				} else {
					quote += " (not shown)"
				}
			}
			if q.text != "" {
				quote += ":\n\"\"\"\n" + q.text + "\n\"\"\""
			}
			quote += "\n\n"
		}
	}
	if input == "" {
		if photo != nil {
			return quote + "No question was given. Describe or respond to the photo.", photo, true
		}
		return quote + "No question was given. Respond to that message.", nil, true
	}
	return quote + "Question: " + input, photo, true
}

// A reply chain is followed at most chainDepth messages up, and the chain's
// text is kept to about chainChars, dropping the oldest messages first.
const (
	chainDepth = 8
	chainChars = 6000
)

type chainMessage struct {
	author, text    string
	photo, attached bool
}

// replyChain returns the messages m replies to, oldest first. more reports that
// older messages exist but were left out. photo is the image on the message m
// replies to directly, if any. The bot's own answers are part of the chain.
func (e *Engine) replyChain(m transport.Incoming, ownPhoto bool) (chain []chainMessage, more bool, photo *quotedPhoto, err error) {
	seen := map[string]bool{}
	budget := chainChars
	wire := m.Interaction.ReplyTo
	for wire != "" {
		if len(chain) >= chainDepth || budget <= 0 {
			more = true
			break
		}
		id, direction, text, ok, err := e.Store.QuotedMessage(m.ConversationID, wire)
		if err != nil {
			return nil, false, nil, err
		}
		if !ok || seen[id] {
			break
		}
		seen[id] = true
		wire = ""
		if v, err := e.Store.Interaction(m.ConversationID, id); err != nil {
			return nil, false, nil, err
		} else if v != nil && strings.Contains(strings.ToUpper(v.Protocol), "RCS") {
			wire = v.ReplyTo
		}
		img, err := e.quotedPhoto(m.ConversationID, id)
		if err != nil {
			return nil, false, nil, err
		}
		text = strings.TrimSpace(text)
		if text == "" && img == nil {
			continue
		}
		if r := []rune(text); len(r) > quoteLimit {
			text = string(r[:quoteLimit]) + "…"
		}
		origin, _, err := e.Store.MessageOrigin(m.ConversationID, id)
		if err != nil {
			return nil, false, nil, err
		}
		author := "the other person in this chat"
		switch {
		case origin == "bot":
			author = "the AI assistant in this chat"
		case (direction == "outgoing") == m.Outgoing:
			author = "the person asking"
		}
		if len(chain) == 0 && !ownPhoto {
			photo = img
		}
		chain = append(chain, chainMessage{author: author, text: text, photo: img != nil, attached: len(chain) == 0 && img != nil && !ownPhoto})
		budget -= len([]rune(text))
	}
	for i, j := 0, len(chain)-1; i < j; i, j = i+1, j-1 {
		chain[i], chain[j] = chain[j], chain[i]
	}
	return chain, more, photo, nil
}

// A quoted photo is read up to maxPhotoMB. One over the owner's setting
// (defaultPhotoMB unless changed) is scaled to at most imageMaxSide pixels
// first. imageTokens is the allowance reserved for it in the context and
// spending estimates.
const (
	defaultPhotoMB = 5
	maxPhotoMB     = 25
	imageMaxSide   = 2048
	imageTokens    = 2000
)

// imageTypes are the photo formats every Chat Completions provider accepts.
var imageTypes = []string{"image/jpeg", "image/png", "image/gif", "image/webp"}

type quotedPhoto struct {
	store.ImageRef
	media.Info
}

// quotedPhoto returns the first image attached to a saved message, if any.
func (e *Engine) quotedPhoto(conv, message string) (*quotedPhoto, error) {
	files, err := e.Store.MessageFiles(conv, message)
	if err != nil {
		return nil, err
	}
	for _, f := range files {
		if strings.HasPrefix(f.MIME, "image/") {
			return &quotedPhoto{ImageRef: store.ImageRef{Message: message, Attachment: f.ID}, Info: f.Info}, nil
		}
	}
	return nil, nil
}

// ownImage is the first image attached to a message. A command may carry one:
// it, rather than a quoted photo, is what the AI is shown.
func ownImage(m transport.Incoming) *quotedPhoto {
	for _, f := range m.Attachments {
		if strings.HasPrefix(f.MIME, "image/") {
			return &quotedPhoto{ImageRef: store.ImageRef{Wire: m.ID}, Info: f.Info}
		}
	}
	return nil
}

// ownPhotoPrompt words the prompt for a command that came with its own photo.
func ownPhotoPrompt(input string) string {
	if input == "" {
		return "The person sent a photo with no message. Describe or respond to the attached photo."
	}
	return "(A photo is attached.) " + input
}

// photoSaved reports whether photos in conv are saved, which is how a command's
// own photo reaches the AI: the conversation must be archived or open in the
// dashboard.
func (e *Engine) photoSaved(conv string) bool {
	s, err := e.Collection.Settings()
	return err == nil && s.AllowsID(conv)
}

// waitForPhoto finds a command's own photo once it has been saved. Saving runs
// beside command handling, so it may need a moment.
func (e *Engine) waitForPhoto(ctx context.Context, conv, wire string) (message, attachment string, err error) {
	for attempt := 0; attempt < 10; attempt++ {
		id, _, _, ok, err := e.Store.QuotedMessage(conv, wire)
		if err != nil {
			return "", "", err
		}
		if ok {
			photo, err := e.quotedPhoto(conv, id)
			if err != nil {
				return "", "", err
			}
			if photo != nil {
				return id, photo.Attachment, nil
			}
		}
		select {
		case <-ctx.Done():
			return "", "", ctx.Err()
		case <-time.After(300 * time.Millisecond):
		}
	}
	return "", "", errors.New("image_unavailable")
}

// photoUnreadable explains why a quoted photo can't go to the model, or
// returns "" when it can. A photo over limitMB is shrunk later, except a WebP,
// which can't be.
func photoUnreadable(photo quotedPhoto, m Model, limitMB int) string {
	switch {
	case !readsImages(m):
		return "This model can't read images; pick one that can for this command."
	case !contains(imageTypes, photo.MIME):
		return "I can only read JPEG, PNG, GIF and WebP photos."
	case photo.Size > maxPhotoMB<<20:
		return fmt.Sprintf("That photo is too large for the AI; the limit is %d MB.", maxPhotoMB)
	case photo.MIME == "image/webp" && photo.Size > int64(limitMB)<<20:
		return fmt.Sprintf("That WebP photo is over %d MB and can't be shrunk; the limit is set in Settings.", limitMB)
	}
	return ""
}

// loadImage reads a planned photo for one generation, shrinking it when it is
// over limitMB. Caller doesn't hold the control lock: the photo may be
// downloaded from the phone.
func (e *Engine) loadImage(ctx context.Context, conv string, ref *store.ImageRef, limitMB int) (*Image, error) {
	if ref == nil {
		return nil, nil
	}
	message, attachment := ref.Message, ref.Attachment
	if ref.Wire != "" {
		var err error
		if message, attachment, err = e.waitForPhoto(ctx, conv, ref.Wire); err != nil {
			return nil, err
		}
	}
	data, err := e.Collection.QuotedFile(ctx, conv, message, attachment, maxPhotoMB<<20)
	if err != nil {
		return nil, err
	}
	mime := http.DetectContentType(data)
	if !contains(imageTypes, mime) {
		return nil, errors.New("image_unsupported")
	}
	if limit := int64(limitMB) << 20; int64(len(data)) > limit {
		if mime == "image/webp" {
			return nil, errors.New("image_too_large")
		}
		if data, err = media.Shrink(data, imageMaxSide); err != nil {
			return nil, err
		}
		mime = "image/jpeg"
		if int64(len(data)) > limit {
			return nil, errors.New("image_too_large")
		}
	}
	return &Image{MIME: mime, Data: data}, nil
}
func memoryScope(conv, command, model string) string {
	return store.Hash(conv) + ":" + store.Hash(command+"\x00"+model)
}
func (e *Engine) check(ctx context.Context, j store.Job, p store.Plan) (Configuration, string, error) {
	cfg, err := e.Configuration()
	if err != nil {
		return cfg, "", err
	}
	paused, err := e.Store.Paused()
	if err != nil {
		return cfg, "", err
	}
	if paused {
		return cfg, "paused", nil
	}
	if !e.Transport.Ready() {
		return cfg, "transport_unavailable", nil
	}
	at, err := time.Parse(time.RFC3339Nano, j.CreatedAt)
	if err != nil || e.now().Sub(at) > 2*time.Minute {
		return cfg, "stale", nil
	}
	if quiet(cfg.Settings, e.now()) {
		return cfg, "quiet_hours", nil
	}
	inbox, err := e.Collection.Settings()
	if err != nil {
		return cfg, "", err
	}
	if !p.Manual && !inbox.AutomaticReplies {
		return cfg, "automatic_replies_disabled", nil
	}
	if p.InboxOwnerOnly {
		if err := inbox.FileLimits().Check(p.Attachments); err != nil {
			return cfg, "attachment_limits_changed", nil
		}
		c, err := e.lookupConversation(ctx, j.ConversationID)
		if err != nil {
			return cfg, "", err
		}
		if !inbox.Allows(c) || c.ParticipantID != p.Participant {
			return cfg, "inbox_access_disabled", nil
		}
	} else if !e.Store.Enrolled(j.ConversationID, p.Participant) {
		return cfg, "unenrolled", nil
	}
	if p.Owner && !cfg.Settings.OwnerCommands {
		return cfg, "owner_commands_disabled", nil
	}
	if p.Thread != nil {
		cmd, _, _, _, enabled := resolve(cfg, strings.TrimPrefix(j.Command, "!"))
		if !enabled || cmd.Type != "ai" || !cmd.Memory {
			return cfg, "thread_unavailable", nil
		}
		if err := e.Store.CheckAIThread(j.ConversationID, p.Participant, j.Command, p, e.now(), cfg.Settings.MemoryHours, cfg.Settings.RetentionDays); errors.Is(err, store.ErrAIThreadUnavailable) {
			return cfg, "thread_unavailable", nil
		} else if err != nil {
			return cfg, "", err
		}
	}
	valid, err := e.validConversation(ctx, j.ConversationID, p.Participant)
	if err != nil {
		return cfg, "", err
	}
	if !valid {
		return cfg, "identity_changed", nil
	}
	if !p.Manual && !strings.Contains("|!ping|!help|!status|!new|!more|", "|"+j.Command+"|") {
		cmd, _, _, _, ok := resolve(cfg, strings.TrimPrefix(j.Command, "!"))
		if !ok || !allowed(cmd, j.ConversationID) {
			return cfg, "command_unavailable", nil
		}
		if cmd.Approval && !p.Approved {
			return cfg, "approval_required", nil
		}
	}
	return cfg, "", nil
}

// Dispatch sends at most one queued job. The control lock is held throughout,
// except while a provider generates text or files upload, so pause and consent
// changes stay responsive; every check is repeated after the lock is regained.
// The send itself is serialized with pause and enrollment, and its intent is
// persisted (submitting, with a transaction ID) before the transport call.
func (e *Engine) Dispatch(ctx context.Context) error {
	ctx = transport.WithPhonePurpose(ctx, "send")
	e.dispatchMu.Lock()
	defer e.dispatchMu.Unlock()
	e.mu.Lock()
	j, p, cfg, ready, err := e.nextReady(ctx)
	if !ready || err != nil {
		e.mu.Unlock()
		return err
	}
	if p.Mutation != nil {
		return e.dispatchReaction(ctx, j, p) // releases the lock
	}
	defer e.mu.Unlock()
	history, err := e.memoryContext(j, cfg, &p)
	if err != nil {
		return err
	}
	if done, err := e.generateReply(ctx, j, &p, &cfg, history); done {
		return err
	}
	if done, err := e.prepareFiles(ctx, j, &p); done {
		return err
	}
	return e.submit(ctx, j, p, cfg, history)
}

// nextReady returns the next queued job that passes every pre-send check.
// A job that fails a check is completed or sent for approval instead; a check
// that errors backs off before the same job is tried again. Caller holds the
// control lock.
func (e *Engine) nextReady(ctx context.Context) (j store.Job, p store.Plan, cfg Configuration, ready bool, err error) {
	paused, err := e.Store.Paused()
	if err != nil || paused || !e.Transport.Ready() {
		return j, p, cfg, false, err
	}
	j, p, err = e.Store.Next()
	if errors.Is(err, sql.ErrNoRows) {
		return j, p, cfg, false, nil
	}
	if err != nil {
		return j, p, cfg, false, err
	}
	if e.preflight.job != j.ID {
		e.preflight = preflightRetry{job: j.ID}
	}
	if e.now().Before(e.preflight.until) {
		return j, p, cfg, false, nil
	}
	cfg, code, err := e.check(ctx, j, p)
	if err != nil {
		e.preflight.failures++
		e.preflight.until = e.now().Add(min(30*time.Second, time.Second*time.Duration(1<<min(e.preflight.failures-1, 5))))
		return j, p, cfg, false, err
	}
	e.preflight = preflightRetry{}
	if code != "" {
		if code == "approval_required" {
			_, err = e.Store.Transition(j.ID, "queued", "awaiting_approval")
		} else {
			state := "cancelled"
			if code == "stale" {
				state = "expired"
			}
			err = e.Store.Complete(j.ID, state, code)
		}
		return j, p, cfg, false, err
	}
	return j, p, cfg, true, nil
}

// memoryContext returns the conversation memory a generation may use, or none
// when memory was cleared or disabled after the job was planned.
func (e *Engine) memoryContext(j store.Job, cfg Configuration, p *store.Plan) ([]store.Turn, error) {
	history := p.Context
	if p.Memory {
		epoch, epochErr := e.Store.MemoryEpoch(j.ConversationID)
		cmd, _, _, _, enabled := resolve(cfg, strings.TrimPrefix(j.Command, "!"))
		if epochErr != nil {
			return nil, epochErr
		}
		if epoch != p.MemoryEpoch || !enabled || !cmd.Memory {
			p.Memory = false
			history = nil
		}
	}
	return history, nil
}

// generateReply asks the provider for an AI reply, within the daily budget.
// The lock is released only for the provider call; the job state and every
// pre-send check are verified again afterwards. done reports that dispatch
// ended here (the job completed, moved to approval, or an error).
func (e *Engine) generateReply(ctx context.Context, j store.Job, p *store.Plan, cfg *Configuration, history []store.Turn) (done bool, err error) {
	if p.Model == "" || p.Reply != "" {
		return false, nil
	}

	m, err := e.model(p.Model)
	if err != nil {
		err = e.Store.Complete(j.ID, "failed", "model_unavailable")
		return true, err
	}
	var g Generation
	if json.Unmarshal(p.Generation, &g) != nil || validateModel(g, m) != nil {
		err = e.Store.Complete(j.ID, "failed", "generation_unsupported")
		return true, err
	}
	if p.Image != nil && !readsImages(m) {
		err = e.Store.Complete(j.ID, "failed", "model_images_unsupported")
		return true, err
	}
	reserve := m.OutputPrice*float64(g.Tokens) + m.InputPrice*float64(len(p.Input)+len(p.Instructions)+512)
	if p.Image != nil {
		reserve += m.InputPrice * imageTokens
	}
	for _, turn := range history {
		reserve += m.InputPrice * float64(len(turn.Content)+64)
	}
	day := e.now().UTC().Format("2006-01-02") + "T00:00:00Z"
	spent, unknown, err := e.Store.SpendSince(day)
	if err != nil {
		return true, err
	}
	if cfg.Settings.DailyBudget > 0 && (!m.PricingKnown || unknown > 0 || spent+reserve > cfg.Settings.DailyBudget) {
		err = e.Store.Complete(j.ID, "failed", "budget_exceeded_or_unknown")
		return true, err
	}
	provider, key, err := e.providerForModel(m)
	if err != nil || len(key) == 0 {
		err = e.Store.Complete(j.ID, "failed", "provider_key_missing")
		return true, err
	}
	ok, err := e.Store.Transition(j.ID, "queued", "generating")
	if err != nil || !ok {
		return true, err
	}
	if err = e.Store.Usage(j.ID, p.Model, 0, 0, 0, nil); err != nil {
		return true, err
	}
	if err = e.Store.SetPublicSetting("requested_reasoning:"+j.ID, g.Reasoning); err != nil {
		return true, err
	}
	genctx, cancel := context.WithTimeout(ctx, 95*time.Second)
	e.pending[j.ID] = cancel
	e.mu.Unlock()
	var result Result
	image, imageErr := e.loadImage(genctx, j.ConversationID, p.Image, cfg.Settings.AIPhotoMaxMB)
	var generr error
	if imageErr != nil {
		zero := 0.0
		result.Cost = &zero
	} else {
		result, generr = provider.Generate(genctx, key, *p, history, m, image)
	}
	cancel()
	e.mu.Lock()
	delete(e.pending, j.ID)
	reportedModel := result.Model
	if reportedModel == "" {
		reportedModel = p.Model
	}
	if err = e.Store.Usage(j.ID, reportedModel, result.PromptTokens, result.CompletionTokens, result.ReasoningTokens, result.Cost); err != nil {
		return true, err
	}
	current, _, loaderr := e.Store.JobPlan(j.ID)
	if loaderr != nil && current.State == "generating" {
		return true, loaderr
	}
	if loaderr != nil || current.State != "generating" {
		return true, nil
	}
	if imageErr != nil {
		err = e.Store.Complete(j.ID, "failed", "image_unavailable")
		return true, err
	}
	if generr != nil {
		err = e.Store.Complete(j.ID, "failed", safeProviderCode(generr))
		return true, err
	}
	p.Reply = result.Text
	checked, code, err := e.check(ctx, j, *p)
	*cfg = checked
	if err != nil {
		e.Store.Complete(j.ID, "failed", "final_check_failed")
		return true, err
	}
	if code != "" {
		if code == "approval_required" {
			p.Approval = true
			p.Approved = false
			if err = e.Store.SavePlan(j.ID, *p); err != nil {
				return true, err
			}
			_, err = e.Store.Transition(j.ID, "generating", "awaiting_approval")
		} else {
			err = e.Store.Complete(j.ID, "cancelled", code)
		}
		return true, err
	}
	if err = e.Store.SavePlan(j.ID, *p); err != nil {
		return true, err
	}
	if _, err = e.Store.Transition(j.ID, "generating", "queued"); err != nil {
		return true, err
	}

	return false, nil
}

// Uploading does not send a message. Release the control lock so pause and
// consent revocation remain responsive, then repeat every final-send check.
// done reports that dispatch ended here.
func (e *Engine) prepareFiles(ctx context.Context, j store.Job, p *store.Plan) (done bool, err error) {
	if len(p.Attachments) == 0 {
		return false, nil
	}

	preparer, ok := e.Transport.(transport.FilePreparer)
	if !ok {
		err = e.Store.Complete(j.ID, "failed", "attachments_unavailable")
		return true, err
	}
	transitioned, transitionErr := e.Store.Transition(j.ID, "queued", "generating")
	if transitionErr != nil || !transitioned {
		return true, transitionErr
	}
	uploadCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	e.pending[j.ID] = cancel
	e.mu.Unlock()
	files, uploadErr := preparer.PrepareFiles(uploadCtx, p.Attachments)
	cancel()
	e.mu.Lock()
	delete(e.pending, j.ID)
	current, _, loadErr := e.Store.JobPlan(j.ID)
	// Cancellation clears the plan, so a known terminal state may have a
	// plan-decode error. A failed SQL lookup has no state and must propagate.
	if current.State != "" && current.State != "generating" {
		return true, nil
	}
	if loadErr != nil {
		return true, loadErr
	}
	if uploadErr != nil {
		err = e.Store.Complete(j.ID, "failed", "attachment_upload_failed")
		return true, err
	}
	_, code, checkErr := e.check(ctx, j, *p)
	if checkErr != nil || code != "" {
		err = e.Store.Complete(j.ID, "cancelled", "attachment_final_check_failed")
		return true, err
	}
	p.Attachments = files
	if err = e.Store.SavePlan(j.ID, *p); err != nil {
		return true, err
	}
	if _, err = e.Store.Transition(j.ID, "generating", "queued"); err != nil {
		return true, err
	}

	return false, nil
}

// submit persists the send intent (submitting, with a new transaction ID),
// then calls the transport once. A transport error other than a definite
// non-submission is recorded as unknown_submission and never retried.
func (e *Engine) submit(ctx context.Context, j store.Job, p store.Plan, cfg Configuration, history []store.Turn) error {
	current, _, err := e.Store.JobPlan(j.ID)
	if err != nil || current.State != "queued" {
		return err
	}
	if p.Reply == "" && len(p.Attachments) == 0 {
		err = e.Store.Complete(j.ID, "failed", "empty_reply")
		return err
	}
	max := p.MaxChars
	if max == 0 {
		max = 12000
	}
	runes := []rune(p.Reply)
	if len(runes) > max {
		p.Reply = string(runes[:max-25]) + "\n[Reply shortened.]"
	}
	if p.ReplyTarget != "" {
		c, lookupErr := e.lookupConversation(ctx, j.ConversationID)
		if lookupErr != nil {
			return lookupErr
		}
		v, targetErr := e.replyTarget(ctx, c, p.ReplyTarget)
		if targetErr != nil || v.UpstreamID != p.ReplyWireTarget {
			err = e.Store.Complete(j.ID, "failed", "reply_unavailable")
			return err
		}
	}
	txn := store.TransactionID()
	if err = e.Store.SubmittingPlan(j.ID, txn, p, e.now()); err != nil {
		return err
	}
	// The send is serialized with pause/enrollment; once invoked its outcome may be uncertain.
	sendctx, cancelSend := context.WithTimeout(ctx, 30*time.Second)
	var receipt transport.Receipt
	var senderr error
	if p.Thread != nil && p.Thread.ReplyWire != "" {
		if sender, ok := e.Transport.(transport.ReplySender); ok {
			receipt, senderr = sender.SendReply(sendctx, j.ConversationID, txn, p.Reply, p.Thread.ReplyWire, nil)
		} else {
			senderr = transport.ErrNotSubmitted
		}
	} else if p.ReplyTarget != "" {
		if sender, ok := e.Transport.(transport.ReplySender); ok {
			receipt, senderr = sender.SendReply(sendctx, j.ConversationID, txn, p.Reply, p.ReplyWireTarget, p.Attachments)
		} else {
			senderr = transport.ErrNotSubmitted
		}
	} else if len(p.Attachments) > 0 {
		if sender, ok := e.Transport.(transport.FileSender); ok {
			receipt, senderr = sender.SendFiles(sendctx, j.ConversationID, txn, p.Reply, p.Attachments)
		} else {
			senderr = transport.ErrNotSubmitted
		}
	} else {
		receipt, senderr = e.Transport.Send(sendctx, j.ConversationID, txn, p.Reply)
	}
	cancelSend()
	if senderr != nil {
		state, code := "unknown_submission", "transport_outcome_unknown"
		if errors.Is(senderr, transport.ErrNotSubmitted) {
			state, code = "failed", "not_submitted"
		}
		err = e.Store.Complete(j.ID, state, code)
		return err
	}
	if receipt.State != "submitted" {
		err = e.Store.Complete(j.ID, "unknown_submission", "unrecognized_receipt")
		return err
	}
	if len(p.Attachments) > 0 {
		err = e.Store.SaveSentFiles(txn, j.ConversationID, p.Reply, p.Attachments)
	} else {
		err = e.Store.SaveMessage(txn, j.ConversationID, "outgoing", p.Reply)
	}
	if err != nil {
		return err
	}
	if p.Thread != nil && p.Thread.ReplyWire != "" {
		if err = e.Store.SaveReplyRelationship(txn, j.ConversationID, p.Thread.ReplyWire); err != nil {
			return err
		}
	}
	if p.ReplyTarget != "" {
		if err = e.Store.SaveReplyRelationship(txn, j.ConversationID, p.ReplyWireTarget); err != nil {
			return err
		}
	}
	origin := "bot"
	if p.Manual {
		origin = "web"
	}
	if err = e.Store.RecordSubmitted(txn, origin, p.InboxOwnerOnly); err != nil {
		return err
	}
	if p.Model != "" && p.Memory && p.Thread == nil {
		cmd, _, _, _, ok := resolve(cfg, strings.TrimPrefix(j.Command, "!"))
		epoch, epochErr := e.Store.MemoryEpoch(j.ConversationID)
		if epochErr != nil {
			return epochErr
		}
		if ok && cmd.Memory && epoch == p.MemoryEpoch {
			history = append(history, store.Turn{Role: "user", Content: p.Input}, store.Turn{Role: "assistant", Content: p.Reply})
			if err = e.Store.Remember(memoryScope(j.ConversationID, j.Command, p.Model), history, e.now().Add(time.Duration(p.MemoryHours)*time.Hour)); err != nil {
				return err
			}
		}
	}
	if p.Thread != nil {
		if err = e.Store.TouchAIThread(j.ID, e.now(), cfg.Settings.MemoryHours, cfg.Settings.RetentionDays); err != nil {
			return err
		}
	}
	err = e.Store.Complete(j.ID, "submitted", "")
	return err
}

func safeProviderCode(err error) string {
	code := err.Error()
	if strings.HasPrefix(code, "provider_") && !strings.ContainsAny(code, " \r\n") {
		return code
	}
	return "provider_failed"
}
func (e *Engine) Compose(ctx context.Context, conv, text string) (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	text = strings.TrimSpace(text)
	if text == "" || utf8.RuneCountInString(text) > 12000 {
		return "", errors.New("invalid_message")
	}
	cfg, err := e.Configuration()
	if err != nil {
		return "", err
	}
	paused, err := e.Store.Paused()
	if err != nil {
		return "", err
	}
	if paused || !e.Transport.Ready() || quiet(cfg.Settings, e.now()) {
		return "", errors.New("bot_unavailable")
	}
	list, err := e.Transport.Conversations(ctx)
	if err != nil {
		return "", err
	}
	for _, c := range list {
		if c.ID == conv && !c.Group && c.Writable && e.Store.Enrolled(conv, c.ParticipantID) {
			id := store.ID()
			_, err = e.Store.Enqueue(id, conv, "manual", store.Plan{Participant: c.ParticipantID, Reply: text, MaxChars: cfg.Settings.MaxChars, Manual: true})
			return id, err
		}
	}
	return "", errors.New("enrollment_required")
}
func (e *Engine) Playground(name, input string) (map[string]any, error) {
	cfg, err := e.Configuration()
	if err != nil {
		return nil, err
	}
	name = strings.TrimPrefix(name, "!")
	switch name {
	case "ping":
		return map[string]any{"text": "pong", "model": "Local", "simulated": true}, nil
	case "new":
		return map[string]any{"text": "Simulated memory reset. Saved memory is unchanged.", "model": "Local", "simulated": true}, nil
	case "status":
		return map[string]any{"text": "Synthetic status preview. No bot state was changed.", "model": "Local", "simulated": true}, nil
	case "help":
		return map[string]any{"text": "Commands: !ping, !help, !status, !new, !more", "model": "Local", "simulated": true}, nil
	case "more":
		return map[string]any{"text": "Paired replies are sent in full, subject to the character limit.", "model": "Local", "simulated": true}, nil
	}
	// Preview a copy only. Imported/disabled commands stay disabled on disk.
	for i := range cfg.Commands {
		if cfg.Commands[i].Name == name {
			cfg.Commands[i].Enabled = true
		}
	}
	cmd, model, instructions, _, ok := resolve(cfg, name)
	if !ok {
		return nil, errors.New("command_unavailable")
	}
	text := "Simulated AI response. No provider request was made."
	if cmd.Type == "fixed" {
		text = strings.ReplaceAll(instructions, "{input}", input)
	}
	return map[string]any{"text": text, "model": model, "simulated": true}, nil
}
func (e *Engine) Run(ctx context.Context, report func(error)) {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	retention := time.NewTicker(time.Hour)
	defer retention.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-retention.C:
			cfg, err := e.Configuration()
			if err == nil {
				_, err = e.Store.Retention(cfg.Settings.RetentionDays)
				if err == nil {
					err = e.Store.PruneAIThreads(e.now(), cfg.Settings.MemoryHours, cfg.Settings.RetentionDays)
				}
			}
			if err != nil {
				report(err)
			}
		case <-ticker.C:
			if err := e.Dispatch(ctx); err != nil {
				report(err)
			}
		}
	}
}
func (e *Engine) Reconcile(conv, txn, state, reported string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	rank := map[string]int{"submitting": 0, "unknown_submission": 0, "submitted": 1, "delivered": 2, "read": 3}
	desired, ok := rank[state]
	if !ok || desired == 0 {
		return nil
	}
	id, current, found, err := e.Store.JobByTransaction(conv, txn)
	if err != nil || !found {
		return err
	}
	previous, ok := rank[current]
	if !ok || desired < previous {
		return nil
	}
	if err = e.Store.Complete(id, state, reported); err != nil {
		return err
	}
	return e.Store.UpdateMessageStatus(conv, txn, state)
}

type preflightRetry struct {
	job      string
	until    time.Time
	failures int
}
