import { act, renderHook, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { clearResources, peekResource, storeResource, useResource } from "./resource";
import { ago } from "../components/RefreshBar";

const words = (e: unknown) => (e instanceof Error ? e.message : "failed");

describe("useResource", () => {
  beforeEach(() => {
    clearResources();
  });

  it("loads once, then shows the kept copy at once next time while refreshing quietly", async () => {
    const load = vi.fn().mockResolvedValueOnce("first").mockResolvedValueOnce("second");
    const first = renderHook(() => useResource("k", load, words));
    expect(first.result.current.data).toBeNull();
    await waitFor(() => expect(first.result.current.data).toBe("first"));
    first.unmount();

    const again = renderHook(() => useResource("k", load, words));
    // The kept copy is there on the very first render: no loading screen.
    expect(again.result.current.data).toBe("first");
    expect(again.result.current.refreshing).toBe(true);
    await waitFor(() => expect(again.result.current.data).toBe("second"));
    expect(again.result.current.refreshing).toBe(false);
  });

  it("keeps showing the old data when a refresh fails, and says why", async () => {
    storeResource("k", "old");
    const load = vi.fn().mockRejectedValue(new Error("server busy"));
    const { result } = renderHook(() => useResource("k", load, words));
    await waitFor(() => expect(result.current.error).toBe("server busy"));
    expect(result.current.data).toBe("old");
    load.mockResolvedValueOnce("new");
    await act(() => result.current.refresh());
    expect(result.current.data).toBe("new");
    expect(result.current.error).toBe("");
  });

  it("does not start a second request while one is running", async () => {
    let finish: (v: string) => void = () => {};
    const load = vi.fn(() => new Promise<string>((r) => (finish = r)));
    const { result } = renderHook(() => useResource("k", load, words));
    act(() => void result.current.refresh());
    act(() => void result.current.refresh());
    expect(load).toHaveBeenCalledTimes(1);
    await act(async () => finish("done"));
    expect(result.current.data).toBe("done");
  });

  it("keeps nothing in browser storage, and forgets it all on sign-out", () => {
    storeResource("a", { x: 1 });
    expect(localStorage.length + sessionStorage.length).toBe(0);
    expect(peekResource("a")).toBeDefined();
    clearResources();
    expect(peekResource("a")).toBeUndefined();
  });
});

describe("ago", () => {
  it("says how old data is in plain words", () => {
    const now = Date.parse("2026-11-07T12:00:00Z");
    expect(ago(now - 10_000, now)).toBe("just now");
    expect(ago(now - 5 * 60_000, now)).toBe("5 min ago");
    expect(ago(now - 3 * 3600_000, now)).toBe("3 h ago");
  });
});
