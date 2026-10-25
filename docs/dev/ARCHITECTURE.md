# Architecture

SmsBot Server is a single Go service with an embedded React dashboard. It pairs with
your Google Messages account like Messages for web, keeps an encrypted local copy of
the conversations you choose, and optionally answers commands you define.

```mermaid
flowchart LR
  Phone[Your phone<br/>Google Messages] <--> Google[Google Messages relay]
  Google <--> T[transport<br/>libgm adapter]
  subgraph Service[SmsBot service - one Go process]
    T --> B[bot engine<br/>commands, plans, sends]
    T --> C[collection<br/>consent, capture, gap repair]
    C --> H[history + archive workers<br/>sync, originals, export]
    B -. consent .-> C
    B <--> S[(store<br/>encrypted SQLite)]
    C <--> S
    H <--> S
    A[httpapi<br/>owner API v1] --> B
    A --> C
    A --> S
    W[web dashboard<br/>embedded assets] --- A
  end
  Browser[Browser or installed phone app] <--> W
  B -. optional .-> P[AI provider<br/>OpenRouter or compatible endpoint]
```

## Packages

| Package | Responsibility |
|---|---|
| `cmd/smsbot` | Command line: serve, initialise, pair, update cookies, backup/restore, archive export/restore, `--version`. |
| `internal/app` | Assembles the service: constructs the engine, collection service and workers, runs startup work in order (recover and pause before connecting; preservation before expiry), routes transport events, and owns every worker goroutine. Nothing imports it. |
| `internal/transport` | Normalised conversation and message contracts. The fixture adapter is physically separate from the opt-in Google adapter (libgm). Google cookies and session live here and in encrypted storage only. |
| `internal/store` | SQLite (WAL) with schema migrations, sealed payloads, unique incoming jobs, enrollment, hashed credentials, backups. |
| `internal/vault` | Master key handling and AES-GCM sealing bound to a scope (job, message, session). Windows uses DPAPI; Linux requires a 0600 key file. |
| `internal/bot` | Command routing, immutable plans, provider generation, the serialized dispatcher, pre-send rechecks, receipt reconciliation, owner sends. It reads collection consent but owns no collection work. |
| `internal/collection` | The owner's message collection: settings and consent, observation capture, capture-gap repair, archive sync requests, collected inbox reads and read receipts, and the consent-checked hosts the history and original-file workers use. It never routes commands or submits messages, and does not import `bot`. |
| `internal/history` | Background reads that fill the local archive (recent reconciliation, older imports, viewed-section checks) without ever routing commands, and the bounded read shared by owner-requested history pages. |
| `internal/archive` | Streaming encrypted copies of original files, storage statistics, exports and offline restore. |
| `internal/media` | Attachment validation, limits and intent digests. |
| `internal/httpapi` | Owner-only JSON API, session cookies, exact Host and Origin checks, embedded dashboard. |
| `web/` | React and TypeScript dashboard. It polls the API and caches only static files. |

Collection and the bot share one control lock: consent changes, send decisions and
protected commits serialize on it. Cross-service reactions (stopping automatic
replies cancels in-flight generation) are wired explicitly in `internal/app`.

## Request flow

**Incoming command.** Fresh complete text from an exactly enrolled individual
participant passes pause and readiness checks, is routed to a built-in or custom
command, and becomes an encrypted immutable plan plus a unique persisted job. Just
before sending, enrollment, freshness and identity are checked again. The
transaction ID and a `submitting` state are persisted before the transport call, and
the result is `submitted` or `unknown_submission`.

**Message you type in the dashboard.** The composer clears at once and shows a
pending bubble. The server records the intent under a per-message idempotency key,
then follows the same checks and persisted-before-submit steps. Statuses advance
through Sent, Delivered and Read as Google reports them.

**History.** The archive reads available history in bounded pages. Everything it
saves is stored for viewing only: historical messages never execute commands and
never change Live/Pause.

## Safety rules the code is built around

- **Exact participants.** Only an individual conversation identified by its protocol
  participant ID can be enrolled or collected. Names are labels. Groups are excluded.
- **Fresh input only.** Old, historical, outgoing-echo and file-bearing messages
  never run commands.
- **Persist before submit.** The outbound intent is stored before the transport call.
- **Never retry the uncertain.** After a transport call whose outcome is unknown the
  job becomes `unknown_submission`. It is never retried automatically, and the UI offers
  no retry for it.
