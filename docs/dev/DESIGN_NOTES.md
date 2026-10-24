# Design notes

## Message interactions

`message_interactions` stores sealed upstream identities, per-message protocol,
reply targets and reaction snapshots, independent of the thread's current protocol.
Non-nil snapshots are complete; nil observations preserve existing metadata.
History reads carry their start time so an older in-flight scan cannot replace a
newer observation. Metadata changes emit `message_changes` without creating content
revisions. The upstream protocol provides no definitive reaction version; delayed
upstream snapshots remain a documented limitation.

Replies extend the existing immutable compose intent and its idempotency proof.
Only the owner API resolves local target IDs to encrypted wire IDs. The dispatcher
rechecks protocol and sends a native RCS reply with force-RCS enabled.
Reactions use the existing serialized dispatcher with a separate mutation branch,
encrypted durable `interaction_intents`, version checks, and persisted submitting
state. They never enter text-send, message receipt, or command execution paths.
Queued, submitted, and uncertain actions lock the target until definitive failure
or an observed intended state. Restart never retries an uncertain submission.
Reaction confirmation is `reaction_confirmed`, separate from message read receipts.
Service backups and archive exports/restores preserve message interaction metadata;
service backups also retain mutation evidence without resuming work.

The shared UI menu reads per-message eligibility; unsupported SMS/MMS actions and
outbound editing are omitted. The pinned companion transport has no outbound edit
operation. Observed content revisions continue through the existing archive path.

Detailed design notes, grouped by subsystem. Start with [ARCHITECTURE.md](ARCHITECTURE.md) for the overview and the safety rules; this file records the finer points. The service owns messaging and bot state; the dashboard uses API v1 and can be closed without stopping the bot.

## Boundaries

- `transport`: normalized conversation/incoming contracts, physically separate fixture implementation, opt-in Google adapter. Google cookies/session stay here and in encrypted storage.
- `store`: schema migrations, unique incoming jobs, enrollment, encrypted plans/messages, hashed credentials. SQLite connections are bounded; no network filesystem.
- `bot`: fresh incoming filtering, exact trust, immutable fixed plans, serialized submission, pause, pre-send rechecks and receipt reconciliation.
- `httpapi`: exact Host/origin checks, bounded strict JSON, owner session cookies, static embedded dashboard.
- `web`: same-origin API, memory-only credentials, system theme, responsive navigation, snapshots polled every three seconds without overlapping refreshes, paused while the page is hidden. It is an installable web app (manifest plus a service worker that caches only the static shell and hashed assets, never `/api/` or `/healthz`); see docs/HOSTING.md for Tailscale HTTPS. Conversation refresh errors are shown separately from server connection failures. SSE and generated API clients are future improvements; polling is explicit in this milestone.
- Google conversation refresh checks up to four independent full-detail requests concurrently, preserving inbox order and publishing the identity cache only after all checks succeed. Exact participant and pre-send identity checks remain required.
- Dashboard state/readiness polling runs independently of settings, activity and conversation discovery, with a separate nonoverlap guard for each refresh lane. Slow startup discovery cannot hold login busy or prevent the paused controls from becoming available when transport readiness changes.
- Dashboard Enable/Pause actions apply the state returned by the pause endpoint immediately, independently of conversation refresh. A revision check prevents state polls started before or during the action from overwriting the confirmed response. Failed mutations retain the previously displayed state and show the error.
- Google startup account snapshots (`AccountChange.IsFake` in libgm) are informational and do not invalidate verified readiness. Actual account changes, logout and active-session warnings still pause automation and require owner recovery. Temporary phone/data loss blocks readiness and triggers verified reconnection without changing the owner Live/Pause choice. Diagnostics distinguish those event categories without recording account data.
- Google receive callbacks filter unused events and historical incoming messages before the bounded queue. Queue overflow immediately blocks readiness and wakes the consumer to pause automation, invalidate cached identities and discard command input across the gap. Draining restores queue availability, advances the freshness cutoff and emits sanitized `event_queue_overflow`/`event_queue_drained` diagnostics; it never reenables automation or clears a connection/account failure. Such failures latch independently even if their events are dropped by a full queue. Historical outgoing status events remain eligible for transaction reconciliation.

