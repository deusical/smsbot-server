import { test, expect, type Page } from "@playwright/test";
import { readFileSync } from "node:fs";
const headers = { Origin: "http://127.0.0.1:8788" };
async function setup(page: Page) {
  const descriptor = JSON.parse(readFileSync(".test-state/connection.json", "utf8"));
  await page.goto("/");
  await page.getByLabel("Owner credential").fill(readFileSync(descriptor.owner, "utf8"));
  await page.getByRole("button", { name: "Sign in", exact: true }).click();
  await expect(page.getByRole("heading", { name: "Home", exact: true })).toBeVisible();
  const previous = await (await page.request.get("/api/v1/inbox/settings")).json();
  expect((await page.request.put("/api/v1/inbox/settings", { headers, data: { ...previous, enabled: true, scope: "all", load_older: false, automatic_replies: false, archive_retention_days: 0 } })).ok()).toBeTruthy();
  await page.route("**/api/v1/inbox/conversations", route => route.fulfill({ json: { conversations: [{ id: "demo-chat", participant_id: "demo-peer", label: "Demo conversation", protocol: "RCS", writable: true, group: false, enrolled: false, archived: true }], next_cursor: "" } }));
  return async () => {
    const current = await (await page.request.get("/api/v1/inbox/settings")).json();
    await page.request.put("/api/v1/inbox/settings", { headers, data: { ...previous, revision: current.revision } });
  };
}
async function open(page: Page) {
  await page.reload();
  await page.getByRole("button", { name: "Conversations", exact: true }).click();
  await page.locator(".conversation").filter({ hasText: "Demo conversation" }).getByRole("button", { name: /Demo conversation/ }).click();
}
const messages = Array.from({ length: 200 }, (_, i) => ({ id: `view-row-${i}`, direction: "incoming", text: `Synthetic viewed message ${i}`, at: new Date(Date.UTC(2026, 0, 1, 0, i)).toISOString() }));

test("viewed sections refresh automatically and edits, gaps and original upgrades appear", async ({ page }) => {
  const cleanup = await setup(page);
  const ranges: { since: string; until: string }[] = [];
  let record: Record<string, unknown> | null = null;
  let fresh = false, heads = 0, imageReads = 0;
  const attachment = { id: "viewed-image", name: "synthetic.gif", mime: "image/gif", size: 0, archive_state: "pending", preview_only: true };
  await page.route("**/api/v1/inbox/archive", route => route.fulfill({ json: record ? [record] : [] }));
  await page.route("**/api/v1/inbox/conversations/demo-chat/history?*", route => {
    heads++;
    return route.fulfill({ json: { messages: messages.map(m => m.id === "view-row-197" ? { ...m, attachments: [attachment] } : m), next_cursor: "", change_cursor: 0 } });
  });
  await page.route("**/api/v1/inbox/conversations/demo-chat/refresh", route => {
    const range = route.request().postDataJSON(); ranges.push(range); fresh = true;
    record = { conversation: { id: "demo-chat" }, state: "running", imported: 0, refresh_since: range.since, refresh_until: range.until || new Date().toISOString(), refresh_state: "checked_available", refresh_checked_at: new Date().toISOString() };
    return route.fulfill({ status: 202, json: { ...record, refresh_state: "queued" } });
  });
  await page.route("**/api/v1/inbox/conversations/demo-chat/changes?*", route => route.fulfill({ json: {
    messages: fresh ? [{ ...messages[197], text: "Synthetic refreshed edit", revision: 1, attachments: [{ ...attachment, size: 4776505, archive_state: "saved", preview_only: false }] }, { id: "view-gap", direction: "incoming", text: "Synthetic recovered gap", at: new Date(Date.parse(messages[198].at) + 30000).toISOString() }] : [], cursor: fresh ? 2 : 0, more: false,
  } }));
  await page.route("**/attachments/viewed-image?*", route => { imageReads++; return route.fulfill({ contentType: "image/gif", body: Buffer.from("R0lGODlhAQABAIAAAAAAAP///ywAAAAAAQABAAACAUwAOw==", "base64") }); });
  try {
    await open(page);
    await expect.poll(() => ranges.length).toBeGreaterThan(0);
    expect(Date.parse(ranges[0].since)).toBeGreaterThan(Date.parse(messages[0].at));
    expect(ranges[0].until).toBe("");
    await expect(page.getByText("Synthetic refreshed edit", { exact: true })).toBeVisible({ timeout: 12000 });
    await expect(page.getByText("Synthetic recovered gap", { exact: true })).toBeVisible();
    await expect(page.locator(".attachment-info")).toContainText("4.6 MB");
    await expect(page.locator(".attachment-info")).not.toContainText("Thumbnail preview");
    await expect.poll(() => imageReads).toBeGreaterThan(1);
    const scroller = page.locator(".messages");
    await scroller.evaluate(el => { el.scrollTop = 0; });
    await expect.poll(() => ranges.some(r => r.since === messages[0].at && !!r.until)).toBeTruthy();
    await expect.poll(() => scroller.evaluate(el => el.scrollTop)).toBeLessThan(80);
    expect(heads).toBe(1);
  } finally { await cleanup(); }
});

