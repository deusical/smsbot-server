# User guide

The dashboard follows your system light or dark theme (toggle in the top bar) and uses a
bottom tab bar on phones. Served over HTTPS it installs as a phone app. Theme, drafts,
credentials and keys are never written to browser storage; your sign-in lives in an
HttpOnly cookie. The sign-in page offers **Keep me signed in** (until you sign out).

## Top bar

The **Live / Paused** switch ("Bot sending") controls all sending. Pausing cancels queued
and waiting replies; it cannot retract a message already submitted. Every service start
is paused. A "Demo data" tag marks the offline demo.

## Home

Plain-language status, a 24-hour request chart, counts (needs approval, uncertain sends,
people with bot replies on, enabled commands), recent requests with inline **Approve** and
**Cancel**, and links to people and commands.

## Conversations

![Conversations](images/conversations.png)

A Messages-style view of your individual conversations. Groups are never used. The list
updates as your phone reports new messages; opening or returning to the dashboard also
checks your phone once, so it does not keep waking your phone while the tab is open.

- **Order and markers.** Newest message first, pinned conversations on top, a time on each
  row, an unread dot, and a last-message preview (with "You:" for messages you sent).
  "typing…" appears while the other person is typing.
- **Bot replies** switch per row enrolls that exact participant for commands.
- **Open a thread** to read, search ("search as you type"), and load older messages by
  scrolling up. A round button jumps back to the latest message.
- **Send.** Enter sends, Shift+Enter adds a line (on touch keyboards Enter adds a line and
  the send button sends). The box clears at once and a bubble shows **Sending**, then
  **Sent**, **Delivered** and **Read** as Google reports them. Paste an image or drop files
  onto the box to attach them, within your attachment limits. If a send is not confirmed
  you get **Retry sending** (it can never send twice). If it definitely failed you can
  **Edit and resend**. If the outcome is uncertain you are asked to check your phone; it is
  never retried for you.
- **Read receipts.** While this tab is open and focused and you are looking at the newest
  message of an unread conversation, it is marked read on your phone, so the other person
  may see a receipt if your phone sends them. Turn this off in **Settings > Messages &
  privacy**. Sending is not required and pausing does not stop it.

- **Message actions.** Hover a message and use the three-dot button beside it for Copy
  text and Details. The menu opens next to the message without covering it, and the
  message stays outlined while the menu is open. On phones, tap the message to open the
  menu as a bottom sheet; the message scrolls clear of the sheet. Menus also work from
  the keyboard. Escape closes the menu and returns focus to the message.
- **RCS replies.** Eligible collected RCS messages offer Reply. A preview above the
  composer shows the target; canceling it preserves your text. Replies can include
  attachments. Quoted bubbles link to the retained original; missing originals are
  labeled unavailable. Clear an active search before jumping to an original.
- **RCS reactions.** Choose an emoji, use the expanded picker, or paste a Unicode
  emoji. Choosing your existing emoji removes it; a different one changes it.
  Reactions sit on the lower edge of the message as one compact pill (a number appears
  only when several people chose the same emoji); yours is outlined. Hover a reaction
  to see who chose it. A submitted change waits
  for the phone to sync before another change can be made. An uncertain outcome is
  never retried automatically. **Check request** only repeats the same idempotent
  API request when its acknowledgement was lost; it cannot create another action.

Action availability belongs to each message: an older SMS in an RCS conversation
does not gain RCS actions. SMS/MMS and unknown messages currently offer Copy text
and Details; native RCS actions never silently become SMS sends. Collection, pause,
phone availability, and exact conversation identity still control sending.
Observed phone edits continue to show **Edited**. Editing a sent message from this
dashboard remains unavailable because the companion transport exposes no edit operation.

Keyboard shortcuts: **Ctrl/Cmd+K** focuses conversation search (Esc clears), **Alt+Up** and
**Alt+Down** switch conversation, **Ctrl/Cmd+S** saves pending settings changes.

## Commands

A studio with a searchable list (each with an enable switch, plus the built-ins `!ping`,
`!help`, `!status`, `!new`, `!more`) and an editor: name, description, fixed text or AI,
approval, memory, model or profile, optional generation overrides, allowed conversations
and an offline **Try it** preview. **New** offers Blank, Quick reply, AI assistant and
Translator. Templates and duplicates start disabled; AI templates require approval and
leave memory off. Model profiles have their own tab.

