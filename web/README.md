# Dashboard

The dashboard uses the same authenticated API as the native Android server client. A dark workspace is the default, with an in-memory light-theme toggle and responsive desktop/mobile navigation. Theme choices, drafts, credentials and keys are never saved in browser storage. Owner sessions stay in HttpOnly cookies.

## Controls

- Commands edits fixed replies, AI instructions, model profiles, optional memory, approval requirements, enrolled-conversation restrictions and command generation overrides. Save configuration before using the offline playground. Built-in commands remain server-owned.
- Command templates and duplication create disabled drafts; AI templates require approval and leave memory off. Search and type filters help browse the library. Model selectors search the catalog while retaining the current selection, including saved models absent from the catalog.
- Configuration drafts survive background refreshes. Navigation asks before discarding unsaved changes; a save bar offers explicit save/discard actions and browser-close protection. Recipe import confirms replacement when a draft is unsaved. Drafts are not persisted across browser sessions.
- Settings manages the default model, favorites, per-model generation preferences, reply limits, daily provider budget, quiet hours/time zone, opt-in memory duration and retained-history duration. Model default reasoning leaves the provider's legacy default behavior intact. Supported reasoning efforts and temperature controls come from the cached model catalog. Refresh model catalog explicitly contacts OpenRouter.
- The provider key is an owner-only write; the dashboard receives only whether a key is configured. AI input and opted-in memory are shared with OpenRouter. Playground previews are synthetic and never make provider requests or submit messages.
- Conversations shows encrypted, retained server history with search and older-page loading. It does not promise a complete Google Messages history. Manual compose requires an enrolled writable conversation, an enabled bot and a ready phone; queued status does not prove delivery.
- Conversation search and All/Enrolled/Available filters leave enrollment unchanged. Manual compose confirms the recipient and exact text before making the queue request. Canceling the confirmation keeps the text. Phone-history import is under History import options.
- Activity exposes approval and cancellation for eligible jobs. Approval errors, including expiration, are displayed without treating the request as submitted. Unknown submissions have no retry action.
- Activity filters approvals, in-progress, completed and attention states and searches commands/conversation labels. Metrics describe loaded recent requests only; known reported costs are summed and requests with unavailable costs are identified separately. These are not complete history or billing totals.
- Recipe export omits sender identities, conversation restrictions and keys. Import validates on the server and leaves every imported command disabled. Review imported settings before enabling commands.

Polling does not overlap. Enable/Pause applies the mutation response immediately; older state polls cannot overwrite that response. Conversation history requests guard against results from a previous conversation or search.

## Validation

Run `npm run build` for TypeScript and production asset generation. Run `npm test` after rebuilding the server's embedded assets and fixture binary. Playwright starts an isolated fixture server with synthetic credentials and conversations. Feature form tests mock API responses; they make no Google, OpenRouter or carrier requests. The existing fixture journey also validates real offline command routing and client revocation.