## Processing

Fresh complete incoming text → exact enrolled individual participant → pause/readiness checks → built-in/custom command routing → encrypted immutable plan and unique persisted job → current enrollment/freshness/identity checks → persisted transaction ID and submitting state → transport call → submitted or unknown_submission.

Pause/unenrollment cancel queued work. Startup recovery marks submitting jobs uncertain, then starts paused and cancels queued work. No automatic retries after invoking a transport. Correlated Google status events match both conversation and transaction ID; statuses do not downgrade. Displayed means Google reports displayed, not proof a human consciously read the text.

History fetching does not feed routing. The default inbox retains accepted command text and replies. The opt-in owner inbox observes complete fresh incoming/outgoing text independently of command acceptance, pause and quiet hours. A bounded queue and four capture workers verify current individual-conversation identity before retention without blocking transport control events. Consent epochs discard work queued under revoked capture policies. Capture lookup failures, overflow or stale queued messages raise an owner-only gap warning without pausing automation. Inbox discovery coalesces requests and keeps pages until the phone reports activity, spacing the resulting phone reads at least 10 seconds apart; a page served from before the latest activity is marked `stale` so the dashboard asks again. Individual lookup failures produce explicitly partial pages. Settings validation and history fetches have a shared 20-second deadline and run outside the engine mutex, with final revision/access checks before persistence. Selected access binds stable participant IDs; all-conversation access still excludes groups. Encrypted owner settings and `/inbox/*` APIs are separate from recipe configuration. Legacy message/history APIs filter private inbox rows. Schema v3 adds message provenance/status/visibility metadata and upstream-to-transaction aliases without modifying the existing ciphertext scope. Backups preserve visibility/aliases but exclude collection consent. Automatic replies have a separate control, while global pause still stops every send. Settings revisions reject concurrent stale saves; manual owner compose requests use a stable idempotency key and recheck access before submission. An encrypted intent digest binds that key to its original text, so lost acknowledgements can be retried even after pause/cancellation without creating another job.

Phone history imports store available pages without routing commands; retained history has bounded decrypted search and stable cursors. The web scroller merges local, imported and live messages by canonical ID, preserves scroll position on prepend, and leaves readers above the bottom in place during polling. Provider work and retention are implemented. Booting a fixture environment cannot construct the Google adapter. Use separate state directories for fixture/live environments.

## Authentication

Owner initialization writes a random credential to a local owner file; the database stores only its hash. Login creates a random browser session (12 hours, or until sign-out when the owner opts into "Keep me signed in"; that cookie renews while in use, and Settings > Devices can sign out every browser) with HttpOnly/SameSite Strict cookies. State-changing browser requests require exact Origin. Login uses strict JSON and trims surrounding whitespace from the pasted credential; there is no failed-attempt lockout, since the owner credential is a long random value. Exact Host validation prevents DNS rebinding. API responses are no-store; CSP blocks remote scripts and framing.

Only owner browser sessions authenticate API calls; bearer credentials are never honoured. Hosting defaults to loopback. Nonloopback binds require direct TLS and an exact HTTPS origin. A configured private HTTPS proxy can front loopback; forwarded headers never determine trust or origin.

## Configuration, generation and recovery

Configuration/provider keys and queued context are encrypted with separate scopes. Schema v2 adds an encrypted scoped memory table and a usage ledger, preserving v1 data and normalizing message timestamps for stable pagination. A serialized dispatcher releases its control mutex during provider generation, allowing pause/cancellation; it rechecks current enrollment/identity, command availability, approval, quiet hours and age before submission. Memory epochs invalidate old context on reset/revocation.

