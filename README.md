# SmsBot Server

A self-hosted personal command bot for a paired Google Messages phone, with a React/TypeScript dashboard that also installs as an app on your phone. **Development milestone, not a production release.**

## Implemented

- Authenticated owner dashboard and revocable Android client credentials.
- Individual conversation enrollment using protocol participant IDs; groups excluded.
- Built-in checks/reset commands and custom fixed/AI commands through OpenRouter or an owner-configured OpenAI-compatible endpoint; encrypted immutable plans, duplicate protection, pause, approvals, cancellation and stale-command rejection.
- Cached model catalog, profiles, per-model reasoning/temperature controls, scoped opt-in memory, quiet hours, request limits and reported-spend thresholds.
- Offline playground, disabled recipe imports (including Android command packs), retained text history/search/pagination, explicit phone-text import and manual compose.
- Owner-only conversation files: send attachments with configurable limits, including Unlimited, preview images/audio/video, download files, and display attachments in available imported phone history.
- Retention, encrypted offline backup/restore, and optional direct TLS or exact-origin private HTTPS proxy support.
- Encrypted message bodies, queued plans and pairing sessions. SQLite WAL on local disk; master key outside the database directory. Windows key protection uses CurrentUser DPAPI.
- Persisted outbound transaction IDs; interrupted submissions become uncertain and are not automatically retried. Correlated Google status events can advance submitted/delivered/read state.
- Explicit offline fixture transport, with synthetic conversations and no Google or provider connections.
- Responsive web dashboard that follows the system light/dark theme: live/paused status with a 24-hour request chart, inline approvals, a messenger-style conversation view with Enter-to-send compose, a command studio (list + editor + offline preview), searchable model picker with favorites, sectioned settings, and drafts that survive navigation behind one save bar.
- Installable phone app: the same dashboard works on phones (bottom navigation, full-screen conversations, safe-area aware) and installs to the home screen over HTTPS, for example through [Tailscale Serve](docs/HOSTING.md#tailscale-on-an-always-on-host). Optional stay-signed-in sessions (revocable everywhere); API data is never cached offline.
- Opt-in paired transport and local cookie/emoji pairing helper, pinned to the tested libgm revision.

The web dashboard is the supported phone client. The separate native Android app's server mode still works with a revocable client credential, but it is no longer developed and does not track new dashboard features.

## Not implemented or verified yet

The paired transport sends a complete reply up to the configured character cap; Android-only SMS segment/SIM controls do not apply. Legacy history import remains explicit. The owner archive synchronizes available history automatically, follows its separate retention policy and never routes imported commands. Full-inbox history includes available attachments; legacy client history remains text-only. Exhausting available pages does not establish complete Google history. Both import paths match messages to recorded sends using explicit upstream/transaction IDs. Re-import repairs older duplicate rows when that exact link is available; equal text and nearby timestamps never trigger deduplication.

Provider generation and delivery acceptance on the real phone, personal HTTPS/private-network setup, locked/open/background-phone cases, sustained authentication and browser handoff remain owner-controlled acceptance checks. Automated provider checks use local mocks or the HTTP-free fixture provider. Public hosting and portable cross-platform key recovery are outside this development milestone.

The owner reports a successful live `!ping` after cookie recovery. Full live acceptance, reconnect behavior, long-term uptime, locked-phone behavior, competing paired clients and Linux live Google operation remain pending. Offline Linux container startup/recovery has been rehearsed. Temporary network/phone outages block sending and automatically retry a verified phone check with bounded backoff. Recovery preserves the owner's Live/Pause choice: previously Live resumes for fresh commands, while explicit Pause remains paused. Unsent pre-gap work is cancelled and delayed commands from the gap never execute. Authentication, account changes, competing sessions, fatal connection errors and session-storage failures still pause automation and require owner intervention. Every process restart still starts paused. An overloaded receive-event queue also pauses automation and discards command input across the gap. Queue availability recovers after draining, while replies remain paused for owner review; diagnostics distinguish overload from connection failure. Do not promise unattended reliability yet. See the [completion plan](docs/COMPLETION_PLAN.md) for feature, client and deployment work alongside authentication observation.

## Develop on Windows

Go 1.27.2 toolchain and Node.js 24 are pinned for this milestone. Dependencies are locked in go.mod/go.sum and web/package-lock.json.

```powershell
.\build.ps1
.\start.ps1                  # Fixture mode, starts paused
```

Release builds omit linker/debug tables and embed only the current dashboard output.
For a quick local rebuild with unchanged dependencies, use
`./build.ps1 -SkipInstall -SkipChecks`; this still rebuilds the dashboard and binary.
The default build installs locked dependencies, runs offline tests and vet, and
reuses Go's content-keyed test cache. Use `-DebugSymbols` when you need debugger
symbols, and `-SkipWeb` only when the embedded dashboard is already current.
CI continues to run fresh race tests. Docker builds use BuildKit dependency and
compiler cache mounts; run `docker build -t smsbot .` with BuildKit enabled.

Open `http://127.0.0.1:8787`. Read the owner credential file locally as directed by the launcher and paste it into the dashboard. Do not send that credential to a coding agent. Secrets/state live in the local application data directory, outside source. Fixture and Google environments use different directories.

In **Conversations**, turn on **Bot replies** for the demo conversation, flip the top-bar switch to **Live**, open the conversation and use **Simulate !ping**. Activity will show submitted; fixture mode makes no claim of delivery. The service runs while the browser is closed. Ctrl+C stops it cleanly; every startup pauses automation and cancels eligible old queue entries.

## Pairing for controlled live acceptance

Google session cookies provide broad account access. Use your own account and follow [upstream pairing guidance](https://docs.mau.fi/bridges/go/gmessages/authentication.html). Keep credentials local. Do not reuse/import the prototype vault automatically.

1. Pause the existing Android/prototype bot and any server instance handling the designated conversation.
2. In a private browser session, pair Messages for web and copy the `/web/config` request as cURL (bash), as in the proven prototype workflow.
3. Run locally:

```powershell
.\start.ps1 -Transport Google -Pair
```

4. Approve the displayed emoji on your phone. Clear sensitive clipboard data yourself.
5. Run `.\start.ps1 -Transport Google`. It starts paused. Review exact enrollment in the authenticated dashboard; enable only for a designated test thread.

Google Messages chooses SMS/RCS and fallback; the server does not force RCS. The phone must remain online. Only complete fresh incoming text is routed; historical data, outgoing echoes and untrusted participants cannot run commands. The initial conversation picker shows the first 25 inbox threads.

### Update cookies without pairing again

For an existing pairing with rejected credentials, stop the Google service with Ctrl+C. In the same private browser session and Google account used for pairing, copy a fresh `/web/config` request as cURL (bash), then run locally:

```powershell
.\start.ps1 -Transport Google -UpdateCookies
```

This offline command reads the clipboard through stdin, takes the service state lock, saves an exact encrypted backup beside `google-session.enc` with a unique `.before-cookie-update-*.enc` suffix, and updates imported cookies while preserving pairing keys, identities, tokens and other session fields. The optional imported cookie is removed if absent from the new input; other server-acquired cookies are preserved. It refuses missing/incomplete pairing state or a running service and makes no Google requests. Use the same account; the offline command cannot verify account ownership or credential validity. Backups remain encrypted sensitive state outside source.

Clear your clipboard locally, close the private window without signing out, and run `.\start.ps1 -Transport Google`. Leave automation paused. Successful cookie replacement does not prove authentication works or explain a previous 401; paused startup checks phone readiness. Do not use `-Pair` to overwrite an existing pairing. If startup still fails, preserve the session and its backup for further diagnosis.

## Phone access

Run the service on an always-on host and open it from your phone through a private HTTPS proxy; see [Tailscale on an always-on host](docs/HOSTING.md#tailscale-on-an-always-on-host). Install it from the browser menu (Chrome: **Install app**; Safari: Share > **Add to Home Screen**).

## Legacy Android client connection

On a **disposable emulator**, install the Android debug APK and use:

```powershell
adb -s YOUR_EMULATOR_SERIAL reverse tcp:8787 tcp:8787
```

Create a client credential in web **Settings > Devices**, then enter `http://127.0.0.1:8787` and that credential in Android Settings > Self-hosted server. Debug builds permit loopback HTTP only. Release clients require HTTPS. No permanent administrator key is embedded in Android.

## Validation

```powershell
.\build.ps1
.\security-check.ps1
cd web
npx playwright install chromium
npm test
```

Playwright starts its own isolated fixture service and ephemeral synthetic credentials. Tests never contact Google, carriers or AI providers. See [validation](docs/VALIDATION.md), [architecture](docs/ARCHITECTURE.md), [security](docs/SECURITY.md), and [roadmap](docs/ROADMAP.md).

## License

AGPL-3.0-or-later. The paired adapter uses [mautrix/gmessages libgm](https://github.com/mautrix/gmessages/tree/main/pkg/libgm); its license and notices must be preserved. The separate Android project retains its existing MIT license. Do not copy upstream server code into Android under an assumed MIT license.

## Configure the server bot

### Full conversation inbox

In web **Settings > Messages & privacy**, enable **Show all conversation messages** for all eligible individual conversations or explicitly selected conversations. This owner-only option retains ordinary incoming text and outgoing phone messages as well as command traffic. Capture works while sending is paused. An owner warning identifies possible capture gaps after a lookup failure or overload; importing available history may recover missed text. Inbox access does not enroll a sender for bot commands, enable AI memory, or grant existing Android credentials access to the additional text. Selection binds stable conversation/participant IDs; groups remain excluded. Files and captions are retained for display and never execute commands or enter AI memory.

With **Show all conversation messages** enabled, use **Attach files** beside the message box to send images, videos, audio or documents, with an optional caption. In **Messages & privacy > Attachment limits**, configure files per message, per-file MB and combined MB. Set any field to **0 (Unlimited)** to disable that application limit independently, or use its **Unlimited** button. Defaults are four nonempty files, 10 MiB each and 20 MiB combined; these are configurable application defaults, not Google limits. Selected files can be removed before sending. Images preview inline, browser-supported audio/video have playback controls, and every file has a download button. Active document types such as HTML and SVG are downloads only. Remote attachments are fetched on demand through the authenticated server; original availability and MMS/RCS file restrictions depend on Google, the phone and the carrier. Files exceeding your configured per-file viewing/download limit remain visible as file entries with an explanation; set it to Unlimited to remove that cap. When an image original is unavailable but Google supplies a thumbnail, the preview can use it; downloading still requires the original. A missing or expired original shows an unavailable state instead of hiding its message.

Unlimited removes application count/size/body ceilings for that policy; it does not bypass Google/carrier restrictions, operation timeouts, SQLite limits, or the host's available memory/storage. Uploads/downloads currently buffer content in memory.

Attachment metadata, remote decryption keys and locally uploaded bytes are encrypted at rest and follow message retention. Backups include retained file payloads/references; an on-demand remote file is not archived by a backup. Files are owner-only and are not exposed to legacy Android credentials. Attachment sending uses the same durable intent, final access checks and uncertain-send protections as text. An interrupted upload cannot authorize a later send after pause or consent revocation.

Enable **Load older phone history when scrolling up** to fetch available messages and attachment references in small pages after retained server history. Older pages preserve your position; new live messages offer a **New messages** button when you are reading above the bottom. Available Google history may be incomplete and the configured retention period applies. Historical commands never execute. Conversation discovery supports loading additional inbox pages beyond the initial 25 threads.

Turn **Automatic command replies** off to converse manually without command automation. Manual replies send when you press Enter (Shift+Enter adds a line) and still require a ready transport, an accessible individual conversation, quiet-hour/request-limit checks, and global sending to be enabled. The **Live/Paused** switch in the top bar stops all sending; pausing cancels queued and waiting replies. If a send isn't acknowledged, the text stays in the composer and sending again reuses the same request key. Repeated submission of the same owner compose request cannot queue a second reply; uncertain sends are never retried automatically. Turning capture off stops collection and access through the full inbox, without deleting retained text. These owner preferences are excluded from recipes and backups; backups preserve private-message visibility and deduplication aliases.

In web Commands, create fixed replies with `{input}` or AI instructions; select a model/profile, optional memory, restrictions and approval. In **Settings > Providers**, save an OpenRouter key or an OpenAI-compatible API base URL and access key; refresh either model catalog from **Settings > AI models**. The compatible endpoint can be OpenAI or a local gateway that implements `/models` and `/chat/completions`. Keys and endpoint settings are encrypted and never returned with a key to either client. Fixture AI uses a deterministic synthetic provider and requires no real key.

AI input and opted-in context are sent to the provider selected by the model: OpenRouter or the configured compatible endpoint. Generation parameters are validated against cached model capabilities; OpenRouter requests also enforce `require_parameters`. Model-default reasoning/temperature are omitted. New default-model selections seed Low when advertised; explicit/legacy preferences remain intact. Compatible catalogs without pricing metadata are blocked while the daily AI budget is enabled, because the service cannot reserve an estimated cost; set the budget to 0 only if you want to rely on the provider's own spending controls. Queued inputs, instructions, context and generation controls are snapshots. Revoking memory or resetting it invalidates queued context and prevents an in-flight answer from repopulating deleted memory.

Approvals and queued plans expire after two minutes in this service. Requests are counted globally in rolling-hour/UTC-day limits, including manual messages. Quiet hours withhold requests rather than deferring them. The daily monetary control is a reported-spend threshold with a conservative pre-call estimate, not a provider billing guarantee; missing pricing/unknown spend blocks further AI when the threshold is enabled. Zero disables that threshold. No automatic provider retries are added, and uncertain message submissions are never retried.

Server recipe import replaces configuration with commands disabled; Android `smsbot.commands` v1 packs append disabled commands, reject duplicate names/sender restrictions, preserve explicit generation overrides and omit SMS segment settings. Server keys, enrollments and local phone secrets are never automatically migrated.

See [feature parity](docs/PARITY.md), [recovery](docs/BACKUP.md), [API contract](api/openapi.yaml) and [validation](docs/VALIDATION.md).

![Synthetic web workspace](docs/images/server-workspace.png)

![Synthetic web command editor](docs/images/server-commands.png)


## Local history synchronization

The owner inbox now uses a separate `internal/history` read-job service. Enabling
collection starts resumable available-history ingestion and periodic recent
reconciliation, independently of bot replies and global sending pause. The Archive
panel can request all available history or an RFC3339-backed date
cutoff, cancel a backfill, and show saved counts, state and last successful sync.
Stopping collection preserves the owner-readable local archive; it does not grant
legacy clients access. Groups remain excluded and fetching/sending rechecks exact
participant identity. Local text pages and search work while the phone is offline.

Archive retention is separate from the bot's job retention: zero keeps archived
messages indefinitely, otherwise the archive policy expires them. Already expired
messages must be recovered from available upstream history. Schema v5 adds encrypted
verified archive identities/checkpoints, encrypted observed content revisions, a
local change feed and a conversation/time index. Existing service backups include
these records but restore jobs cancelled, without collection consent. Historical
commands and restored messages never execute. Previously observed revisions can be
read through the owner API; upstream intermediate edits are not guaranteed. Recent
reconciliation overlaps by 30 minutes and pages back to the prior successful sync;
older ranges can be rescanned explicitly. No arbitrary old-edit detection guarantee
is made without a covering scan or update event.

The UI preloads local pages near the top and virtualizes long loaded histories.
A local change cursor refreshes edits and new rows beyond the newest 50-message
page. Original files are preserved by the separate archive service; encrypted streaming
backups and readable exports include saved originals and explicit missing counts. Live Google
sync/edit acceptance is pending; automated coverage uses offline fixtures.


### Understanding archive progress

Conversations shows the enabled collection scope and archive retention. Background
archiving continues while tabs are closed and is independent of bot enrollment and
Live/Pause. Each conversation separates visible-section checks, recent reconciliation
and older-history import progress. The import form restores its saved date; a blank
date requests all available history. Explicit requests run ahead of routine inbox
discovery in bounded, fair batches. Progress counts checked pages/messages separately
from newly saved or updated rows, so zero newly saved is not a frozen or empty archive.

A capture check names the affected conversation, approximate time range and reason,
then automatically retries available-history reads while current access permits.
Completed checks say available range rechecked; they do not establish completeness.
Unknown-scope failures remain explicitly unverified. Gap records are encrypted and
owner-only. Attachments continue downloading separately. Live Google gap-repair and
manual-import priority acceptance remain pending; automated checks use offline data.


### Archive panel

Use **Archive** in the main navigation for collection scope, all tracked conversation
imports, capture checks, retention, original-file limits and message exports.
**Archive all available history** removes a conversation's import date limit;
optional date limits live under **Advanced import options**. Importing and viewing
history never runs old commands. Collection access remains in Settings > Messages
& privacy. Conversations show messages, search and compose without archive status
or controls; viewed sections still reconcile automatically in the background.
