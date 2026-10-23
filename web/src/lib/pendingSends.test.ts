import { act, renderHook } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { api, type Job } from "./api";
import {
  addPending, clearPending, getPending, usePendingFollower, type Pending,
} from "./pendingSends";

vi.mock("./api", () => ({ api: vi.fn() }));

const conversation = "synthetic-chat";
const pending: Pending = {
  key: "synthetic-request", text: "Synthetic message", files: [],
  at: "2026-01-01T00:00:00Z", route: "inbox", phase: "queued",
  job: "synthetic-job",
};

beforeEach(() => {
  clearPending();
  vi.useFakeTimers();
  vi.setSystemTime(new Date("2026-01-01T00:00:00Z"));
  vi.mocked(api).mockReset();
  vi.mocked(api).mockResolvedValue([]);
});
afterEach(() => clearPending());

async function follow(jobs: Job[]) {
  vi.mocked(api).mockResolvedValue(jobs);
  addPending(conversation, { ...pending, queuedAt: Date.now() });
  const hook = renderHook(() => usePendingFollower(true));
  await act(async () => {});
  return hook;
}

function job(state: string): Job {
  return {
    id: pending.job!, conversation_id: conversation, command: "compose",
    state, created_at: pending.at, transaction_id: "synthetic-transaction",
  };
}

describe("pending send tracking", () => {
  it("keeps an unknown submission uncertain and only polls for status", async () => {
    await follow([job("unknown_submission")]);
    expect(getPending(conversation, pending.key)).toMatchObject({
      phase: "failed", failure: "uncertain", key: pending.key, route: "inbox",
    });
    await act(async () => { await vi.advanceTimersByTimeAsync(60000); });
    expect(vi.mocked(api).mock.calls).toEqual([["/activity"]]);
    expect(getPending(conversation, pending.key)?.failure).toBe("uncertain");
  });

  it("waits for the missing-job grace period before marking a send uncertain", async () => {
    await follow([]);
    await act(async () => { await vi.advanceTimersByTimeAsync(30000); });
    expect(getPending(conversation, pending.key)?.phase).toBe("queued");
    await act(async () => { await vi.advanceTimersByTimeAsync(1000); });
    expect(getPending(conversation, pending.key)).toMatchObject({ phase: "failed", failure: "uncertain" });
    expect(vi.mocked(api).mock.calls.every(([path]) => path === "/activity")).toBe(true);
  });

  it.each(["submitted", "delivered", "read"])("reconciles %s to the transaction and expires the pending bubble", async state => {
    await follow([job(state)]);
    expect(getPending(conversation, pending.key)).toMatchObject({
      phase: "sent", transaction: "synthetic-transaction",
    });
    await act(async () => { await vi.advanceTimersByTimeAsync(16000); });
    expect(getPending(conversation, pending.key)).toBeUndefined();
  });

  it("does not poll when tracking is inactive and cancels polling on unmount", async () => {
    addPending(conversation, pending);
    const inactive = renderHook(() => usePendingFollower(false));
    await act(async () => { await vi.advanceTimersByTimeAsync(5000); });
    expect(api).not.toHaveBeenCalled();
    inactive.unmount();
    clearPending();
    const active = await follow([]);
    active.unmount();
    const calls = vi.mocked(api).mock.calls.length;
    await act(async () => { await vi.advanceTimersByTimeAsync(5000); });
    expect(api).toHaveBeenCalledTimes(calls);
  });
});