The fixture provider has no HTTP client. OpenRouter remains the default; the owner can also save an encrypted OpenAI-compatible base URL and key. Cleartext HTTP is limited to loopback for local gateways, while other endpoints require HTTPS. Catalog refresh is explicit: OpenRouter models are public, and compatible model refresh uses the configured key. Compatible model IDs are namespaced and retain their remote model ID for generation, so both catalogs can coexist without model-name collisions. Compatible calls have the same bounded timeout and response-size limit as OpenRouter. The reported-spend threshold fails closed on unknown pricing or usage. Manual compose uses the same queue/submission protections.

Backup/restore runs under the offline state lock, excludes credentials/provider/pairing/memory, retains usage and deduplication evidence, and restores all eligible work paused/cancelled or uncertain. Authentication diagnostics contain event identifiers, times and numeric status/category only. Successful token-refresh events save immediately; save failure pauses automation. The running observation service must be cleanly restarted by its owner to load a new binary.

## Conversation attachments

Schema v4 adds `message_attachments`, with one encrypted payload per message and a cascading foreign key. Payloads contain sanitized file metadata, inline/local bytes when available, and upstream media references/decryption keys; only metadata is exposed in owner history. Existing message ciphertext scopes stay unchanged. Exact upstream/transaction alias matching also reconciles media; local sent originals survive phone echoes. Retention deletes attachment payloads with their message. Offline backups preserve encrypted attachments and accept older schema backups.

The Google adapter normalizes text and media parts for both fresh events and available history. A message containing any attachment cannot execute a command, including a command-like caption. Full-inbox capture consent, exact participant checks and group exclusion apply to file-only messages. Legacy import/client paths remain text-only. Remote bytes are retrieved only when an owner previews/downloads a file, with access and identity checked again after retrieval. Remote references may expire; retained metadata is not an archive of upstream bytes. Image previews can fall back to an available upstream thumbnail, but original-file downloads never silently return a thumbnail.

Owner-only inbox preferences include attachment count, per-file MiB and combined MiB limits. Defaults are four files, 10 MiB each and 20 MiB combined. Zero disables each limit independently. Owner compose derives its JSON ceiling from the configured finite limits and base64/metadata overhead, or disables the ceiling when the policy is unlimited; there is no fixed 28 MiB upload ceiling. Nonempty files remain required. Downloads/previews use the current per-file policy; live/history normalization no longer drops inline files at 10 MiB or 32 media parts. Settings validation prevents negative values and integer overflow. Unlimited remains constrained by host memory/storage, SQLite and upstream limits, because uploads/downloads currently buffer bytes. Preferences stay encrypted and outside recipe/backup consent. Current limits are rechecked before dispatch. The durable encrypted plan and request digest bind exact file content and metadata. Uploads use the `generating` state outside the engine control mutex; cancellation/pause remain responsive. Current job state, consent, identity, freshness and sending policy are checked after upload and before persisting `submitting` with its transaction ID. Google submission is invoked once; uncertain submissions are never automatically retried. The pinned library's media upload/download setup calls do not accept a context; its HTTP client timeouts bound those calls, and cancellation is rechecked before submission. No background continuation can send after cancellation.

Attachment expiry is enforced with a keyed read-only timestamp check; previews and range requests do not run table-wide retention writes. The owner-only content endpoint supports byte ranges, no-store, nosniff and safe Content-Disposition. Only an explicit image/audio/video allowlist can render inline; active documents always download with a sandbox CSP. Browser file previews use temporary object URLs that are revoked. The service worker never caches API/file content. Additive API v1 attachment support is advertised by the `attachments` capability.


## Local history synchronization

The owner inbox now uses a separate `internal/history` read-job service. Enabling
collection starts resumable available-history ingestion and periodic recent
reconciliation, independently of bot replies and global sending pause. The Archive
panel can request all available history or an RFC3339-backed date
cutoff, cancel a backfill, and show saved counts, state and last successful sync.
Stopping collection preserves the owner-readable local archive; it does not expose
messages through the legacy bot history APIs. Groups remain excluded and fetching/sending rechecks exact
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

