import { act, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { ReplacePhone } from "./ReplacePhone";

afterEach(() => { vi.useRealTimers(); vi.unstubAllGlobals(); });

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
