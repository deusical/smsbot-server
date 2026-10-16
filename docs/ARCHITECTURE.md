# Architecture

Selected stack: Go service, SQLite/local WAL, libgm adapter, React/TypeScript web dashboard, existing Kotlin/Compose Android client. The service owns messaging and bot state; clients use API v1 and can be closed without stopping the bot.

## Boundaries

- `transport`: normalized conversation/incoming contracts, physically separate fixture implementation, opt-in Google adapter. Google cookies/session stay here and in encrypted storage.
- `store`: schema migrations, unique incoming jobs, enrollment, encrypted plans/messages, hashed credentials. SQLite connections are bounded; no network filesystem.
- `bot`: fresh incoming filtering, exact trust, immutable fixed plans, serialized submission, pause, pre-send rechecks and receipt reconciliation.
- `httpapi`: exact Host/origin checks, bounded strict JSON, owner cookies, revocable client bearer credentials, static embedded dashboard.
- `web`: same-origin API, memory-only credentials, system theme, responsive navigation, snapshots polled every three seconds without overlapping refreshes, paused while the page is hidden. It is an installable web app (manifest plus a service worker that caches only the static shell and hashed assets, never `/api/` or `/healthz`); see docs/HOSTING.md for Tailscale HTTPS. Conversation refresh errors are shown separately from server connection failures. SSE and generated API clients are future improvements; polling is explicit in this milestone.
- Google conversation refresh checks up to four independent full-detail requests concurrently, preserving inbox order and publishing the identity cache only after all checks succeed. Exact participant and pre-send identity checks remain required.
- Dashboard state/readiness polling runs independently of settings, activity and conversation discovery, with a separate nonoverlap guard for each refresh lane. Slow startup discovery cannot hold login busy or prevent the paused controls from becoming available when transport readiness changes.
- Dashboard Enable/Pause actions apply the state returned by the pause endpoint immediately, independently of conversation refresh. A revision check prevents state polls started before or during the action from overwriting the confirmed response. Failed mutations retain the previously displayed state and show the error.
- Android: API-backed server screen, Keystore-encrypted connection, dispatch guards. Its existing local engine remains independent and unavailable in server mode.
- Google startup account snapshots (`AccountChange.IsFake` in libgm) are informational and do not invalidate verified readiness. Actual account changes, logout and active-session warnings still pause automation and require owner recovery. Temporary phone/data loss blocks readiness and triggers verified reconnection without changing the owner Live/Pause choice. Diagnostics distinguish those event categories without recording account data.
- Google receive callbacks filter unused events and historical incoming messages before the bounded queue. Queue overflow immediately blocks readiness and wakes the consumer to pause automation, invalidate cached identities and discard command input across the gap. Draining restores queue availability, advances the freshness cutoff and emits sanitized `event_queue_overflow`/`event_queue_drained` diagnostics; it never reenables automation or clears a connection/account failure. Such failures latch independently even if their events are dropped by a full queue. Historical outgoing status events remain eligible for transaction reconciliation.

## Processing

Fresh complete incoming text → exact enrolled individual participant → pause/readiness checks → built-in/custom command routing → encrypted immutable plan and unique persisted job → current enrollment/freshness/identity checks → persisted transaction ID and submitting state → transport call → submitted or unknown_submission.

Pause/unenrollment cancel queued work. Startup recovery marks submitting jobs uncertain, then starts paused and cancels queued work. No automatic retries after invoking a transport. Correlated Google status events match both conversation and transaction ID; statuses do not downgrade. Displayed means Google reports displayed, not proof a human consciously read the text.

