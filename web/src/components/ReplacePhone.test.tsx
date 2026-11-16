import { act, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { ReplacePhone } from "./ReplacePhone";

afterEach(() => { vi.useRealTimers(); vi.restoreAllMocks(); vi.unstubAllGlobals(); });

it("submits copied text without retaining it, shows approval and cancels", async () => {
  vi.useFakeTimers();
  let state = { state: "idle", emoji: "" };
  const requests: { method: string; body?: string }[] = [];
  vi.stubGlobal("fetch", vi.fn(async (_url: unknown, init?: RequestInit) => {
    const method = init?.method ?? "GET";
    requests.push({ method, body: init?.body as string });
    if (method === "POST") state = { state: "pairing", emoji: "synthetic-emoji" };
    if (method === "DELETE") state = { state: "failed", emoji: "" };
    return new Response(JSON.stringify(state), { status: method === "POST" ? 202 : 200 });
  }));
  const view = render(<ReplacePhone />);
  await act(async () => { await vi.advanceTimersByTimeAsync(0); });
  fireEvent.click(screen.getByText("Replace phone"));
  fireEvent.change(screen.getByLabelText("Copied Google request"), { target: { value: "synthetic copied curl text" } });
  await act(async () => { fireEvent.click(screen.getByText("Pause and pair new phone")); });
  expect(requests.find(r => r.method === "POST")?.body).toBe(JSON.stringify({ cookies: "synthetic copied curl text" }));
  expect(screen.queryByLabelText("Copied Google request")).toBeNull();
  expect(screen.getByRole("status").textContent).toContain("synthetic-emoji");
  await act(async () => { fireEvent.click(screen.getByText("Cancel pairing")); });
  await act(async () => { await vi.advanceTimersByTimeAsync(1500); });
  expect(requests.some(r => r.method === "DELETE")).toBe(true);
  expect((screen.getByLabelText("Copied Google request") as HTMLTextAreaElement).value).toBe("");
  const reads = requests.length;
  view.unmount();
  await act(async () => { await vi.advanceTimersByTimeAsync(3000); });
  expect(requests.length).toBe(reads);
});

it("does not poll while closed and idle, and pauses active polling when hidden", async () => {
  vi.useFakeTimers();
  let visibility = "visible";
  vi.spyOn(document, "visibilityState", "get").mockImplementation(() => visibility as DocumentVisibilityState);
  let reads = 0;
  vi.stubGlobal("fetch", vi.fn(async () => {
    reads++;
    return new Response(JSON.stringify({ state: "idle", emoji: "" }), { status: 200 });
  }));

  const view = render(<ReplacePhone />);
  await act(async () => { await vi.advanceTimersByTimeAsync(0); });
  expect(reads).toBe(1);

  // Idle and closed: neither timers nor visibility transitions poll.
  await act(async () => { await vi.advanceTimersByTimeAsync(15000); });
  visibility = "hidden";
  act(() => { document.dispatchEvent(new Event("visibilitychange")); });
  visibility = "visible";
  await act(async () => { document.dispatchEvent(new Event("visibilitychange")); });
  expect(reads).toBe(1);

  // Opening the flow starts polling, pauses when hidden, and catches up on return.
  fireEvent.click(screen.getByText("Replace phone"));
  await act(async () => { await vi.advanceTimersByTimeAsync(1500); });
  expect(reads).toBe(2);
  visibility = "hidden";
  act(() => { document.dispatchEvent(new Event("visibilitychange")); });
  await act(async () => { await vi.advanceTimersByTimeAsync(6000); });
  expect(reads).toBe(2);
  visibility = "visible";
  await act(async () => { document.dispatchEvent(new Event("visibilitychange")); });
  expect(reads).toBe(3);

  // Closing the idle flow stops polling again.
  fireEvent.click(screen.getByText("Replace phone"));
  await act(async () => { await vi.advanceTimersByTimeAsync(15000); });
  expect(reads).toBe(3);
  view.unmount();
});