### History entry points and their grants

Three routes read upstream history. They share the bounded read sequence (identity
and access check, change watermark, one page for that exact participant, identity
and access check again, policy recheck under the engine lock) and the store's
message/alias/attachment persistence, but never each other's grants:

| Route | Grant | Saves | Retention | Counts |
|---|---|---|---|---|
| `POST /conversations/{id}/history/import` (legacy) | Current bot enrollment of the exact participant | Text only; cannot rewrite or expose a private capture | Bot job retention | `imported` |
| `POST /inbox/conversations/{id}/history/import` | Collection consent with "load older" | Text, files and interactions; newer live observations win | Archive retention | `imported`, `duplicates` (unchanged or updated), `skipped` |
| Archive jobs (`internal/history`) | Collection consent at the revision read before the fetch | As inbox, per workflow checkpoint | Archive retention and import cutoff | Per-import progress |

None of them routes commands, changes Live/Pause, or proves that upstream history is
complete: what is saved locally is what was read, and absence from a page deletes
nothing.
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
when collection is allowed. It is checked when a section opens and on return to
the page; while it stays open, pushed messages and capture-gap repair keep it
current. No explicit refresh button is required. The owner-only `POST
/api/v1/inbox/conversations/{id}/refresh` accepts RFC3339 `since` and optional
`until` bounds. `archive_refresh` advertises support. Google can start at the
view's upper bound; pages continue to the lower bound to repair gaps as well as
previously stored text, receipt and attachment metadata. Absence from a returned
page never deletes local history or proves that Google history is complete.

Refresh cursor/state/backoff and focus timestamps are encrypted in the existing
archive record, independently of deep-import progress. Superseding a range bumps
the view's own generation (`refresh_generation`) so old view reads cannot publish;
it does not disturb an import page already in flight. Consent revisions, exact participant
checks, group exclusion and per-message change watermarks still guard commits.
An active section gets the first history turn, followed by one round-robin
background conversation. Recent reconciliation continues; completed old-history
scans are not revisited automatically (archiving again re-scans within existing
date/retention limits), and cancelled deep imports remain cancelled. Owner-visible states distinguish saved data,
checking, retrying and when the available section was last checked.

Original-file sweeps prioritize the recently viewed time range. Every third turn
is reserved for ordinary background work; one bounded file attempt per turn lets
view changes and revocation take effect between reads. Focus expires after two
minutes without renewal. Current file policy, encrypted streaming publication,
source comparison and consent checks apply to both priorities. The UI's change
feed updates file state and previews when originals are saved.


## Verified phone reconnection

Temporary listen/ping/phone-readiness failures latch transport unavailability independently of the receive queue. A single recovery worker checks the existing paired client with a 20-second deadline, retries with exponential backoff and jitter (capped at five minutes), and re-arms a missing receive stream after repeated failures. Recovery hints expedite checks but do not establish readiness. Successful recovery requires a responding phone, a connected stream and a saved encrypted session; a generation check prevents a stale success from overriding a newer failure. Authentication, account change, session conflict, fatal stream and storage errors remain blocked for owner intervention.

Connection interruption cancels queued, generating and approval work without changing the persisted owner Live/Pause choice. Generation/upload contexts are cancelled; submitted/uncertain intents retain their evidence and are never replayed. Readiness gates every send while offline. Recovery clears cached identities and advances the command freshness cutoff, so only new post-verification commands can execute. Receipts still reconcile during the gap. Explicit owner pause remains authoritative, and process startup still pauses. If the phone does not answer the startup readiness check, the service still starts, unready, and the same recovery worker verifies the phone with backoff; only rejected credentials stop startup. Exiting instead made systemd restart the service, and probe the phone, every minute until it answered. API v1 adds sanitized connection_state and connection_retry_at fields; the dashboard distinguishes reconnecting from owner-action failures and explains automatic resumption when Live.


