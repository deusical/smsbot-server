# Contributing

SmsBot Server is an experimental personal project. Issues and focused pull requests are
welcome; response times are best effort. Please read the [README](README.md),
[Known limitations](docs/LIMITATIONS.md) and
[architecture](docs/dev/ARCHITECTURE.md) first. Report security problems privately, as
described in [Security](docs/SECURITY.md), not in a public issue.

## Ground rules

- **Never commit real data.** Credentials, cookies, real participants and real message
  content stay out of source, tests, screenshots, logs and PRs. Tests use synthetic
  fixtures only and never contact Google, carriers or AI providers.
- **Keep the safety invariants.** Exact enrolled-participant checks, group exclusion,
  freshness checks, pause, and immutable plans. Persist outbound intent before
  submission, and never retry an uncertain send automatically. Do not log credentials,
  message bodies, contacts or raw upstream errors.
- **Keep the API stable.** Additions must be backward compatible and documented in
  [api/openapi.yaml](api/openapi.yaml).
- **Include the full change.** Implementation, synthetic tests, migrations and the docs
  that describe the behavior.
- **Do not import `golang.org/x/crypto/openpgp`** or its subpackages; the upstream
  advisory has no fixed release.
- Use focused branches and small PRs. They are squash-merged once CI passes.

## Build and test

You need Go 1.27.3 and Node.js 24.15 or newer in the 24.x series.

On Windows:

```powershell
.\scripts\build.ps1          # npm ci, dashboard build, frontend and Go tests, vet, binary
```

On Linux or macOS:

```sh
(cd web && npm ci && npm run build && npm run test:unit)
node scripts/sync-web-assets.mjs    # copy web/dist into the embedded assets
go test ./... && go vet ./... && go build ./cmd/smsbot
```

Browser tests run against an isolated fixture service started from the built binary,
so rebuild the binary first, then from `web/`:

```sh
npx playwright install chromium     # once
npm test
```

Run `gofmt` on Go changes. When Go or npm runtime dependencies change, run
`python scripts/gen-third-party-notices.py` (after `npm ci` in `web/`) and commit the
updated `THIRD_PARTY_NOTICES.md`.

## Frontend tests

From `web/`:

```sh
npm run test:unit                         # one Vitest run; no Go server or browser needed
npm run test:unit:watch                   # rerun affected tests while editing
npm run test:unit -- src/lib/pendingSends.test.ts   # one file
npm run build                             # typecheck application and tests, build dashboard
npm test                                  # Playwright browser suite
```

Put unit and component tests beside the source as `*.test.ts` or `*.test.tsx`. Vitest
uses jsdom, explicit imports from `vitest`, React Testing Library, user-event and
jest-dom matchers. The shared setup unmounts components, restores real timers and blocks
unexpected fetches, so use synthetic responses or module mocks. Keep complete service
flows, scrolling, virtualization, positioning and real-browser focus behavior in
Playwright under `web/tests/`, and backend safety guarantees in Go tests. jsdom does not
verify visual layout. See [web/README.md](web/README.md) for the source layout.

## Live testing

Anything that touches a real Google account needs owner-controlled pairing, a
designated test conversation, and every competing bot paused. Say clearly in your PR
what was and was not verified live; leave unverified acceptance criteria as pending.

## Continuous integration

CI runs build, backend (race tests, vet, gofmt, vulnerability audit) and the full browser
suite. Runs are skipped when a change touches only Markdown, `docs/`, `LICENSE`,
`.gitignore` or `.gitattributes`. See [CI notes](docs/dev/CI_OPTIMIZATION.md).
