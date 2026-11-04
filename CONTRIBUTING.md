# Contributing

SmsBot is an experimental personal project. Issues and focused pull requests are
welcome; responses are best effort. Read the [README](README.md),
[Security and limitations](docs/SECURITY.md) and [Architecture](docs/ARCHITECTURE.md)
first. Report security problems privately, as described in
[Security](docs/SECURITY.md#reporting-a-vulnerability), not in a public issue.

## Ground rules

- **Never commit real data.** Credentials, cookies, real people and real messages stay
  out of code, tests, screenshots, logs and PRs. Tests use synthetic data only and never
  contact Google, carriers or AI providers.
- **Keep the safety rules** listed in [Architecture](docs/ARCHITECTURE.md#safety-rules).
- **Keep the API compatible.** Additions must be backward compatible and described in
  [api/openapi.yaml](api/openapi.yaml).
- **Send the whole change:** code, tests, migrations and any docs it affects.
- **Don't import `golang.org/x/crypto/openpgp`**; its advisory has no fixed release.
- Keep pull requests small and focused. They're squash-merged once CI passes.

## Build and test

On Windows, `.\scripts\build.ps1` installs dependencies, builds the dashboard and the
service, and runs the frontend and Go tests and `go vet`. On Linux or macOS:

```sh
(cd web && npm ci && npm run build && npm run test:unit)
node scripts/sync-web-assets.mjs    # embed the dashboard in the binary
go test ./... && go vet ./... && go build -o bin/smsbot ./cmd/smsbot
```

Browser tests run against an isolated offline service started from `bin/`, so rebuild the
binary first. Then, from `web/`:

```sh
npx playwright install chromium     # once
npm test
```

From `web/`, `npm run test:unit:watch` reruns unit tests while you edit. Put unit tests
next to their source as `*.test.ts(x)`; put full flows, scrolling and layout checks in
Playwright under `web/tests/`; and put backend safety guarantees in Go tests.

Keeping the suites fast:

- Browser tests import from `./fixtures`, not `@playwright/test`. Every page gets
  Playwright's fake clock, running at normal speed, so a test never sleeps through
  the dashboard's 3-second polling: where it waits for the next poll it says so
  with `await nextPoll(page)`. Use `pollUntil(page, check)` when the server may
  still be finishing (a bot reply, an export), and `pollAndSettle(page)` to check
  that a poll did *not* do something. Never use `waitForTimeout` to wait for a poll.
- Sign in with `signIn(page)`; only `mobile.spec.ts` and `routes.spec.ts` use the
  sign-in form, which is what they test.
- Tests leave the fixture server as they found it, so `--repeat-each` works for
  hunting flaky tests. Restore settings with the current revision, and find your
  own results by ID rather than assuming they are the only ones.
- The fixture servers run with `--fixture-speed 10`, which shortens the background
  loops that pace work for a real phone or download worker; the synthetic ones
  need no pacing. Without the flag, fixture mode keeps the production intervals.
- Go tests call `t.Parallel()` first. A test that changes package-level state
  (such as a retry delay) must not, and Go runs it before the parallel ones start.

Run `gofmt` on Go changes. When dependencies change, run
`python scripts/gen-third-party-notices.py` (after `npm ci` in `web/`) and commit the
updated `THIRD_PARTY_NOTICES.md`. `scripts\security-check.ps1` runs `govulncheck` and
`npm audit`.

## Testing with a real account

Use your own paired account, one test conversation, and pause any other bot that
answers there. Say in your PR what you checked on a real phone and what you didn't.

## Continuous integration

CI runs on pull requests: it builds everything and runs the race-enabled Go tests,
`go vet`, `gofmt`, a vulnerability audit and the full browser suite, and a separate job
scans every change for secrets. All of them must pass before a pull request can merge.
The three jobs start together and each builds what it needs. After a merge,
`main.yml` reruns the Go suite on main and saves the caches pull requests restore,
so a pull request reuses the race-test result of every package it leaves unchanged.
Three payload-heavy Go tests skip under `-race` (see `internal/racetest`), so run a
plain `go test ./...` locally. Browser tests use four workers, each with its own
fixture server; set `PW_WORKERS=1` to run them one at a time, and `PW_CHANNEL=chrome`
to use an installed Google Chrome (as CI does).