## Sync scheduling, progress and capture checks

Explicit older-history requests carry an encrypted requested time and advance one
bounded page before routine discovery on each worker turn. Multiple explicit jobs
rotate by last attempt. Visible-section reads keep their own checkpoint; ordinary
recent and deep background reconciliation still get a turn.

The archive record holds one checkpoint per workflow: import (versioned by
`generation`), recent reconciliation, and viewed section (versioned by
`refresh_generation`), plus shared identity, focus and page totals. The stored and
API JSON stays flat. Each step reads the record before its fetch and commits through
one transactional update that changes only its own checkpoint, after consent,
participant and version checks; so steps never erase one another's progress.
Cancelling or restarting an import rejects that import's in-flight page; routine
recent reconciliation and gap repair continue while collection remains enabled.
Existing date-limited pending requests receive the same priority on upgrade. Imports of newly discovered conversations are identified as automatic rather than a new owner request.

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
retained in these records. Owner state includes sanitized ranges and statuses.

A separate repair cursor checks one eligible gap page per history turn with bounded
calls and backoff. Current collection consent, exact individual identity, retention,
message change watermarks and request generations are rechecked before storing any
page. Rechecking never routes old commands, alters Live/Pause or replays sends.
Concurrent newer failures invalidate an older repair's completion checkpoint.
Successful repair reports only that the available range was rechecked, and clears
the active warning for that range; unavailable history can still contain gaps.
Unknown scope is explicitly unverified and cannot be automatically declared repaired.
A gap in a conversation that collection no longer applies to (a group, read-only,
or not selected) is closed as `not_collected` after its identity lookup, instead of
retrying; it no longer counts as an active warning. Repeated lookup failures back
off like other history retries, up to one attempt a day.


## Phone wake budget

Phone RPCs can activate Messages, but request counts are not physical wake counts.
Besides the in-memory counters in `/state` (totals since start and the last hour),
every adapter request is written to the service log as
`Phone request: {"at":…,"kind":…,"purpose":…}`, so traffic can be reviewed after the
fact and across restarts (for example with `journalctl --user -u smsbot | grep
'Phone request'`). The line carries no identifiers or content.
History lanes have independent deadlines. A queued import advances from its saved
checkpoint even if discovery fails; it does not accelerate unrelated recent checks.
Automatic imports rotate by stable conversation ID, independent of discovery-page
ordering. Discovery continues pages at the active cadence until the inbox pass ends,
then waits 15 minutes. Routine fallback reconciliation retains the original idle
cadence of one conversation per 15-minute turn, with stable rotation; this is not a
promise to recheck every conversation every 15 minutes. Push capture and viewed-range
refreshes remain immediate. Unfinished recent scans continue at the active cadence;
future retry deadlines sleep until eligible. The fallback/rotation hint persists
in encrypted settings; the durable LastSync still covers the disconnected interval.

Messages that arrive while live capture is not receiving events are caught up
promptly rather than waiting for the slow fallback rotation. The transport records
each connection outage (readiness lost, or an event-queue overflow) once the
connection is verified again, and wakes the history worker. The service start counts
as an outage beginning at the newest saved message. The worker then runs a discovery
pass at once (list pages only), and every allowed
conversation whose phone-reported last activity is at or after the outage start gets
a recent scan that pages back to 30 minutes before it, extending any scan already in
progress. Conversations with no activity since cost nothing. The window stays open
until a discovery pass that started after the outage completes.
Exact before/after page identity checks, consent and generation checks remain live.

The conversation list is read from the phone when the phone reports activity,
after reconnection or queue overflow (which clear cached identities), and once
when the dashboard opens or returns to the foreground. That foreground read,
requested with `refresh=1` on `GET /api/v1/inbox/conversations`, is honoured at
most once a minute across clients, for changes the phone may not push. A partial
page (a lookup or the read failed) is retried after 30 seconds rather than kept.
Dashboard polling otherwise reads only server state. Conversation details are cached for
10 minutes; sends, reactions and read receipts still verify the conversation live.

