# Dashboard

The dashboard uses the same authenticated API as the native Android server client. It follows the system light/dark preference, with an in-memory toggle in the top bar, and uses a bottom tab bar on phones. Theme choices, drafts, credentials and keys are never saved in browser storage. Owner sessions stay in HttpOnly cookies.

## Pages

- **Top bar:** a **Live/Paused** switch (`role="switch"`, "Bot sending") pauses or resumes all sending. Pausing cancels queued and waiting replies. A "Demo data" tag marks the offline fixture.
- **Home:** current status in plain words, a 24-hour requests chart built from the loaded activity window, a metric strip (needs approval, uncertain sends, people with bot replies on, enabled commands), recent requests with inline **Approve**/**Cancel**, and quick links to people and commands.
- **Conversations:** a messenger layout. Each row has a **Bot replies** switch (enrollment). Groups are shown but can never be enabled. The thread view searches as you type, imports phone history from the header (command-only mode), and loads older pages from a floating button or, with the full inbox, by scrolling up. Compose sends on Enter (Shift+Enter for a new line) without a confirmation step. Pausing shows an inline **Resume sending** action. When a send isn't acknowledged, the text stays and sending again reuses the same idempotency key (full inbox path).
- **Commands:** a studio with a searchable list (each with an enable switch, plus server built-ins) and an editor: name, description, fixed text vs AI, approval, memory, model/profile, optional generation override, allowed conversations and an offline **Try it** preview of the saved command. **New** offers Blank, Quick reply, AI assistant and Translator templates. Templates and duplicates start disabled; AI templates require approval and leave memory off. Model profiles have their own tab.
- **Activity:** filters (all, needs approval, in progress, sent, problems) and search. Rows expand for model, usage and cost. Approve/cancel act inline. Uncertain submissions explain that they are not retried and have no retry action. Metrics describe loaded recent requests only; requests with unavailable cost are counted separately. These are not billing totals.
- **Settings:** sections for AI models (default model, star favorites inside the picker, per-model tuning, catalog refresh), Providers (OpenRouter key, OpenAI-compatible URL/key; keys are write-only), Limits & quiet hours, Messages & privacy (full inbox, older history, automatic replies, retention, memory window), Devices (connection details, add/revoke Android credentials) and Import & export (recipes). Sign out is at the end of the section list.

## Drafts and saving

Commands, profiles, settings and inbox preferences share one draft that survives page changes. A floating save bar appears only when something differs from the saved state; **Save changes** (or Ctrl/Cmd+S) commits configuration and inbox preferences, **Discard** restores the last saved state, and closing the tab with a draft asks first. Obvious mistakes, such as invalid command names, are explained in the save bar before any request. Opening Commands or Settings without a draft reloads the saved configuration. A stale inbox revision shows the server's conflict error; **Discard** loads the latest revision. Importing a recipe over unsaved edits asks first. Provider keys and device credentials save immediately from their own forms.

Polling does not overlap. Enable/Pause applies the mutation response immediately; older state polls cannot overwrite that response. Conversation history requests guard against results from a previous conversation or search.

## Validation

Run `npm run build` for TypeScript and production asset generation. Run `npm test` after rebuilding the server's embedded assets and fixture binary. Playwright starts an isolated fixture server with synthetic credentials and conversations. Feature form tests mock API responses; they make no Google, OpenRouter or carrier requests. The existing fixture journey also validates real offline command routing and client revocation.