- **Pause is authoritative.** Every start is paused. Pausing cancels queued, generating
  and approval work. Pause cannot retract a message already submitted.
- **Immutable plans.** Changing configuration after a request is queued does not change
  that request.
- **No secrets in logs.** Message bodies, contacts, credentials and raw upstream errors
  are never logged. Persistent payloads are encrypted.
- **Owner only.** There is a single owner. API calls need an owner browser session;
  bearer credentials are not accepted.

## Who owns what

- **Startup and lifecycle.** `internal/app` recovers interrupted sends and pauses before
  any transport connects, publishes consent and preservation markers before the legacy
  expiry pass, starts every worker, and stops them all on any exit path.
- **Sync checkpoints.** The archive record embeds one checkpoint per workflow: import
  (versioned by `generation`), recent reconciliation, and viewed section (versioned by
  `refresh_generation`). Each history step commits through one transactional update
  that changes only its own checkpoint (`store.UpdateArchive`, `history.Commit`).
- **Scheduling.** `internal/history/plan.go` defines, per workflow, whether work is
  pending and when it is next eligible; step selection and the worker's sleep both use it.
- **Original files.** `internal/archive` owns retry timing (`RetryDelay`, `RetryAt`,
  including legacy deadlines) and eligibility (`phoneWorkAllowed`); the store's
  `mergeObserved` is the one rule for files the phone reports again.
- **Sends.** `bot.Engine.Dispatch` runs named steps (`nextReady`, `memoryContext`,
  `generateReply`, `prepareFiles`, `submit`) under the control lock, releasing it only
  for provider generation and file upload. `submit` persists the transaction before the
  single transport call.
- **SQL.** Business-state queries live in `internal/store` as named operations. The
  archive export and restore keep their deliberate snapshot SQL.
- **Dashboard.** `useSession` and `useDashboard` own the session and app-wide polling;
  in a conversation, `useConversationMessages` owns the message list and its one poll
  loop, `useThreadScroll` owns scroll anchoring, and `useComposer` owns drafts and
  one-intent-per-send.

## Compatibility paths

These exist for saved state or clients that predate the current design. Each lists what
would justify retiring it.

| Path | Why it exists | Retire when |
|---|---|---|
| `POST /conversations/{id}/history/import` and `/compose` (legacy, enrollment-based) | Pre-inbox bot history and sends; the legacy compose has no idempotency key, so only a definite rejection is retried | The dashboard still uses them for enrolled conversations while collection is off; retire only with that mode |
| `archive_pending` preservation markers | Protect opted-in history from the legacy expiry pass until a verified archive record exists | Every installation has verified archive records for opted-in conversations |
| Original-file deadlines with `retry_backoff_version` 0 | Failures saved before the daily cooldown adopt it locally, with no phone attempt | No saved file carries a version-0 failure deadline |
| Legacy Google inline previews stored as originals | Imported before originals were separate; repaired once before any download | No saved file is a legacy preview |
| `mark_read_when_viewed` as an optional field | Settings saved before the option existed keep the default (on) | Not retirable without a settings migration |
| Archive policy tokens without effective capacity | Blocks saved before capacity comparison get one bounded attempt | No saved block lacks a token |

## Configuration and state

State lives in a data directory (`smsbot.db`, the encrypted Google session, archived
originals); the master key lives outside it. A state directory is locked while the
service or an offline command uses it. Backups and archive exports are offline
operations described in [BACKUP.md](../BACKUP.md).

## More detail

[DESIGN_NOTES.md](DESIGN_NOTES.md) records the finer design points per subsystem:
processing, history synchronization, attachments, archive module, reconnection,
sync scheduling, storage statistics and instant send. The API contract is
[api/openapi.yaml](../../api/openapi.yaml).


## Reply-linked AI threads

AI ask memory uses encrypted thread and exchange records plus a hashed upstream
answer index, independently of the optional message archive. A root ask has empty
context; a continuation snapshots only its selected parent's ancestry. Jobs still
own send state. The exact prepared answer and transaction association are durable
before submission; upstream echoes register reply targets only by exact chat and
transaction evidence. Receipt echoes do not extend lifetime repeatedly. No inbox
or history scan is used to classify a reply. Reset epochs and live command/identity
checks protect generation and submission. Filtered exports exclude thread tables
and thread context plans; message-history retention remains separate.
