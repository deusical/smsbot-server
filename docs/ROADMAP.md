# Implementation status

Architecture accepted: Go server + native Kotlin/Compose Android + React/TypeScript dashboard. See [completion plan](COMPLETION_PLAN.md) for the ordered remaining work and acceptance gates.

| Phase | Status | Remaining gate |
|---|---|---|
| 0 Transport reliability | Service pairing/startup verified; owner reports live !ping success after cookie recovery | Reply-count/delivery detail, locked phone/open Messages, SMS/RCS, outages, competing clients and authentication observation |
| 1 Foundation | Local repository, encrypted state, authentication, API, fixture adapter, build/CI | Independent review, personal deployment and live release acceptance |
| 2 Messaging | Built-in/custom fixed/AI commands, approvals/cancellation, persistent jobs, uncertainty and correlated statuses | Remaining live phone/provider acceptance and sustained Google recovery |
| 3 Clients | Web dashboard and native Android server connection/control/inbox; native HTTP lifecycle tests | Owner private HTTPS/phone setup and live end-to-end acceptance |
| 4 Bot parity | Implemented: custom/AI commands, catalog/profiles/generation, memory/reset, limits, approvals, quiet hours | Live provider acceptance if authorized; transport-specific differences documented in PARITY.md |
| 5 Inbox/migration | Implemented: retained-text pages/search/retention, explicit phone text import, manual compose, server/Android recipes, encrypted offline backup/restore | No complete-history/attachment or cross-platform key-recovery claim |
| 6 Self-hosted release | Private-origin/direct TLS and container/offline recovery implemented and rehearsed | Personal network/cert setup, independent review, longer live acceptance and 72-hour stable-release observation |

Owner-reported ping success does not establish sustained authentication, complete delivery coverage or production reliability. All normal automated checks remain offline. The next human checkpoints are controlled restart/browser-handoff observation and the remaining designated live acceptance cases. PARITY.md separates implemented capability from the remaining human acceptance gates. Observe at least 24 hours before claiming continuous operation and 72 before a stable release; duration alone does not prove every failure case.
