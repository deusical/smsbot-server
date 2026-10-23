import { describe, expect, it } from "vitest";
import type { Conversation } from "./api";
import { sortConversations } from "./conversationOrder";

function conversation(id: string, extra: Partial<Conversation> = {}): Conversation {
  return {
    id, participant_id: `peer-${id}`, label: id, protocol: "RCS",
    group: false, writable: true, enrolled: false, ...extra,
  };
}

describe("conversation ordering", () => {
  it("keeps pinned conversations first, then sorts each group newest first", () => {
    const input = [
      conversation("older", { last_message_at: "2026-01-01T00:00:00Z" }),
      conversation("pinned-old", { pinned: true, last_message_at: "2025-01-01T00:00:00Z" }),
      conversation("newer", { last_message_at: "2026-02-01T00:00:00Z" }),
      conversation("pinned-new", { pinned: true, last_message_at: "2025-02-01T00:00:00Z" }),
    ];
    const original = [...input];
    expect(sortConversations(input).map(c => c.id)).toEqual([
      "pinned-new", "pinned-old", "newer", "older",
    ]);
    expect(input).toEqual(original);
  });

  it("preserves server order for ties and puts missing or invalid dates last", () => {
    const input = [
      conversation("missing"),
      conversation("first", { last_message_at: "2026-01-01T00:00:00Z" }),
      conversation("invalid", { last_message_at: "invalid" }),
      conversation("second", { last_message_at: "2026-01-01T00:00:00Z" }),
    ];
    expect(sortConversations(input).map(c => c.id)).toEqual([
      "first", "second", "missing", "invalid",
    ]);
  });
});