History fetching does not feed routing. The default inbox retains accepted command text and replies. The opt-in owner inbox observes complete fresh incoming/outgoing text independently of command acceptance, pause and quiet hours. A bounded queue and four capture workers verify current individual-conversation identity before retention without blocking transport control events. Consent epochs discard work queued under revoked capture policies. Capture lookup failures, overflow or stale queued messages raise an owner-only gap warning without pausing automation. Inbox discovery coalesces requests and caches pages for 30 seconds; individual lookup failures produce explicitly partial pages. Settings validation and history fetches have a shared 20-second deadline and run outside the engine mutex, with final revision/access checks before persistence. Selected access binds stable participant IDs; all-conversation access still excludes groups. Encrypted owner settings and `/inbox/*` APIs are separate from recipe configuration and denied to client credentials. Legacy message/history APIs filter private inbox rows. Schema v3 adds message provenance/status/visibility metadata and upstream-to-transaction aliases without modifying the existing ciphertext scope. Backups preserve visibility/aliases but exclude collection consent. Automatic replies have a separate control, while global pause still stops every send. Settings revisions reject concurrent stale saves; manual owner compose requests use a stable idempotency key and recheck access before submission. An encrypted intent digest binds that key to its original text, so lost acknowledgements can be retried even after pause/cancellation without creating another job.

Phone history imports store available pages without routing commands; retained history has bounded decrypted search and stable cursors. The web scroller merges local, imported and live messages by canonical ID, preserves scroll position on prepend, and leaves readers above the bottom in place during polling. Provider work and retention are implemented. Booting a fixture environment cannot construct the Google adapter. Use separate state directories for fixture/live environments.

## Authentication

Owner initialization writes a random credential to a local owner file; the database stores only its hash. Login creates a random browser session (12 hours, or until sign-out when the owner opts into "Keep me signed in"; that cookie renews while in use, and Settings > Devices can sign out every browser) with HttpOnly/SameSite Strict cookies. State-changing browser requests require exact Origin. Login uses strict JSON and trims surrounding whitespace from the pasted credential; there is no failed-attempt lockout, since the owner credential is a long random value. Exact Host validation prevents DNS rebinding. API responses are no-store; CSP blocks remote scripts and framing.

Owners create revocable random Android credentials. Client tokens cannot create/revoke credentials. Invalid bearer credentials never fall back to a browser cookie. Hosting defaults to loopback. Nonloopback binds require direct TLS and an exact HTTPS origin. A configured private HTTPS proxy can front loopback; forwarded headers never determine trust or origin.

## Cross-repository workflow

Service source is a separate repository. Android retains its existing repository/license. API additions precede native client changes; the client verifies api_version before activating server mode. Entering server mode disables local dispatch under the Android dispatch mutex and cancels pending work. Returning requires confirmed server pause; local remains paused. An unreachable server does not authorize fallback.

## Configuration, generation and recovery

Configuration/provider keys and queued context are encrypted with separate scopes. Schema v2 adds an encrypted scoped memory table and a usage ledger, preserving v1 data and normalizing message timestamps for stable pagination. A serialized dispatcher releases its control mutex during provider generation, allowing pause/cancellation; it rechecks current enrollment/identity, command availability, approval, quiet hours and age before submission. Memory epochs invalidate old context on reset/revocation.

The fixture provider has no HTTP client. OpenRouter remains the default; the owner can also save an encrypted OpenAI-compatible base URL and key. Cleartext HTTP is limited to loopback for local gateways, while other endpoints require HTTPS. Catalog refresh is explicit: OpenRouter models are public, and compatible model refresh uses the configured key. Compatible model IDs are namespaced and retain their remote model ID for generation, so both catalogs can coexist without model-name collisions. Compatible calls have the same bounded timeout and response-size limit as OpenRouter. The reported-spend threshold fails closed on unknown pricing or usage. Manual compose uses the same queue/submission protections.

Backup/restore runs under the offline state lock, excludes credentials/provider/pairing/memory, retains usage and deduplication evidence, and restores all eligible work paused/cancelled or uncertain. Authentication diagnostics contain event identifiers, times and numeric status/category only. Successful token-refresh events save immediately; save failure pauses automation. The running observation service must be cleanly restarted by its owner to load a new binary.

## Conversation attachments

