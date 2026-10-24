import { act, renderHook } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { useDashboard } from "./useDashboard";
import { useSession } from "./useSession";

type Handler = (url: string) => Promise<Response> | Response;
const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
const state = (revision: string, extra = {}) => ({
  api_version: 1, paused: true, transport_ready: true, transport: "fixture", fixture: true,
  capabilities: ["full_inbox"], conversations_revision: revision, ...extra,
});

describe("useDashboard", () => {
  let calls: string[];
  let handler: Handler;
  let visibility: DocumentVisibilityState;

  beforeEach(() => {
    vi.useFakeTimers();
    calls = [];
    visibility = "visible";
    vi.spyOn(document, "visibilityState", "get").mockImplementation(() => visibility);
    vi.stubGlobal("fetch", vi.fn(async (url: string) => {
      calls.push(url);
      return handler(url);
    }));
  });
  afterEach(() => vi.restoreAllMocks());

  function mount() {
    return renderHook(() => {
      const session = useSession();
      const dashboard = useDashboard(session, () => {
        session.end();
        dashboard.reset();
      });
      return { session, dashboard };
    });
  }
  const settle = () => act(async () => { await vi.advanceTimersByTimeAsync(0); });

  it("reads the list once per revision and forces one fresh read on return", async () => {
    handler = (url) => {
      if (url.endsWith("/state")) return json(state("r1"));
      if (url.endsWith("/inbox/settings")) return json({ enabled: true, revision: 1, conversations: [] });
      if (url.endsWith("/activity")) return json([]);
      return json({ conversations: [{ id: "c1" }], next_cursor: "" });
    };
    const { result } = mount();
    await settle();
    expect(result.current.session.signedIn).toBe(true);
    expect(calls.filter((u) => u.includes("/inbox/conversations"))).toEqual(["/api/v1/inbox/conversations?refresh=1"]);
    // Unchanged revision: polling reads state, never the list.
    await act(async () => { await vi.advanceTimersByTimeAsync(9000); });
    expect(calls.filter((u) => u.includes("/inbox/conversations"))).toHaveLength(1);
    // Hidden tabs stop polling entirely.
    visibility = "hidden";
    const before = calls.length;
    await act(async () => { await vi.advanceTimersByTimeAsync(9000); });
    expect(calls.length).toBe(before);
    // Returning asks the phone once more.
    visibility = "visible";
    await act(async () => { document.dispatchEvent(new Event("visibilitychange")); await vi.advanceTimersByTimeAsync(0); });
    expect(calls.filter((u) => u.endsWith("?refresh=1"))).toHaveLength(2);
  });

  it("keeps pause and readiness current while discovery is slow", async () => {
    let releaseList: () => void = () => {};
    handler = (url) => {
      if (url.endsWith("/state")) return json(state("r1", { paused: calls.filter((u) => u.endsWith("/state")).length < 2 }));
      if (url.endsWith("/inbox/settings")) return json({ enabled: true, revision: 1, conversations: [] });
      if (url.endsWith("/activity")) return json([]);
      return new Promise((resolve) => { releaseList = () => resolve(json({ conversations: [], next_cursor: "" })); });
    };
    const { result } = mount();
    await settle();
    expect(result.current.dashboard.state?.paused).toBe(true);
    await act(async () => { await vi.advanceTimersByTimeAsync(3000); });
    expect(result.current.dashboard.state?.paused).toBe(false);
    releaseList();
    await settle();
  });

  it("drops a response that arrives after the session ended", async () => {
    let releaseActivity: () => void = () => {};
    handler = (url) => {
      if (url.endsWith("/state")) return json(state("r1"));
      if (url.endsWith("/inbox/settings")) return json({ enabled: true, revision: 1, conversations: [] });
      if (url.endsWith("/activity"))
        return new Promise((resolve) => { releaseActivity = () => resolve(json([{ id: "late", conversation_id: "c", command: "!ping", state: "queued", created_at: "" }])); });
      return json({ conversations: [], next_cursor: "" });
    };
    const { result } = mount();
    await settle();
    act(() => { result.current.session.end(); result.current.dashboard.reset(); });
    releaseActivity();
    await settle();
    expect(result.current.session.signedIn).toBe(false);
    expect(result.current.dashboard.jobs).toEqual([]);
  });
});
