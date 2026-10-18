# Contributing

Read AGENTS.md, README.md and docs/ARCHITECTURE.md. Use focused feature branches and reviewed PRs, squash merging after required checks pass. Do not push or publish merely because a local feature is complete.

Every slice includes implementation, synthetic tests, migration updates and relevant docs. Keep API additions backward compatible and update api/openapi.yaml. Credentials, real participants and real message content never enter source, tests, screenshots or PRs.

Run build.ps1 and the web Playwright suite. On Linux use npm ci/build, copy web/dist into internal/httpapi/assets, go test ./..., go vet ./..., and go build ./cmd/smsbot. Live acceptance needs owner-controlled pairing and a designated conversation, with all competing bots paused.

## Frontend tests

Use Node.js 24.15 or newer in the 24.x series (required by the jsdom test
environment). From `web/`, run `npm ci` once after checkout or a dependency change, then:

```sh
npm run test:unit                         # one Vitest run; no Go server or browser install
npm run test:unit:watch                   # rerun affected tests while editing
npm run test:unit -- src/pendingSends.test.ts  # one file
npm run build                            # typecheck application and tests, build dashboard
npm test                                 # existing Playwright browser suite
```

Put unit and component tests beside the source as `src/**/*.test.ts` or
`src/**/*.test.tsx`. Vitest uses jsdom, explicit imports from `vitest`, React Testing
Library, user-event and jest-dom matchers. The shared setup unmounts components and
restores real timers after each test, and blocks unexpected fetches. Use synthetic
responses or module mocks for API-dependent behavior; reset module-owned state in
the test file. No owner credential or running service is needed for these tests.

Test frontend decisions and state transitions here, plus component interactions
that do not depend on layout. Keep complete service flows, scrolling,
virtualization, positioning and real-browser focus behavior in Playwright under
`web/tests/`. Keep backend safety guarantees in Go tests. Existing Playwright
coverage remains in place; avoid duplicating it unless the tests prove distinct
behavior at different layers. jsdom does not verify visual layout.

`build.ps1` runs frontend unit tests unless `-SkipChecks` or `-SkipWeb` is supplied.
On Linux, also run `npm run test:unit` during verification. CI requires the unit
suite in its build job before running backend and browser jobs. `npm test` retains
its existing Playwright meaning.

Report actual checks and unresolved acceptance criteria. Unknown outbound submissions must never enter an automatic retry path.

When Go or npm runtime dependencies change, run `python scripts/gen-third-party-notices.py` (after `npm ci` in `web/`) and commit the updated THIRD_PARTY_NOTICES.md.
