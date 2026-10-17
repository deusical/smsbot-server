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

A Messages-style view of your individual conversations. Groups are never used.

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
quiet hours withhold requests instead of deferring them. The daily spend control is a
conservative reported-spend threshold, not a billing guarantee; unknown pricing blocks
further AI use once it is on.

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
  in the background. Archive again to re-scan. Advanced options limit a new import to a
  start date. Recent checks, viewed-section checks and file downloads run separately.
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
| Limits & quiet hours | Hourly and daily limits, spend threshold, quiet hours, reply length |
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
