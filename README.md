# SmsBot Server

A self-hosted command bot and message archive for your own Google Messages account.
It runs as one small service, shows your conversations in a web dashboard that also
installs as a phone app, and can answer commands you define (fixed text or AI) in the
conversations you choose.

![Dashboard home](docs/images/home.png)

> **Experimental, use at your own risk.** SmsBot talks to Google Messages through an
> unofficial, reverse-engineered client ([libgm](https://github.com/mautrix/gmessages/tree/main/pkg/libgm)).
> Google can change or block that at any time, and using it may conflict with Google's
> terms. It stores broad Google session cookies. Read [Security](docs/SECURITY.md) and
> [Known limitations](docs/LIMITATIONS.md) before you pair a real account. Pair only an
> account you own, and test with a designated conversation first.

## What it does

- **Messages dashboard.** A Messages-style view of your individual conversations:
  newest first, pinned and unread markers, last-message previews, typing indicators,
  instant send with Sending / Sent / Delivered / Read status, paste or drop images,
  search, and keyboard shortcuts. Works on desktop and as an installable phone app.
- **Local archive.** An encrypted local copy of the conversations you enable, kept
  up to date in the background, with original files, edit history, exports and offline
  backup and restore.
- **Command bot.** Built-in commands plus your own fixed or AI commands, with
  approvals, memory, quiet hours, rate and spend limits, and an offline playground.
  AI uses OpenRouter or any OpenAI-compatible endpoint you configure.
- **Careful by design.** Exact participant enrollment, groups excluded, history never
  runs commands, every start is paused, and an uncertain send is never retried
  automatically. See [Architecture](docs/dev/ARCHITECTURE.md).

![Conversations](docs/images/conversations.png)

## Quick start (offline demo)

You need Go 1.27.3 and Node.js 24.15 or newer in the 24.x series. On Windows:

```powershell
.\scripts\build.ps1  # builds the dashboard and the service, runs the checks
.\scripts\start.ps1  # offline demo with synthetic conversations, starts paused
```

Open <http://127.0.0.1:8787>, read the owner credential file from the path the
launcher prints, and paste it into the sign-in page. Turn on **Bot replies** for the demo
conversation, flip the top-bar switch to **Live**, open the conversation and use
**Simulate !ping**. The demo never contacts Google or an AI provider.

On Linux or Docker see [Getting started](docs/GETTING_STARTED.md) and
[Hosting](docs/HOSTING.md).

## Using your own account

[Getting started](docs/GETTING_STARTED.md) walks through pairing a real Google
Messages account (cookie and emoji approval, done locally), phone access over a private
HTTPS address such as Tailscale, and updating cookies without pairing again.

## Documentation

| Guide | Contents |
|---|---|
| [Getting started](docs/GETTING_STARTED.md) | Build, demo, pairing, cookie updates, troubleshooting |
| [User guide](docs/USER_GUIDE.md) | Every page and setting, commands, archive, shortcuts |
| [Hosting](docs/HOSTING.md) | Always-on host, private HTTPS, Docker, direct TLS |
| [Backup and recovery](docs/BACKUP.md) | Offline backup and restore, archive export |
| [Security](docs/SECURITY.md) | What is stored, threat model, reporting problems |
| [Known limitations](docs/LIMITATIONS.md) | What is unverified or unsupported |
| [Architecture](docs/dev/ARCHITECTURE.md) | Components, flows and safety rules |
| [Design notes](docs/dev/DESIGN_NOTES.md) | Detailed per-subsystem design |
| [API](api/openapi.yaml) | OpenAPI contract for the owner API |
| [Changelog](CHANGELOG.md) | Release notes |
| [Contributing](CONTRIBUTING.md) | Development workflow and checks |
| [Third-party notices](THIRD_PARTY_NOTICES.md) | Licenses of bundled dependencies |

## Develop and test

```powershell
.\scripts\build.ps1               # install, build, frontend/Go tests and vet
.\scripts\security-check.ps1      # Go and npm vulnerability scans
cd web; npm run test:unit         # fast frontend tests; no fixture server needed
npx playwright install chromium; npm test  # from web/: browser tests
```

Use `npm run test:unit:watch` from `web/` for frontend test feedback while editing.
See [Contributing](CONTRIBUTING.md#frontend-tests) for test placement and focused runs.
Playwright starts its own isolated fixture service with synthetic data and never
contacts Google, carriers or AI providers. Pass `-SkipInstall -SkipChecks` to
`scripts\build.ps1` for a quick rebuild. `smsbot --version` prints the build version.

## License

AGPL-3.0-or-later, see [LICENSE](LICENSE). The Google adapter uses
[mautrix/gmessages libgm](https://github.com/mautrix/gmessages/tree/main/pkg/libgm);
its license and the notices for every other bundled dependency are in
[THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md) and must be preserved.
