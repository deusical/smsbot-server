# Dashboard

The dashboard uses the same authenticated API as the native Android server client. It follows the system theme and adapts to phone and desktop widths. Owner sessions stay in HttpOnly cookies; credentials and keys are never saved in browser storage.

## Controls

- Commands edits fixed replies, AI instructions, model profiles, optional memory, approval requirements, enrolled-conversation restrictions and command generation overrides. Save configuration before using the offline playground. Built-in commands remain server-owned.
- Settings manages the default model, favorites, per-model generation preferences, reply limits, daily provider budget, quiet hours/time zone, opt-in memory duration and retained-history duration. Model default reasoning leaves the provider's legacy default behavior intact. Supported reasoning efforts and temperature controls come from the cached model catalog. Refresh model catalog explicitly contacts OpenRouter.
- The provider key is an owner-only write; the dashboard receives only whether a key is configured. AI input and opted-in memory are shared with OpenRouter. Playground previews are synthetic and never make provider requests or submit messages.
- Conversations shows encrypted, retained server history with search and older-page loading. It does not promise a complete Google Messages history. Manual compose requires an enrolled writable conversation, an enabled bot and a ready phone; queued status does not prove delivery.
- Activity exposes approval and cancellation for eligible jobs. Approval errors, including expiration, are displayed without treating the request as submitted. Unknown submissions have no retry action.
- Recipe export omits sender identities, conversation restrictions and keys. Import validates on the server and leaves every imported command disabled. Review imported settings before enabling commands.

Polling does not overlap. Enable/Pause applies the mutation response immediately; older state polls cannot overwrite that response. Conversation history requests guard against results from a previous conversation or search.

## Validation

Run `npm run build` for TypeScript and production asset generation. Run `npm test` after rebuilding the server's embedded assets and fixture binary. Playwright starts an isolated fixture server with synthetic credentials and conversations. Feature form tests mock API responses; they make no Google, OpenRouter or carrier requests. The existing fixture journey also validates real offline command routing and client revocation.
