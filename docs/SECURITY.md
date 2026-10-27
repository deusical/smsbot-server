# Security and limitations

SmsBot is an experimental personal project that holds powerful credentials. Read this
before you pair a real account.

## Risks of the approach

- **Unofficial client.** SmsBot talks to Google Messages through
  [libgm](https://github.com/mautrix/gmessages/tree/main/pkg/libgm), a reverse-engineered
  Messages for web client. Google can change or block it at any time, and using it may
  conflict with Google's terms. Use only your own account.
- **Broad credentials.** Pairing imports Google session cookies. Anyone who controls the
  host, or has a copy of its state together with the key, can act as that session until
  you revoke it in your Google account.
- **No audit.** The code has been reviewed by its author and by AI reviewers, not by a
  security professional.

## What it stores and how

- **Encrypted at rest.** Messages, files, queued replies, the Google session, settings
  and notification subscriptions are encrypted with AES-GCM. The master key lives
  outside the state directory: Windows protects it for the current user (DPAPI), and
  Linux requires a key file readable only by its owner.
- **Only what you enable.** Messages are archived only for conversations you turn on,
  and kept until retention removes them.
- **No secrets in logs.** Cookies, message text, contacts and raw errors from Google are
  never logged.

## Who can use it

- **One owner.** A single long random credential, written to a file when you initialize.
  Sign-in uses an HttpOnly, SameSite=Strict cookie; **Sign out everywhere** (Settings >
  Devices) ends every session.
- **Private by default.** The service listens on loopback. Any other address requires
  direct TLS. It checks the exact `Host` and `Origin` of every request and ignores
  forwarded headers. Keep it on a private network, such as Tailscale; don't expose it
  to the internet.

## What can trigger a reply

- Only individual conversations you enrolled, matched by participant ID, never by name.
  Groups are saved and shown only when you allow them. You can send to a group from the
  dashboard, only to the exact people you were shown. The bot acts in a group only for
  members you enrolled, and only while the group has exactly the people you approved.
- Only fresh text messages. History, old messages, messages with attachments and the
  bot's own replies never run commands.
- Every start is paused. Pausing cancels queued replies but can't recall one already
  sent.
- A send whose outcome is uncertain is never retried automatically; check your phone.

## Data that leaves the server

- **AI providers.** Only when an AI command runs: its input, plus any context you turned
  on, goes to the provider you configured. A file (photo, PDF, document, audio or video)
  goes too, but only when someone replies to it with an AI command, or sends one with a
  command or as a reply to an AI answer. Its contents are passed to the model as material
  to read, labelled as not being instructions, and are never logged. Provider keys can be set but never read back.
- **Notifications.** Off until you turn them on for a device. They go through that
  browser's push service (Google's, for Chrome) encrypted so that only the browser can
  read them. Turn off **Show message text** to send only the sender's name.
- **Read receipts.** By default, reading an unread conversation in the dashboard marks it
  read on your phone, so the other person may see a receipt. You can turn this off.

## Known limitations

- **Tested on one setup.** The author uses it daily with one Android phone: messaging,
  delivery statuses, RCS replies and AI commands work there. Automated tests are
  synthetic and never contact Google. Competing paired clients, very large files and
  macOS hosts are untested.
- **RCS-only actions.** Replies and reactions work on RCS messages only. Editing a sent
  message isn't possible; Google allows that only in the phone app.
- **History is best effort.** The archive keeps what Google makes available. A message
  missing from Google's pages is never treated as deleted.
- **Cookies can expire.** Google sometimes rejects the saved session. Update the cookies
  as described in [Getting started](GETTING_STARTED.md#when-google-rejects-the-cookies).
- **One owner.** No multi-user support. In a group, the bot answers only members you
  enrolled; it never answers your own messages there.
- **Attachments are held in memory** while they're processed, so very large files are
  limited by the host's memory.

## Reporting a vulnerability

Report security problems privately through GitHub's **Report a vulnerability** (Security
tab), not in a public issue, and don't include real credentials or messages. Responses
are best effort.
