# Dashboard (developer notes)

The React and TypeScript dashboard is built with Vite and embedded into the Go binary.
For what each page does, see the [user guide](../docs/USER_GUIDE.md).

## Layout

| Path | Purpose |
|---|---|
| `src/main.tsx` | App shell: navigation, layout, theme, save bar; composes the hooks below |
| `src/pages/` | `Home`, `Conversations`, `ConversationHistory` (list and thread), `ArchivePage`, `Commands`, `Settings`, `Activity` |
| `src/components/` | Shared pieces: `ui`, `Icon`, `Confirmation`, `Attachments`, `MessageActions`, `MessageBubble` (saved and pending bubbles), `Composer`, `Generation`, and the archive widgets `HistorySync`, `ArchiveSettings`, `CaptureChecks` |
| `src/lib/` | No-UI code: `useSession` (sign-in, expiry, session generation), `useDashboard` (state, activity, settings and conversation-list polling), `useConversationMessages` (one thread's messages, pagination, change feed, imports and its single poll loop), `useThreadScroll` (the one owner of scroll anchoring), `useReadReceipts`, `useComposer` (drafts and one-intent-per-send), `api` (typed `fetch` wrapper and API types), `connection` (phone status labels), `workspace` (shared draft state), `conversationOrder`, `pendingSends`, `pendingActions`, `typing`, `useViewedHistoryRefresh` |
| `src/styles/` | Stylesheet split by area; `index.css` imports the parts in cascade order, so keep that order |
| `src/test/` | Vitest setup; unit tests sit next to their source files |
| `tests/` | Playwright browser specs |

## Conventions

- No browser storage: theme, drafts, credentials, seen state and pending messages stay
  in memory; the session is an HttpOnly cookie. A test asserts `localStorage` is empty.
- Polling never overlaps, pauses while the tab is hidden, and discards results from a
  previous conversation or search. Enable/Pause applies the mutation response
  immediately so older polls cannot overwrite it.
- The conversation list is refetched when `conversations_revision` in state changes,
  when the tab becomes visible again, and to complete a stale or partial page; there
  is no timed refresh.
- Commands, profiles, settings and inbox preferences share one draft that survives page
  changes; a save bar commits it.
- The service worker caches only static files, never `/api/` responses.

## Develop and test

```sh
npm ci
npm run build        # tsc --noEmit and Vite build
npm test             # Playwright against an isolated fixture service
```

Rebuild the Go binary with the new assets first (`../scripts/build.ps1 -SkipInstall`, or
`node ../scripts/sync-web-assets.mjs` and `go build ./cmd/smsbot` from the repository
root), because Playwright starts the embedded fixture server from `../bin`. Tests use
synthetic data only and never contact Google, carriers or AI providers. Set
`DOCS_SCREENSHOTS=1` to regenerate the documentation screenshots.