test("view changes coalesce behind a pending refresh and offline history stays readable", async ({ page }) => {
  const cleanup = await setup(page);
  let ready = false, failRefresh = false;
  const ranges: { since: string; until: string }[] = [];
  let release!: () => void;
  const held = new Promise<void>(resolve => { release = resolve; });
  await page.route("**/api/v1/state", async route => {
    const response = await route.fetch(); const state = await response.json();
    await route.fulfill({ json: { ...state, transport_ready: ready } });
  });
  await page.route("**/api/v1/inbox/archive", route => route.fulfill({ json: [] }));
  await page.route("**/api/v1/inbox/conversations/demo-chat/history?*", route => route.fulfill({ json: { messages, next_cursor: "", change_cursor: 0 } }));
  await page.route("**/api/v1/inbox/conversations/demo-chat/changes?*", route => route.fulfill({ json: { messages: [], cursor: 0, more: false } }));
  await page.route("**/api/v1/inbox/conversations/demo-chat/refresh", async route => {
    const range = route.request().postDataJSON(); ranges.push(range);
    if (failRefresh) { await route.fulfill({ status: 503, json: { error: "synthetic_unavailable" } }); return; }
    if (ranges.length === 1) await held;
    await route.fulfill({ status: 202, json: { conversation: { id: "demo-chat" }, state: "running", refresh_state: "queued", refresh_since: range.since, refresh_until: range.until || new Date().toISOString() } });
  });
  try {
    await open(page);
    await expect(page.getByText("Synthetic viewed message 199", { exact: true })).toBeVisible();
    await expect(page.getByText(/Phone updates are waiting/)).toBeVisible();
    expect(ranges).toHaveLength(0);
    ready = true;
    await expect.poll(() => ranges.length, { timeout: 10000 }).toBe(1);
    await page.locator(".messages").evaluate(el => { el.scrollTop = 0; });
    await page.waitForTimeout(1000);
    expect(ranges).toHaveLength(1);
    release();
    await expect.poll(() => ranges.length).toBe(2);
    expect(ranges[1].since).toBe(messages[0].at);
    expect(ranges[1].until).not.toBe("");
    await page.evaluate(() => Object.defineProperty(document, "visibilityState", { configurable: true, value: "hidden" }));
    await page.locator(".messages").evaluate(el => { el.scrollTop = el.scrollHeight / 2; });
    await page.waitForTimeout(700);
    expect(ranges).toHaveLength(2);
    await page.evaluate(() => { Object.defineProperty(document, "visibilityState", { configurable: true, value: "visible" }); document.dispatchEvent(new Event("visibilitychange")); });
    await expect.poll(() => ranges.length).toBe(3);
    failRefresh = true;
    await page.locator(".messages").evaluate(el => { el.scrollTop = el.scrollHeight; });
    await expect(page.getByText("Could not check this section. Saved messages remain available; retrying automatically.", { exact: true })).toBeVisible();
    await expect(page.getByText("Synthetic viewed message 199", { exact: true })).toBeVisible();
  } finally { release(); await cleanup(); }
});
