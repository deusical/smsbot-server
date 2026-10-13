# Web workspace passes

## Pass 1: workspace and daily workflows

The first pass used the existing API. It adds a dark workspace with a transient light theme, responsive navigation, connection overview, searchable conversations, an activity review queue, disabled command templates and duplication, catalog search, protected configuration drafts, and recipient/text confirmation for manual compose. Synthetic fixtures cover the interface; live transport acceptance remains separate.

## Pass 2: visual and workflow revamp

A new visual system: aubergine/mint dark theme and lilac/teal light theme, with the system preference as the default. Commands appear as mono chips throughout. Home leads with live/paused status, a 24-hour request chart and inline approvals. Conversations uses a messenger layout with enrollment switches, search as you type and Enter-to-send compose with no confirmation dialog. Commands becomes a list/editor studio with an inline offline preview. Settings is split into sections. Drafts now survive navigation behind one save bar instead of a leave-page dialog. The Activity feed shows plain-language states with inline approve/cancel. The API is unchanged; uncertain sends still never retry, and idempotent compose keys are reused when the same text is sent again.

## Subsequent passes

These are proposed scopes, not implemented features or release commitments. Each can use a focused branch/worktree and its own current-HEAD review gate.

1. **Command studio:** separate library and detail editing, reusable profile browsing, clearer inherited generation settings, keyboard navigation, and a richer offline playground. Keep disabled imports, explicit saving, per-model preferences and immutable queued settings.
2. **Conversation workspace:** improve history navigation and conversation-specific activity. Richer inbox pagination, read markers, attachment support or provider-history completeness need explicit API and storage design first. Preserve exact enrollment, group exclusion and no history replay.
3. **Usage and operations:** add paginated activity and historical usage reporting before presenting full-period totals or charts. Separate provider-reported cost, unknown usage, configured thresholds and transport evidence. Authentication diagnostics must remain sanitized and never expose cookies or message bodies.

## Review and acceptance

Build and browser tests run against isolated synthetic fixtures. Screenshots contain synthetic data only. Changes to API behavior require server tests and compatibility review with the native client. Independent Gemini review must target the exact PR HEAD; failed or stale reviews do not satisfy the merge gate. Live Google/provider tests remain explicitly authorized owner acceptance.
