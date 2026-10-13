# Web workspace passes

## Pass 1: workspace and daily workflows

The current pass uses the existing API. It adds a dark workspace with a transient light theme, responsive navigation, connection overview, searchable conversations, an activity review queue, disabled command templates and duplication, catalog search, protected configuration drafts, and recipient/text confirmation for manual compose. Synthetic fixtures cover the interface; live transport acceptance remains separate.

## Subsequent passes

These are proposed scopes, not implemented features or release commitments. Each can use a focused branch/worktree and its own current-HEAD review gate.

1. **Command studio:** separate library and detail editing, reusable profile browsing, clearer inherited generation settings, keyboard navigation, and a richer offline playground. Keep disabled imports, explicit saving, per-model preferences and immutable queued settings.
2. **Conversation workspace:** improve history navigation and conversation-specific activity. Richer inbox pagination, read markers, attachment support or provider-history completeness need explicit API and storage design first. Preserve exact enrollment, group exclusion and no history replay.
3. **Usage and operations:** add paginated activity and historical usage reporting before presenting full-period totals or charts. Separate provider-reported cost, unknown usage, configured thresholds and transport evidence. Authentication diagnostics must remain sanitized and never expose cookies or message bodies.

## Review and acceptance

Build and browser tests run against isolated synthetic fixtures. Screenshots contain synthetic data only. Changes to API behavior require server tests and compatibility review with the native client. Independent Gemini review must target the exact PR HEAD; failed or stale reviews do not satisfy the merge gate. Live Google/provider tests remain explicitly authorized owner acceptance.
