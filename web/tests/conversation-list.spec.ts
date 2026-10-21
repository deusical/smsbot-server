import {test, expect, type Page} from "@playwright/test";
import {readFileSync} from "node:fs";
type Row = {id:string;label:string;last_message_at?:string;unread?:boolean;pinned?:boolean};
async function prepare(page: Page, rows: Row[], messageCount = 3) {
  const state = {rows, revision: 1};
  await page.route("**/api/v1/state", async route => {
    const response = await route.fetch();
    if (!response.ok()) return route.fulfill({response});
    await route.fulfill({json: {...await response.json(), conversations_revision: `test-${state.revision}`}});
  });
  await page.route("**/api/v1/inbox/settings", async route => {
    const response = await route.fetch();
    await route.fulfill({json: {...await response.json(), enabled: true, scope: "all", archive_retention_days: 0}});
  });
  await page.route(/\/api\/v1\/inbox\/conversations(\?refresh=1)?$/, route => route.fulfill({json: {conversations: state.rows.map(r => ({participant_id: `peer-${r.id}`, protocol: "RCS", writable: true, archived: true, enrolled: false, ...r})), next_cursor: ""}}));
  await page.route("**/api/v1/inbox/conversations/*/history?*", route => route.fulfill({json: {messages: Array.from({length: messageCount}, (_, i) => ({id: `m${i}`, direction: i % 2 ? "outgoing" : "incoming", text: `Synthetic message ${i}`, at: new Date(Date.UTC(2026, 9, 16, 12, i)).toISOString()})), next_cursor: ""}}));
  const descriptor = JSON.parse(readFileSync(".test-state/connection.json", "utf8"));
  await page.goto("/");
  await page.getByLabel("Owner credential").fill(readFileSync(descriptor.owner, "utf8"));
  await page.getByRole("button", {name: "Sign in", exact: true}).click();
  await page.getByRole("button", {name: "Conversations", exact: true}).click();
  return state;
}
const names = async (page: Page) => (await page.locator(".conversation .conversation-text strong").allTextContents()).map(t => t.replace(/ \((pinned|unread)\)/g, ""));
const now = Date.now();
const iso = (minutesAgo: number) => new Date(now - minutesAgo * 60000).toISOString();

test("conversations order by recency with pinned first and reorder on new activity", async ({page}) => {
  const state = await prepare(page, [
    {id: "a", label: "Older chat", last_message_at: iso(600)},
    {id: "b", label: "Newest chat", last_message_at: iso(5)},
    {id: "c", label: "Pinned chat", last_message_at: iso(3000), pinned: true},
    {id: "d", label: "Middle chat", last_message_at: iso(90)},
  ]);
  await expect(page.locator(".conversation")).toHaveCount(4);
  expect(await names(page)).toEqual(["Pinned chat", "Newest chat", "Middle chat", "Older chat"]);
  await expect(page.locator(".conversation").filter({hasText: "Pinned chat"}).locator(".conversation-flags svg")).toHaveCount(1);
  state.rows[0].last_message_at = iso(0);
  state.revision++;
  await expect.poll(() => names(page), {timeout: 10000}).toEqual(["Pinned chat", "Older chat", "Newest chat", "Middle chat"]);
});

test("unread indicator clears when the conversation is opened", async ({page}) => {
  await prepare(page, [
    {id: "a", label: "Unread chat", last_message_at: iso(2), unread: true},
    {id: "b", label: "Quiet chat", last_message_at: iso(20)},
  ]);
  const unread = page.locator(".conversation").filter({hasText: "Unread chat"});
  await expect(unread).toHaveClass(/unread/);
  await expect(page.locator(".conversation").filter({hasText: "Quiet chat"})).not.toHaveClass(/unread/);
  await unread.getByRole("button", {name: /Unread chat/}).click();
  await expect(unread).not.toHaveClass(/unread/);
});

test("keyboard shortcuts focus search and switch conversations", async ({page}) => {
  await prepare(page, [
    {id: "a", label: "First chat", last_message_at: iso(1)},
    {id: "b", label: "Second chat", last_message_at: iso(2)},
  ]);
  await page.keyboard.press("Control+k");
  await expect(page.getByLabel("Search conversations")).toBeFocused();
  await page.keyboard.press("Escape");
  await page.locator(".conversation").filter({hasText: "First chat"}).getByRole("button", {name: /First chat/}).click();
  await expect(page.getByRole("heading", {name: "First chat", exact: true})).toBeVisible();
  await page.keyboard.press("Alt+ArrowDown");
  await expect(page.getByRole("heading", {name: "Second chat", exact: true})).toBeVisible();
  await page.keyboard.press("Alt+ArrowUp");
  await expect(page.getByRole("heading", {name: "First chat", exact: true})).toBeVisible();
});

test("pasting an image attaches it and text pastes are untouched", async ({page}) => {
  await prepare(page, [{id: "a", label: "Paste chat", last_message_at: iso(1)}]);
  await page.locator(".conversation").getByRole("button", {name: /Paste chat/}).click();
  const box = page.getByLabel("Message", {exact: true});
  await expect(box).toBeVisible();
  const prevented = await box.evaluate(el => {
    const data = new DataTransfer();
    data.items.add(new File([new Uint8Array([137, 80, 78, 71])], "image.png", {type: "image/png"}));
    const event = new ClipboardEvent("paste", {clipboardData: data, bubbles: true, cancelable: true});
    el.dispatchEvent(event);
    return event.defaultPrevented;
  });
  expect(prevented).toBe(true);
  await expect(page.getByLabel("Selected files")).toContainText(/image-\d{8}-\d{6}-\d+\.png/);
  const textPrevented = await box.evaluate(el => {
    const data = new DataTransfer();
    data.setData("text/plain", "plain text");
    const event = new ClipboardEvent("paste", {clipboardData: data, bubbles: true, cancelable: true});
    el.dispatchEvent(event);
    return event.defaultPrevented;
  });
  expect(textPrevented).toBe(false);
});

test("jump to latest appears after scrolling up and scrollbars are themed", async ({page}) => {
  await prepare(page, [{id: "a", label: "Long chat", last_message_at: iso(1)}], 80);
  await page.locator(".conversation").getByRole("button", {name: /Long chat/}).click();
  await expect(page.getByText("Synthetic message 79", {exact: true})).toBeVisible();
  await expect(page.getByRole("button", {name: "Jump to latest"})).toHaveCount(0);
  await page.locator(".messages").evaluate(el => {el.scrollTop = 0;});
  const jump = page.getByRole("button", {name: "Jump to latest"});
  await expect(jump).toBeVisible();
  await jump.click();
  await expect.poll(() => page.locator(".messages").evaluate(el => el.scrollHeight - el.scrollTop - el.clientHeight)).toBeLessThan(80);
  await expect(jump).toHaveCount(0);
  expect(await page.locator(".conversation-list").evaluate(el => getComputedStyle(el).scrollbarWidth)).toBe("thin");
  expect(await page.locator(".messages").evaluate(el => getComputedStyle(el).scrollbarWidth)).toBe("thin");
});
