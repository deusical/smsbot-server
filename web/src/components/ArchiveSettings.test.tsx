import { act, render } from "@testing-library/react";
import { expect, it, vi } from "vitest";
import { ArchiveSettings } from "./ArchiveSettings";

it("stops export status reads while hidden and catches up immediately on return", async () => {
  vi.useFakeTimers();
  let visibility = "visible";
  vi.spyOn(document, "visibilityState", "get").mockImplementation(() => visibility as DocumentVisibilityState);
  let reads = 0;
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
    if (String(input).endsWith("/backup")) reads++;
    return new Response(String(input).endsWith("/policy") ? JSON.stringify({enabled:true,max_file_mb:0,quota_mb:0}) : "null", {status:200});
  }));
  const view = render(<ArchiveSettings conversations={[]} />);
  await act(async () => { await vi.advanceTimersByTimeAsync(0); });
  expect(reads).toBe(1);
  visibility = "hidden";
  act(() => { document.dispatchEvent(new Event("visibilitychange")); });
  await act(async () => { await vi.advanceTimersByTimeAsync(15000); });
  expect(reads).toBe(1);
  visibility = "visible";
  await act(async () => { document.dispatchEvent(new Event("visibilitychange")); });
  expect(reads).toBe(2);
  await act(async () => { await vi.advanceTimersByTimeAsync(3000); });
  expect(reads).toBe(3);
  view.unmount();
  await act(async () => { await vi.advanceTimersByTimeAsync(6000); document.dispatchEvent(new Event("visibilitychange")); });
  expect(reads).toBe(3);
});
