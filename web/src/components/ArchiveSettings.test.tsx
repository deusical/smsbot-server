import { act, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { ArchiveSettings } from "./ArchiveSettings";

afterEach(() => {
  vi.useRealTimers();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

it("stops export status reads while hidden and catches up immediately on return while an export runs", async () => {
  vi.useFakeTimers();
  let visibility = "visible";
  vi.spyOn(document, "visibilityState", "get").mockImplementation(() => visibility as DocumentVisibilityState);
  let reads = 0;
  const runningJob = {
    id: "job-1",
    state: "running",
    scope: {},
    counts: { messages: 4, saved_files: 1, missing_files: 0, bytes: 512 },
  };
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL) => {
      if (String(input).endsWith("/backup")) reads++;
      return new Response(
        String(input).endsWith("/policy") ? JSON.stringify({ enabled: true, max_file_mb: 0, quota_mb: 0 }) : JSON.stringify(runningJob),
        { status: 200 },
      );
    }),
  );
  const view = render(<ArchiveSettings conversations={[]} />);
  await act(async () => {
    await vi.advanceTimersByTimeAsync(0);
  });
  expect(reads).toBe(1);
  visibility = "hidden";
  act(() => {
    document.dispatchEvent(new Event("visibilitychange"));
  });
  await act(async () => {
    await vi.advanceTimersByTimeAsync(15000);
  });
  expect(reads).toBe(1);
  visibility = "visible";
  await act(async () => {
    document.dispatchEvent(new Event("visibilitychange"));
  });
  expect(reads).toBe(2);
  await act(async () => {
    await vi.advanceTimersByTimeAsync(3000);
  });
  expect(reads).toBe(3);
  view.unmount();
  await act(async () => {
    await vi.advanceTimersByTimeAsync(6000);
    document.dispatchEvent(new Event("visibilitychange"));
  });
  expect(reads).toBe(3);
});

it("does not poll while idle, polls while running, and refreshes on cancel before stopping", async () => {
  vi.useFakeTimers();
  let visibility = "visible";
  vi.spyOn(document, "visibilityState", "get").mockImplementation(() => visibility as DocumentVisibilityState);
  let reads = 0;
  let job: unknown = null;
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      const method = init?.method ?? "GET";
      if (url.endsWith("/policy")) {
        return new Response(JSON.stringify({ enabled: true, max_file_mb: 0, quota_mb: 0 }), { status: 200 });
      }
      if (url.endsWith("/backup/cancel") && method === "POST") {
        job = { id: "job-2", state: "cancelled", scope: {}, counts: { messages: 2, saved_files: 0, missing_files: 0, bytes: 0 } };
        return new Response(JSON.stringify({ cancelling: true }), { status: 200 });
      }
      if (url.endsWith("/backup") && method === "POST") {
        job = { id: "job-2", state: "running", scope: {}, counts: { messages: 0, saved_files: 0, missing_files: 0, bytes: 0 } };
        return new Response(JSON.stringify(job), { status: 202 });
      }
      reads++;
      return new Response(JSON.stringify(job), { status: 200 });
    }),
  );

  const view = render(<ArchiveSettings conversations={[]} />);
  await act(async () => {
    await vi.advanceTimersByTimeAsync(0);
  });
  expect(reads).toBe(1);

  // Idle: no periodic reads and no visibility reads after the initial check.
  await act(async () => {
    await vi.advanceTimersByTimeAsync(15000);
  });
  visibility = "hidden";
  act(() => {
    document.dispatchEvent(new Event("visibilitychange"));
  });
  visibility = "visible";
  await act(async () => {
    document.dispatchEvent(new Event("visibilitychange"));
  });
  expect(reads).toBe(1);

  // Starting an export starts 3 s polling.
  await act(async () => {
    fireEvent.click(screen.getByRole("button", { name: "Start message export" }));
  });
  await act(async () => {
    await vi.advanceTimersByTimeAsync(3000);
  });
  expect(reads).toBe(2);

  // Cancelling refreshes immediately and stops polling once no longer running.
  await act(async () => {
    fireEvent.click(screen.getByRole("button", { name: "Cancel message export" }));
  });
  expect(reads).toBe(3);
  expect(screen.getByText(/Export cancelled/)).toBeTruthy();
  await act(async () => {
    await vi.advanceTimersByTimeAsync(15000);
  });
  expect(reads).toBe(3);
  view.unmount();
});