Schema v4 adds `message_attachments`, with one encrypted payload per message and a cascading foreign key. Payloads contain sanitized file metadata, inline/local bytes when available, and upstream media references/decryption keys; only metadata is exposed in owner history. Existing message ciphertext scopes stay unchanged. Exact upstream/transaction alias matching also reconciles media; local sent originals survive phone echoes. Retention deletes attachment payloads with their message. Offline backups preserve encrypted attachments and accept older schema backups.

The Google adapter normalizes text and media parts for both fresh events and available history. A message containing any attachment cannot execute a command, including a command-like caption. Full-inbox capture consent, exact participant checks and group exclusion apply to file-only messages. Legacy import/client paths remain text-only. Remote bytes are retrieved only when an owner previews/downloads a file, with access and identity checked again after retrieval. Remote references may expire; retained metadata is not an archive of upstream bytes. Image previews can fall back to an available upstream thumbnail, but original-file downloads never silently return a thumbnail.

Owner-only inbox preferences include attachment count, per-file MiB and combined MiB limits. Defaults are four files, 10 MiB each and 20 MiB combined. Zero disables each limit independently. Owner compose derives its JSON ceiling from the configured finite limits and base64/metadata overhead, or disables the ceiling when the policy is unlimited; there is no fixed 28 MiB upload ceiling. Nonempty files remain required. Downloads/previews use the current per-file policy; live/history normalization no longer drops inline files at 10 MiB or 32 media parts. Settings validation prevents negative values and integer overflow. Unlimited remains constrained by host memory/storage, SQLite and upstream limits, because uploads/downloads currently buffer bytes. Preferences stay encrypted and outside recipe/backup consent. Current limits are rechecked before dispatch. The durable encrypted plan and request digest bind exact file content and metadata. Uploads use the `generating` state outside the engine control mutex; cancellation/pause remain responsive. Current job state, consent, identity, freshness and sending policy are checked after upload and before persisting `submitting` with its transaction ID. Google submission is invoked once; uncertain submissions are never automatically retried. The pinned library's media upload/download setup calls do not accept a context; its HTTP client timeouts bound those calls, and cancellation is rechecked before submission. No background continuation can send after cancellation.

Attachment expiry is enforced with a keyed read-only timestamp check; previews and range requests do not run table-wide retention writes. The owner-only content endpoint supports byte ranges, no-store, nosniff and safe Content-Disposition. Only an explicit image/audio/video allowlist can render inline; active documents always download with a sandbox CSP. Browser file previews use temporary object URLs that are revoked. The service worker never caches API/file content. Additive API v1 attachment support is advertised by the `attachments` capability; legacy Android clients receive no new private-file access.


## Local history synchronization

The owner inbox now uses a separate `internal/history` read-job service. Enabling
collection starts resumable available-history ingestion and periodic recent
reconciliation, independently of bot replies and global sending pause. The thread's
History sync panel can request all available history or an RFC3339-backed date
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

Migration protects existing private and opted-in conversation history from legacy startup expiry until verified archive bindings have been recorded. These pending preservation markers grant no collection or viewing rights.

History fetches no longer share a mutex with live capture. Per-message change
watermarks suppress stale concurrent snapshot writes; consent/job generations are
rechecked before persistence. Successful network reads use a separate local commit
budget. Verified archive metadata clears pending preservation markers atomically;
background retention also runs without a browser or enabled collection. Stopped
sync does not disable enrolled legacy compose. Inbox retry intents retain their
original endpoint and key even when collection policy changes after submission.

Recent reconciliation and deep backfill retain independent encrypted retry times,
failure counters and sanitized errors. A recent failure ends that conversation's
current tick; subsequent ticks can run either lane when its own backoff permits.
Inbox history advertises a change cursor only for persisted archives; conversations
without an archive keep polling snapshots for new messages.
Archive UI polling uses the change feed after the initial snapshot, and page reads
and change-feed reads serialize so an older snapshot cannot resurrect deleted rows
or overwrite an already-applied edit.


