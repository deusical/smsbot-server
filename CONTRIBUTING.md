# Contributing

Read AGENTS.md, README.md and docs/ARCHITECTURE.md. Use focused feature branches and reviewed PRs, squash merging after required checks pass. Do not push or publish merely because a local feature is complete.

Every slice includes implementation, synthetic tests, migration updates and relevant docs. Keep API additions backward compatible and update api/openapi.yaml. Credentials, real participants and real message content never enter source, tests, screenshots or PRs.

Run build.ps1 and the web Playwright suite. On Linux use npm ci/build, copy web/dist into internal/httpapi/assets, go test ./..., go vet ./..., and go build ./cmd/smsbot. Live acceptance needs owner-controlled pairing and a designated conversation, with all competing bots paused.

Report actual checks and unresolved acceptance criteria. Unknown outbound submissions must never enter an automatic retry path.

When Go or npm runtime dependencies change, run `python scripts/gen-third-party-notices.py` (after `npm ci` in `web/`) and commit the updated THIRD_PARTY_NOTICES.md.
