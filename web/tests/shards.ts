// CI splits the browser suite by measured duration rather than test count.
// Shards 1 and 2 run the files pinned here; shard 3 (and a local run with no
// SMSBOT_SHARD) takes every other file, so a new spec cannot go unrun.
const pinned: Record<string, string[]> = {
  "1": ["inbox", "archive-panel", "recovery"],
  "2": ["dashboard", "workspace", "send-flow", "conversation-list"],
};
const ignored = Object.values(pinned).flat();
const match = (names: string[]) => names.map((n) => `**/${n}.spec.ts`);

export function shardSelection(shard: string | undefined) {
  if (!shard) return {};
  return pinned[shard]
    ? { testMatch: match(pinned[shard]) }
    : { testIgnore: match(ignored) };
}
