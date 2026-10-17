# Dashboard (developer notes)

The React and TypeScript dashboard is built with Vite and embedded into the Go binary.
For what each page does, see the [user guide](../docs/USER_GUIDE.md).

## Layout

| File | Purpose |
|---|---|
| `src/main.tsx` | App shell: session, polling lanes, page routing |
| `src/api.ts` | Typed `fetch` wrapper and API types |
| `src/Conversations.tsx`, `ConversationHistory.tsx` | Conversation list and thread |
| `src/conversationOrder.ts` | Ordering, relative times, in-memory seen state |
| `src/pendingSends.ts` | In-memory pending messages and the job follower |
| `src/typing.ts` | Typing indicator polling |
| `src/ArchivePage.tsx`, `HistorySync.tsx`, `ArchiveSettings.tsx`, `CaptureChecks.tsx` | Archive page |
| `src/Commands.tsx`, `Generation.tsx` | Command studio |
| `src/Settings.tsx`, `Activity.tsx`, `workspace.ts` | Settings, activity feed, shared draft state |
| `src/ui.tsx`, `Icon.tsx`, `style.css` | Shared components, icons and styles |

## Conventions

- No browser storage: theme, drafts, credentials, seen state and pending messages stay
  in memory; the session is an HttpOnly cookie. A test asserts `localStorage` is empty.
- Polling never overlaps, pauses while the tab is hidden, and discards results from a
  previous conversation or search. Enable/Pause applies the mutation response
  immediately so older polls cannot overwrite it.
- The conversation list is refetched only when `conversations_revision` in state
  changes (30-second fallback).
- Commands, profiles, settings and inbox preferences share one draft that survives page
  changes; a save bar commits it.
- The service worker caches only static files, never `/api/` responses.

## Develop and test

```sh
npm ci
npm run build        # tsc --noEmit and Vite build
npm test             # Playwright against an isolated fixture service
```

Rebuild the Go binary with the new assets first (`../build.ps1 -SkipInstall`, or
`node ../scripts/sync-web-assets.mjs` and `go build ./cmd/smsbot` from the repository
root), because Playwright starts the embedded fixture server from `../bin`. Tests use
synthetic data only and never contact Google, carriers or AI providers. Set
`DOCS_SCREENSHOTS=1` to regenerate the documentation screenshots.
