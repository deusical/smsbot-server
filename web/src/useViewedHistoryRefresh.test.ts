import { act, renderHook } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { useViewedHistoryRefresh } from "./useViewedHistoryRefresh";

describe("useViewedHistoryRefresh", () => {
  const range = { since: "2026-01-01T00:00:00Z", until: "2026-01-02T00:00:00Z" };
  let calls: string[];
  let visibility: DocumentVisibilityState;

  function setVisibility(value: DocumentVisibilityState) {
    visibility = value;
    document.dispatchEvent(new Event("visibilitychange"));
  }

  beforeEach(() => {
    vi.useFakeTimers();
    calls = [];
    visibility = "visible";
    vi.spyOn(document, "visibilityState", "get").mockImplementation(() => visibility);
    vi.stubGlobal("fetch", vi.fn(async (url: string) => {
      calls.push(url);
      return new Response("{}", { status: 200, headers: { "Content-Type": "application/json" } });
    }));
  });

  afterEach(() => {
    vi.useRealTimers();
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
  });

  it("checks the phone when a section opens and on return, not on a timer", async () => {
    const { unmount } = renderHook(() => useViewedHistoryRefresh("demo-chat", true, true, range));
    await act(async () => { await vi.advanceTimersByTimeAsync(500); });
    expect(calls).toEqual(["/api/v1/inbox/conversations/demo-chat/refresh"]);

    await act(async () => { await vi.advanceTimersByTimeAsync(5 * 60_000); });
    expect(calls).toHaveLength(1);

    await act(async () => { setVisibility("hidden"); await vi.advanceTimersByTimeAsync(0); });
    expect(calls).toHaveLength(1);
    await act(async () => { setVisibility("visible"); await vi.advanceTimersByTimeAsync(0); });
    expect(calls).toHaveLength(2);

    unmount();
    await act(async () => { setVisibility("visible"); await vi.advanceTimersByTimeAsync(60_000); });
    expect(calls).toHaveLength(2);
  });
});
