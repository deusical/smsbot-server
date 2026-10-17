# Changelog

Notable changes. Versions before 0.1.0 were internal milestones.

## 0.1.0-beta (unreleased)

First public beta. Experimental; see [Known limitations](docs/LIMITATIONS.md).

### Added
- Protocol-aware message action menus, RCS quoted replies, Unicode reaction
  add/change/remove, bounded quote navigation, and durable reaction reconciliation.
  Outbound editing remains pending companion-transport support.
- Messages-style dashboard: conversations ordered by recency with pinned and unread
  markers, relative times, last-message previews, typing indicators, jump to latest,
  keyboard shortcuts (Ctrl/Cmd+K, Alt+Up/Down), paste or drop images, themed
  scrollbars, and installable phone app.
- Instant send with Sending, Sent, Delivered and Read status; safe retry using
  per-message idempotency keys; edit and resend after a definite failure.
- Read marking while you view an unread conversation (on by default, setting in
  Messages & privacy) and live list refresh driven by phone activity.
- Archive page: overview, per-conversation archive and re-scan, automatic history
  repair, retention, original-file archive and exports.
- `smsbot --version`; gofmt check in CI.

### Added (operations)
- `scripts/deploy-remote.ps1` and `scripts/remote-install.py`: checksum-verified,
  rollback-capable deployment of merged `main` to a remote Linux host over SSH.
  Documented in [Hosting](docs/HOSTING.md).

### Changed
- Conversation polish: the message menu opens beside the message (never over it) and
  drops its title bar, reactions become a compact pill on the bubble's edge, messages
  from one sender group into runs, quoted replies are easier to read, and on phones a
  tap opens the menu with the message scrolled clear of the sheet. The phone
  navigation shows "Chats" so the label is no longer cut off.
- Visible message actions are prefetched in memory so RCS menus open with the
  correct actions immediately. Early clicks show loading or retry instead of a
  partial menu.
- The API accepts owner browser sessions only.
- Documentation rewritten: README, getting started, user guide, architecture, security
  and known limitations. Detailed design notes moved to `docs/DESIGN_NOTES.md`.

### Removed
- The Android client and everything that existed only for it: bearer client
  credentials and `/clients`, the legacy `GET /conversations` list and
  `GET /conversations/{id}/messages`, and Android command-pack import. Saved client
  credentials are deleted on startup.
- The unused `Configuration.tsx` screen.