After asking the phone for an original file, the transport reads the message back
at once (the phone may already have uploaded it without another push), then at 4
and 12 seconds, within the 20-second request deadline; the push normally finishes
the request first. `GET /api/v1/state` reports in-memory counts of phone requests
by kind, in total and for the last hour, with no identifiers or content. They
reset on restart. `phone_request_purposes` attributes adapter attempts to bounded
work categories. `phone_relay_requests` also counts libgm control RPCs and HTTP
retries by action, with accepted/failed HTTP outcomes. HTTP acceptance does not
mean phone execution or delivery; relay acknowledgments and media transfers are
excluded. No wire payloads or identifiers are retained.

Archive operations live in the dedicated owner Archive page. It polls the complete
archive job list independently of paginated conversation discovery and shows scope,
per-conversation progress, capture checks, original-file policy, exports and shared
workspace retention settings. The primary action requests all available history;
date-limited imports remain an explicit advanced option. Mutation epochs reject
stale progress reads, and changing conversations cannot reuse another date draft.
Conversation pages contain no archive diagnostics or controls. A separate headless
hook preserves debounced/coalesced viewport reconciliation, hidden-tab handling,
return-to-page checks and retries independently of archive UI mounting. Message
pagination and attachment viewing remain in Conversations. Collection consent,
participant checks, command freshness and Live/Pause semantics are unchanged.

## Avoiding redundant phone reads

Conversation discovery and the inbox list are built from the phone's list pages
alone (25 conversations a page). A list record carries the same identity fields as
a single-conversation lookup; a field check on the Acer found name, group,
read-only, type, sending number and participant identical for all 388
conversations. The earlier design looked each listed conversation up again and
kept the answers as in-memory "hints", which cost about 390 phone requests after
each restart or reconnect. Groups and read-only conversations are still excluded
from discovery. Inbox list records become the send baselines, as lookups did;
sending, capture, history and original-file access still verify identity with a
live lookup.

Discovery pages are cached until phone activity or a reconnect makes them stale.
The list is ordered by recent activity, and activity only moves a conversation to
the front, so a pass re-reads pages from the front until one comes back identical
to its cached copy (same conversations and next cursor); the pages after it are
then confirmed without a phone read. A message in a recent conversation, or a
reconnect, typically costs one or two list pages instead of all 16. Changes that do
not reorder the list (a rename or deletion further down) are still picked up: a
page is read from the phone at least once a day. A restart starts with an empty
cache and reads every page once.

Original-file policy blocks survive repeated observations of the same source and
worker restarts. The worker checks a block locally before repair, authorization or
download. Its persisted policy token records the limits and usage at the blocked
attempt; retry requires a larger effective byte budget. Growing usage, stricter
limits and irrelevant policy changes do not retry a blocked source. Freeing space,
relaxing the limiting policy or observing a changed source allows acquisition to
resume. Unknown-size downloads get a bounded attempt under the current budget;
smaller eligible sources can still fit. Identity and consent checks continue to
apply to every attempt and commit.

Unavailable originals use exponential retry delays, starting at ten seconds and
growing to a daily retry for sustained failures. Legacy persisted failures adopt
the longer delay locally, without a final phone attempt to rewrite their state.
Repeated observations retain the failure deadline and its schedule version; a
changed source is immediately eligible. Recovery of an unchanged source after
many failures can therefore take up to a day unless the owner requests it directly.

## CI execution

Offline checks run once per pull request commit and on main pushes. Superseded
pull request runs are cancelled. A shared fixture build feeds backend checks and
one browser job in parallel; it keeps one worker and its own fixture database. The
build, backend and browser jobs must all succeed before merging; there is no
separate aggregate job. Race tests, vet, Go vulnerability scanning,
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