## Original-file archive module

`internal/archive` owns streaming encrypted originals, retry metadata, orphan cleanup,
snapshot exports and staged offline restoration. Its host interface exposes only
identity/consent authorization, original reads and conditional metadata updates; it
cannot submit messages or dispatch commands. An owner-only API exposes policy and
one persistent export job. Schema v5 is unchanged; encrypted attachment payloads gain
optional blob/digest/retry fields. Operational backups materialize referenced originals
for compatibility, retaining their existing aggregate ceiling. Archive-specific backups
stream originals separately. Saved file metadata survives unchanged upstream echoes;
a changed media source invalidates the old reference. File publication compares the
source again after download and rechecks consent. Random blob names and encrypted
chunks avoid plaintext files on disk except explicitly requested readable exports.


Idle file sweeps use a one-minute cooldown, with an inexpensive change-sequence
check to wake for newly observed messages or files. Unavailable sources remain
terminal until a new source descriptor is observed. Policy-limited work resumes
only after limits or committed blob usage changes. Locally submitted originals
remain authoritative across exact transaction/alias echoes even after inline bytes
are normalized into blobs and phone-side part IDs change. Received attachments
with changed remote sources still invalidate their old blob references.

Google inline image bytes are preview-only. Original reads request full-size media
using the exact upstream message/action IDs, then await the matching update or
read back that exact part. Historical media updates can satisfy an outstanding
read without entering command routing. Requests are bounded, coalesced and
cancelled with their callers. Pending originals report unknown size rather than
the inline preview size. Legacy imported previews are repaired under current
consent and exact identity checks, while locally submitted originals are preserved.

## Automatic viewed-section reconciliation

The owner dashboard shows local history immediately. A debounced viewport range,
with nearby messages for overlap, requests an independent durable refresh lane
when collection is allowed. It renews while visible and on return to the page;
no explicit refresh button is required. The owner-only `POST
/api/v1/inbox/conversations/{id}/refresh` accepts RFC3339 `since` and optional
`until` bounds. `archive_refresh` advertises support. Google can start at the
view's upper bound; pages continue to the lower bound to repair gaps as well as
previously stored text, receipt and attachment metadata. Absence from a returned
page never deletes local history or proves that Google history is complete.

Refresh cursor/state/backoff and focus timestamps are encrypted in the existing
archive record, independently of deep-import progress. Superseding a range bumps
the generation so old reads cannot publish. Consent revisions, exact participant
checks, group exclusion and per-message change watermarks still guard commits.
An active section gets the first history turn, followed by one round-robin
background conversation. Recent reconciliation continues; completed old-history
scans are revisited daily within existing date/retention limits, while cancelled
deep imports remain cancelled. Owner-visible states distinguish saved data,
checking, retrying and when the available section was last checked.

Original-file sweeps prioritize the recently viewed time range. Every third turn
is reserved for ordinary background work; one bounded file attempt per turn lets
view changes and revocation take effect between reads. Focus expires after two
minutes without renewal. Current file policy, encrypted streaming publication,
source comparison and consent checks apply to both priorities. The UI's change
feed updates file state and previews when originals are saved.


## Verified phone reconnection

Temporary listen/ping/phone-readiness failures latch transport unavailability independently of the receive queue. A single recovery worker checks the existing paired client with a 20-second deadline, retries with exponential backoff and jitter (capped at five minutes), and re-arms a missing receive stream after repeated failures. Recovery hints expedite checks but do not establish readiness. Successful recovery requires a responding phone, a connected stream and a saved encrypted session; a generation check prevents a stale success from overriding a newer failure. Authentication, account change, session conflict, fatal stream and storage errors remain blocked for owner intervention.

