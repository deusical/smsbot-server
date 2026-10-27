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

Run `gofmt` on Go changes. When dependencies change, run
`python scripts/gen-third-party-notices.py` (after `npm ci` in `web/`) and commit the
updated `THIRD_PARTY_NOTICES.md`. `scripts\security-check.ps1` runs `govulncheck` and
`npm audit`.

## Testing with a real account

Use your own paired account, one test conversation, and pause any other bot that
answers there. Say in your PR what you checked on a real phone and what you didn't.

## Continuous integration

CI builds everything and runs the race-enabled Go tests, `go vet`, `gofmt`, a
vulnerability audit and the full browser suite. Changes that touch only Markdown or
`docs/` skip CI.
