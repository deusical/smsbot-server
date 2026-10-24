import { act, renderHook } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Conversation, Message } from "./api";
import { clearPending, getPending, usePending } from "./pendingSends";
import { useComposer } from "./useComposer";
import { useConversationMessages } from "./useConversationMessages";
import { useThreadScroll } from "./useThreadScroll";

const conversation: Conversation = {
  id: "c1", participant_id: "p1", label: "Synthetic", protocol: "RCS", group: false, writable: true, enrolled: false,
};
const json = (body: unknown) => new Response(JSON.stringify(body), { status: 200, headers: { "Content-Type": "application/json" } });

describe("useComposer", () => {
  beforeEach(() => vi.stubGlobal("fetch", vi.fn(async () => json({ id: "job" }))));
  afterEach(() => clearPending());

  it("turns a rapid double send into exactly one intent", async () => {
    const textarea = { current: null };
    const { result } = renderHook(() => ({
      composer: useComposer({ conversation, inbox: { enabled: true } as never, canSend: true, textarea, readInbox: async () => ({ enabled: true, scope: "all" }) as never }),
      pending: usePending(conversation.id),
    }));
    act(() => result.current.composer.setText("Synthetic hello"));
    act(() => {
      result.current.composer.send();
      result.current.composer.send();
    });
    expect(result.current.pending).toHaveLength(1);
    const key = result.current.pending[0].key;
    // A retry keeps the same key, so the server deduplicates it.
    await act(async () => { await Promise.resolve(); });
    expect(getPending(conversation.id, key)?.key).toBe(key);
  });
});

describe("useConversationMessages", () => {
  let releases: Array<(m: Message[]) => void>;
  beforeEach(() => {
    releases = [];
    vi.stubGlobal("fetch", vi.fn((url: string) => {
      if (!url.includes("/history?")) return Promise.resolve(json({}));
      return new Promise<Response>((resolve) => releases.push((messages) => resolve(json({ messages, next_cursor: "" }))));
    }));
  });

  it("drops a response for a search that has been replaced", async () => {
    const { result, rerender } = renderHook(({ query }) => {
      const viewport = useThreadScroll({ current: null });
      return useConversationMessages({ conversationId: "c1", base: "/inbox/conversations/c1", query, changeFeed: false, loadOlderFromPhone: false, viewport });
    }, { initialProps: { query: "first" } });
    rerender({ query: "second" });
    await act(async () => { await Promise.resolve(); });
    const msg = (id: string): Message => ({ id, text: id, direction: "incoming", at: "2026-01-01T00:00:00Z" });
    // The newer search answers first; the older one arrives late.
    await act(async () => { releases[1]([msg("for-second")]); await Promise.resolve(); });
    await act(async () => { releases[0]([msg("for-first")]); await Promise.resolve(); });
    expect(result.current.messages.map((m) => m.id)).toEqual(["for-second"]);
  });
});