Connection interruption cancels queued, generating and approval work without changing the persisted owner Live/Pause choice. Generation/upload contexts are cancelled; submitted/uncertain intents retain their evidence and are never replayed. Readiness gates every send while offline. Recovery clears cached identities and advances the command freshness cutoff, so only new post-verification commands can execute. Receipts still reconcile during the gap. Explicit owner pause remains authoritative, and process startup still pauses. API v1 adds sanitized connection_state and connection_retry_at fields; the dashboard distinguishes reconnecting from owner-action failures and explains automatic resumption when Live.


## Sync scheduling, progress and capture checks

Explicit older-history requests carry an encrypted requested time and advance one
bounded page before routine discovery on each worker turn. Multiple explicit jobs
rotate by last attempt. Visible-section reads keep their own checkpoint; ordinary
recent and deep background reconciliation still get a turn. Cancellation and
changed requests invalidate in-flight work through the existing generation check.
Existing date-limited pending requests receive the same priority on upgrade. Daily maintenance is identified as automatic rather than a new owner request.

Older-history progress records pages and messages checked, newly saved messages,
updated messages, oldest scanned timestamp and last progress time. Recent and
visible-section reads do not inflate these per-import counters. The date form
loads the saved request and polling preserves unsaved date edits. The dashboard
shows collection scope and retention, recent-check time, independent viewed-section
status and older-import waiting/retry/completion states. Available pages checked
never means guaranteed complete Google history. Originals remain a separate job.

Capture failures aggregate bounded metadata in memory without blocking the receive
callback on SQL or phone RPCs. A capture worker flushes conversation, approximate
time bounds and a fixed reason into encrypted capture-gap settings. Pending metadata
is visible immediately; persisted checkpoints survive process restarts. A sudden
process termination before the asynchronous flush can lose that pending metadata.
Exceeding the metadata capacity preserves an explicit unknown-scope record rather
than claiming a known repair range. No bodies, media keys or upstream error text are
retained in these records. Owner state includes sanitized ranges and statuses;
client credentials never receive them.

A separate repair cursor checks one eligible gap page per history turn with bounded
calls and backoff. Current collection consent, exact individual identity, retention,
message change watermarks and request generations are rechecked before storing any
page. Rechecking never routes old commands, alters Live/Pause or replays sends.
Concurrent newer failures invalidate an older repair's completion checkpoint.
Successful repair reports only that the available range was rechecked, and clears
the active warning for that range; unavailable history can still contain gaps.
Unknown scope is explicitly unverified and cannot be automatically declared repaired.


Routine archive-scope and conversation-sync explanations can be hidden with close
buttons and reopened from compact controls. Two boolean display preferences survive
navigation/reload in tab session storage (memory fallback when storage is blocked);
no conversation identifiers, messages, API state or credentials are stored there.
Hidden panels stay mounted so polling, automatic checks and date drafts continue.
Errors remain visible outside the hidden details with a review action. Display
controls never call sync cancellation, change collection consent or affect sending.

## CI execution

Offline checks run once per pull request commit and on main pushes. Superseded
pull request runs are cancelled. A shared fixture build feeds backend checks and
two browser shards in parallel; each browser runner keeps one worker and its own
fixture database. The required `service` check passes only when the build, backend
checks and both browser shards succeed. Race tests, vet, Go vulnerability scanning,
production dependency auditing and the complete browser suite remain required.


## Release build size and caching

The local build and CI synchronize the embedded dashboard file set against Vite's
current output, removing obsolete content-hashed files while retaining `.keep`.
Unchanged files are not rewritten. Release builds (PowerShell, CI and Docker) use
`-trimpath -ldflags="-s -w"`; debugger symbol tables are omitted, with an explicit
PowerShell `-DebugSymbols` option for diagnostic builds. Dependencies and features
remain unchanged. Local default validation uses Go's test cache; CI race tests
still force execution. Optional local `-SkipInstall` and `-SkipChecks` switches
support quick rebuilds without weakening default validation. BuildKit mounts retain
npm downloads, Go modules and compiler output across Docker source rebuilds.