Fixed replies can use `{input}`. AI commands send the input and any context you opted into
to the provider selected by the model. Approvals and queued plans expire after two
minutes. Requests are counted in rolling-hour and daily limits (manual messages count);
quiet hours withhold requests instead of deferring them. With **Commands from your phone**
on (Settings > Limits & quiet hours), commands you send yourself in a conversation with
bot replies on run too, whether typed on your phone or in Conversations, and the reply
goes to that conversation. The daily spend control is a
conservative reported-spend threshold, not a billing guarantee; unknown pricing blocks
further AI use once it is on.

### Continuing an AI conversation

For the AI `ask` command, turn on **Remember the conversation** to keep encrypted
AI threads across restarts. Each unquoted `!ask` starts fresh. Use the native Reply
action on an AI answer to continue it; the follow-up does not need `!ask`.
Replying to an older answer branches from that point, excluding later discussion.
Ordinary chat, copied quotations and replies to manual/fixed replies do not invoke AI.
The legacy `!deepseek` alias follows the same rules as `!ask`.

Follow-ups require reliable native reply metadata (currently RCS). Without it,
send a new `!ask`. Text-only, freshness, enrollment, pause, quiet-hours, approval
and spending rules still apply. Owner follow-ups require Commands from your phone.
Turning memory off keeps new asks stateless. Other AI commands keep their existing
rolling-memory behavior.

Threads expire after the configured memory window without a successful answer
(default 24 hours), with an absolute cap from creation equal to bot retention
(default seven days). The model receives the newest complete ancestor exchanges
that fit a bounded context budget; it does not have unlimited recall. Changing
models preserves an active thread's transcript. `!new` clears AI context for the
chat and invalidates unsent thread requests, without deleting archived messages.
Deleted answers no longer trigger plain follow-ups. Thread context does not
require full archive collection and is excluded from filtered backups/exports.

## Activity

Filters (all, needs approval, in progress, sent, problems) and search. Rows expand to show
model, usage and cost. Approve and cancel act inline. Uncertain submissions are explained
and have no retry. Totals describe the loaded recent requests only.

## Archive

![Archive](images/archive.png)

The archive keeps an encrypted local copy of conversations you enable under **Settings >
Messages & privacy**, independent of bot replies and of Live/Pause, and it continues with
the tab closed.

- **Overview.** Storage used, saved messages, original files and conversations tracked.
- **Conversation history.** Pick a conversation and choose **Archive conversation**: it
  reads all available history, fills gaps, reconciles edits and downloads original files
  in the background. Completed imports are not re-scanned automatically; archive again
  to re-scan. Advanced options limit a new import to a start date. Recent checks,
  viewed-section checks and file downloads run separately.
- **Automatic history repair** lists updates that could not be saved and retries them;
  it never claims completeness.
- **Retention** (days, 0 keeps everything), **original file archive** (limits and
  quota) and **message history export** (encrypted backup or readable export).

Available history may have gaps, and absence from Google's pages never deletes saved
messages. Importing and viewing history never run old commands.

## Settings

| Section | What it holds |
|---|---|
| AI models | Default model (star favorites in the picker), per-model tuning, catalog refresh |
| Providers | OpenRouter key and an OpenAI-compatible URL and key (write-only) |
| Limits & quiet hours | Hourly and daily limits, spend threshold, quiet hours, reply length, commands from your phone |
| Messages & privacy | Collection, scope, older history, automatic replies, read receipts, attachment limits, retention, memory window |
| Devices | Phone connection details, **Sign out everywhere** |
| Import & export | Recipes: share commands, profiles and settings as a file (never keys) |

Commands, profiles, settings and inbox preferences share one draft that survives page
changes. A save bar appears when something differs from the saved state; **Save changes**
(or Ctrl/Cmd+S) commits it and **Discard** restores the saved state. Provider keys save
immediately from their own forms. Importing a recipe replaces your configuration with
every command disabled; review before enabling.

## Files and attachments

With collection on, **Attach files** (or paste/drop) sends images, video, audio or
documents with an optional caption. Limits live under **Messages & privacy > Attachment
limits**; 0 means unlimited for that limit, but Google, carriers, memory and storage
still apply. Files are owner-only. Images, audio and video preview inline; anything
else downloads. A message that has an attachment never runs a command, even if its
caption looks like one.
