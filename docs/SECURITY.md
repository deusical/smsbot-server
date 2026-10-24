# Security and privacy

SmsBot is experimental software that holds powerful credentials. Read this before you
pair a real account.

## What you are trusting it with

- **Google session cookies.** Pairing imports cookies from a Messages for web session.
  They are broad account credentials, not narrowly scoped bot permissions. Anyone who
  controls the host (or a backup of its state plus the master key) can act as that
  session until it is revoked in your Google account.
- **Your messages.** With collection on, the service keeps the text and files of the
  conversations you enable, until retention removes them.
- **The owner credential.** One long random value, shown only in the file created at
  initialization. Whoever holds it can use the dashboard.

## How it is protected

- **Encryption at rest.** Message bodies, queued plans, attachment payloads, pairing
  sessions and settings are sealed with AES-GCM bound to a scope (job, message,
  session). The master key lives outside the database directory. Windows wraps it with
  the current user's DPAPI; Linux requires a key file with mode 0600 or stricter. A state
  directory copied without its key cannot be read, and DPAPI-protected keys do not move
  between Windows accounts or to Linux.
- **No secrets in logs.** Cookies, message bodies, contacts and raw upstream errors are
  never logged; libgm logging is discarded. Each request to the phone is logged as one
  line with only its time, kind and purpose (for example `conversation`, `discovery`),
  never an identifier or content. Pairing reads bounded input from stdin and
  parses a copied cURL request without ever executing it.
- **Owner only.** There is a single owner and no other credential type. Sign-in uses
  HttpOnly, SameSite=Strict cookies (12 hours, or until sign-out with **Keep me signed
  in**, renewed while in use). State-changing requests need the exact `Origin`; the exact
  `Host` is enforced to stop DNS rebinding; API responses are `no-store`; the content
  security policy blocks remote scripts and framing. Bearer credentials are not
  accepted. **Sign out everywhere** (Settings > Devices) revokes every session.
- **Hosting.** Loopback by default. A non-loopback bind requires direct TLS and an exact
  HTTPS origin; a private HTTPS proxy in front of loopback is supported. Forwarded
  headers never decide trust or origin. See [Hosting](HOSTING.md).
- **Participant rules.** Enrollment and collection use protocol participant IDs, not
  names. Groups, stale or historical data, outgoing echoes and incomplete attachments
  never run commands, and file-bearing messages never run commands even through a
  caption.
- **Files.** Previews and downloads recheck consent and exact conversation identity.
  Only allowlisted image, audio and video types render inline; other files download
  with a sandbox policy.
- **AI providers.** Used only when a command selects a model and is invoked. Prompts and
  any context you opted in go to OpenRouter or the compatible endpoint you configured;
  cleartext HTTP is allowed only for loopback endpoints. Keys are write-only and never
  returned. Configure only endpoints you trust.
- **Sending safety.** Intent is persisted before submission; an uncertain submission is
  never retried automatically; pause cannot retract what was already submitted.

## What it does not protect against

- Someone who controls the host account or can read both the state and the key.
- Google changing or blocking the unofficial client, or revoking the session.
- Mistakes you configure: an over-broad command, a public origin, a shared owner file.
- Anything outside the offline test suite: live behavior is only partly verified, see
  [Known limitations](LIMITATIONS.md). There has been no independent security audit.

## Operating advice

Run it on a private host, behind Tailscale or similar, as a dedicated non-root user.
Keep the state and key directories private and on local storage, back them up with
[the offline tools](BACKUP.md), and never paste cookies, owner credentials or message
content into issues, chat tools or screenshots. Check dependencies before you build
(`scripts\security-check.ps1` runs `govulncheck` and `npm audit`). The service must not import
`golang.org/x/crypto/openpgp`, which has an open advisory with no fix.

## Reporting a vulnerability

Please report security problems privately through GitHub's "Report a vulnerability"
(Security tab) rather than a public issue, and do not include real credentials or
message content. Expect a best-effort response; this is a personal project.