## Archive storage overview and verification ranges

The owner Archive page polls `/inbox/archive/stats` every 15 seconds while visible.
It reports physical encrypted originals, the shared database and WAL/SHM, exports,
and temporary archive working files separately. Counts include messages belonging
to retained archive identities. Quotas apply only to originals; the database also
contains service settings and bot records. Measurements can change during file
publication or cleanup and do not imply complete upstream history. Stats reads
never wait on the long-running export publication mutex or access the phone.

Capture-repair metadata records the observation time separately from original
message time. A failed historical edit/status update checks a two-minute window
on either side of the original message, bounded by now. Live failures retain the
original capture interval. Multiple pending failures still conservatively merge
coverage, invalidate the repair cursor and advance its generation. Older saved
ranges remain queued until verified; they are not silently discarded or treated
as proof of an uninterrupted outage. This metadata remains encrypted in settings.


Completed owner imports resolve pending repair ranges only when import start is
at or after both failure observation and range end, and its cutoff/retention
covers the full range. Resolution increments repair generation, invalidating
older in-flight attempts. Restarted repair workers reconcile previously completed
imports using the same coverage and verified-identity checks. Newer failures and
partially covered ranges remain pending. Imports record the settings revision at
scan start; changed-policy or older unversioned scans cannot resolve a range by
assumption. Repair pages record checked counts and
last progress independently from deep-import counters. Failed viewed-section
reads cannot starve independent imports or capture repairs.

Archive loading uses explicit text placeholders, request cancellation on unmount,
eight-second browser deadlines, a five-second owner index SQL deadline and a retry
action for slow reads. Returning to a visible tab refreshes immediately. Technical
repair ranges remain available under a compact expandable repair panel.


The Archive page offers one **Archive conversation** action for both a first
archive and a re-scan. Eligible discovered conversations without a saved archive
appear alongside retained archives. The existing consent/identity-checked sync
endpoint creates the durable job, preserves local messages and revisions, and
reuses an identical in-flight job without resetting its cursor. A completed or
cancelled job starts a fresh full scan. Re-ingested attachment references feed the
existing original-file worker, subject to its enabled policy, quotas and retries;
a finished message scan does not imply original-file download completion.


## Instant send, conversation sync, previews and read receipts

The dashboard clears the composer immediately and shows a pending bubble that
follows the persisted job through queued, submitted and saved. Pending items are
memory-only and cleared on sign-out. Each message has its own idempotency key; a
lost response can be retried with the same key and API route, a definitively
failed job can be edited and resent, and an uncertain submission offers neither.
The server still persists outbound intent before submission and never retries an
uncertain send.

The Google adapter counts phone activity (conversation and message events) from
the receive callback without touching the bounded queue, drops its cached
conversation lists at most every ten seconds, and exposes `conversations_revision`
(phone activity plus the saved-message change sequence) in owner state. The
dashboard refetches the list only when that value changes, with a 30-second
fallback. Typing events are held in memory for about eight seconds, only for
conversations already identified as individual chats, and served from the
owner-only `/inbox/typing`. Outgoing typing indicators are never sent.

The owner conversation list adds `last_message_at` (the later of the phone time
and the newest saved message), phone `unread` and `pinned` flags, and a
120-character `preview` read from locally saved, encrypted messages for
conversations the owner collects or archives. Previews are never logged or
stored. The dashboard orders pinned first, then newest.

`POST /inbox/conversations/{id}/read` marks a viewed conversation read on the
phone. It is on by default through `mark_read_when_viewed` and requires
collection access, an individual conversation, an exact participant match and a
phone that reports the conversation unread. It runs outside the engine lock, is
throttled per conversation, refuses when a message newer than the one the owner
saw has arrived, and never sends a message or changes Live/Pause. The phone's own
read-receipt setting decides whether the other person sees a receipt. Live
Google acceptance of read marking and typing is pending; automated coverage uses
offline fixtures.
