# Architecture

Selected stack: Go service, SQLite/local WAL, libgm adapter, React/TypeScript web dashboard, existing Kotlin/Compose Android client. The service owns messaging and bot state; clients use API v1 and can be closed without stopping the bot.

## Boundaries

- `transport`: normalized conversation/incoming contracts, physically separate fixture implementation, opt-in Google adapter. Google cookies/session stay here and in encrypted storage.
- `store`: schema migrations, unique incoming jobs, enrollment, encrypted plans/messages, hashed credentials. SQLite connections are bounded; no network filesystem.
- `bot`: fresh incoming filtering, exact trust, immutable fixed plans, serialized submission, pause, pre-send rechecks and receipt reconciliation.
- `httpapi`: exact Host/origin checks, bounded strict JSON, owner cookies, revocable client bearer credentials, static embedded dashboard.
- `web`: same-origin API, memory-only credentials, system theme, responsive navigation, snapshots polled every three seconds without overlapping refreshes. Conversation refresh errors are shown separately from server connection failures. SSE and generated API clients are future improvements; polling is explicit in this milestone.
- Google conversation refresh checks up to four independent full-detail requests concurrently, preserving inbox order and publishing the identity cache only after all checks succeed. Exact participant and pre-send identity checks remain required.
- Dashboard Enable/Pause actions apply the state returned by the pause endpoint immediately, independently of conversation refresh. A revision check prevents state polls started before or during the action from overwriting the confirmed response. Failed mutations retain the previously displayed state and show the error.
- Android: API-backed server screen, Keystore-encrypted connection, dispatch guards. Its existing local engine remains independent and unavailable in server mode.

## Processing

Fresh complete incoming text → exact enrolled individual participant → pause/readiness checks → built-in/custom command routing → encrypted immutable plan and unique persisted job → current enrollment/freshness/identity checks → persisted transaction ID and submitting state → transport call → submitted or unknown_submission.

Pause/unenrollment cancel queued work. Startup recovery marks submitting jobs uncertain, then starts paused and cancels queued work. No automatic retries after invoking a transport. Correlated Google status events match both conversation and transaction ID; statuses do not downgrade. Displayed means Google reports displayed, not proof a human consciously read the text.

History fetching does not feed routing. The initial inbox retains only accepted command text and replies. Explicit phone text-history import stores available pages without routing commands; retained history has bounded decrypted search and stable cursors. Provider work and retention are implemented. Booting a fixture environment cannot construct the Google adapter. Use separate state directories for fixture/live environments.

## Authentication

Owner initialization writes a random credential to a local owner file; the database stores only its hash. Login creates a 12-hour random browser session with HttpOnly/SameSite Strict cookies. State-changing browser requests require exact Origin. Login uses strict JSON plus bounded failed-attempt cooldown. Exact Host validation prevents DNS rebinding. API responses are no-store; CSP blocks remote scripts and framing.

Owners create revocable random Android credentials. Client tokens cannot create/revoke credentials. Invalid bearer credentials never fall back to a browser cookie. Hosting defaults to loopback. Nonloopback binds require direct TLS and an exact HTTPS origin. A configured private HTTPS proxy can front loopback; forwarded headers never determine trust or origin.

## Cross-repository workflow

Service source is a separate repository. Android retains its existing repository/license. API additions precede native client changes; the client verifies api_version before activating server mode. Entering server mode disables local dispatch under the Android dispatch mutex and cancels pending work. Returning requires confirmed server pause; local remains paused. An unreachable server does not authorize fallback.

## Configuration, generation and recovery

Configuration/provider keys and queued context are encrypted with separate scopes. Schema v2 adds an encrypted scoped memory table and a usage ledger, preserving v1 data and normalizing message timestamps for stable pagination. A serialized dispatcher releases its control mutex during provider generation, allowing pause/cancellation; it rechecks current enrollment/identity, command availability, approval, quiet hours and age before submission. Memory epochs invalidate old context on reset/revocation.

The fixture provider has no HTTP client. Explicit catalog refresh contacts OpenRouter only in Google mode. Generation uses a single requested model with provider parameter enforcement; usage/finish notices contain no reasoning text. The reported-spend threshold fails closed on unknown usage. Manual compose uses the same queue/submission protections.

Backup/restore runs under the offline state lock, excludes credentials/provider/pairing/memory, retains usage and deduplication evidence, and restores all eligible work paused/cancelled or uncertain. Authentication diagnostics contain event identifiers, times and numeric status/category only. Successful token-refresh events save immediately; save failure pauses automation. The running observation service must be cleanly restarted by its owner to load a new binary.
