import {test, expect, type Page} from "@playwright/test";
import {readFileSync} from "node:fs";
type Row = {id:string;label:string;last_message_at?:string;unread?:boolean;pinned?:boolean;preview?:string;preview_from_me?:boolean};
type Msg = {id:string;direction:string;text:string;at:string};
const iso = (minutesAgo: number) => new Date(Date.now() - minutesAgo * 60000).toISOString();

async function prepare(page: Page, rows: Row[], options: {readReceipts?: boolean; messages?: Msg[]; ready?: boolean} = {}) {
  // Install before navigation so every application timer uses the same clock.
  const started = Date.now();
  await page.clock.install({time: started});
  await page.clock.pauseAt(started + 1000);
  const world = {
    rows, revision: "1.1", typing: [] as string[],
    listCalls: 0, forcedCalls: 0, stale: false, partial: false, ready: options.ready ?? true, readCalls: [] as {id:string;through:string}[],
    messages: options.messages ?? [] as Msg[],
  };
  await page.route("**/api/v1/state", async route => {
    const response = await route.fetch();
    if (!response.ok()) return route.fulfill({response});
    await route.fulfill({json: {...await response.json(), paused: false, transport_ready: world.ready, conversations_revision: world.revision}});
  });
  await page.route("**/api/v1/inbox/settings", async route => {
    const response = await route.fetch();
    const body = await response.json();
    await route.fulfill({json: {...body, enabled: true, scope: "all", archive_retention_days: 0, ...(options.readReceipts === false ? {mark_read_when_viewed: false} : {})}});
  });
  await page.route(/\/api\/v1\/inbox\/conversations(\?refresh=1)?$/, route => {
    world.listCalls++;
    if (route.request().url().endsWith("?refresh=1")) world.forcedCalls++;
    return route.fulfill({json: {conversations: world.rows.map(r => ({participant_id: `peer-${r.id}`, protocol: "RCS", writable: true, archived: true, enrolled: false, ...r})), next_cursor: "", ...(world.stale ? {stale: true} : {}), ...(world.partial ? {partial: true} : {})}});
  });
  await page.route("**/api/v1/inbox/typing", route => route.fulfill({json: {conversations: world.typing}}));
  await page.route("**/api/v1/inbox/conversations/*/history?*", route => route.fulfill({json: {messages: world.messages, next_cursor: ""}}));
  await page.route("**/api/v1/inbox/conversations/*/read", route => {
    const id = route.request().url().split("/conversations/")[1].split("/")[0];
    world.readCalls.push({id, through: route.request().postDataJSON().through});
    return route.fulfill({json: {ok: true}});
  });
  const descriptor = JSON.parse(readFileSync(".test-state/connection.json", "utf8"));
  await page.goto("/");
  await page.getByLabel("Owner credential").fill(readFileSync(descriptor.owner, "utf8"));
  await page.getByRole("button", {name: "Sign in", exact: true}).click();
  await page.getByRole("button", {name: "Conversations", exact: true}).click();
  await expect(page.locator(".link-state")).toHaveAttribute("title", /^Last checked /);
  return world;
}
const row = (page: Page, label: string) => page.locator(".conversation").filter({hasText: label});

// Advance each polling turn separately and await its visible completion. A
// single large jump could skip async fetches while refresh is still in flight,
// making negative request-count assertions pass without exercising the polls.
async function poll(page: Page, turns = 1, step = 3000) {
  for (let i = 0; i < turns; i++) {
    const before = await page.locator(".link-state").getAttribute("title");
    expect(before).not.toBeNull();
    await page.clock.runFor(step);
    await expect(page.locator(".link-state")).not.toHaveAttribute("title", before!);
  }
}

test("rows show the last message and a typing indicator", async ({page}) => {
  const world = await prepare(page, [
    {id: "a", label: "Alex Preview", last_message_at: iso(1), preview: "See you at noon", preview_from_me: false},
    {id: "b", label: "Blair Preview", last_message_at: iso(5), preview: "Sounds good", preview_from_me: true},
  ]);
  await expect(row(page, "Alex Preview")).toContainText("See you at noon");
  await expect(row(page, "Blair Preview")).toContainText("You: Sounds good");
  world.typing = ["a"];
  await poll(page);
  await expect(row(page, "Alex Preview").locator(".typing-text")).toHaveText("typing…", {timeout: 8000});
  await row(page, "Alex Preview").getByRole("button", {name: /Alex Preview/}).click();
  await expect(page.locator(".thread-head .typing-text")).toBeVisible();
  await expect(page.locator(".typing-bubble")).toBeVisible();
  world.typing = [];
  await poll(page);
  await expect(page.locator(".typing-bubble")).toHaveCount(0, {timeout: 8000});
});

test("the list is refetched only when the server reports a change", async ({page}) => {
  const world = await prepare(page, [
    {id: "a", label: "First stable", last_message_at: iso(10)},
    {id: "b", label: "Second stable", last_message_at: iso(20)},
  ]);
  await expect(row(page, "First stable")).toBeVisible();
  const settled = world.listCalls;
  await poll(page, 3);
  expect(world.listCalls - settled).toBeLessThanOrEqual(1);
  world.rows = [{id: "a", label: "First stable", last_message_at: iso(10)}, {id: "b", label: "Second stable", last_message_at: iso(0)}];
  world.revision = "2.1";
  await poll(page);
  await expect.poll(async () => (await page.locator(".conversation .conversation-text strong").allTextContents()).map(t => t.replace(/ \((pinned|unread)\)/g, "")), {timeout: 10000}).toEqual(["Second stable", "First stable"]);
});

test("opening and returning to the dashboard each force one phone read; stale pages are asked again", async ({page}) => {
  const world = await prepare(page, [{id: "a", label: "Focus refresh", last_message_at: iso(10)}]);
  await expect(row(page, "Focus refresh")).toBeVisible();
  expect(world.forcedCalls).toBe(1);
  const settled = world.listCalls;
  await poll(page, 3);
  expect(world.listCalls).toBe(settled);
  await page.evaluate(() => Object.defineProperty(document, "visibilityState", {configurable: true, value: "hidden"}));
  await page.evaluate(() => document.dispatchEvent(new Event("visibilitychange")));
  await page.clock.runFor(9000);
  expect(world.listCalls).toBe(settled);
  world.stale = true;
  await page.evaluate(() => { Object.defineProperty(document, "visibilityState", {configurable: true, value: "visible"}); document.dispatchEvent(new Event("visibilitychange")); });
  await expect.poll(() => world.forcedCalls).toBe(2);
  await expect(page.locator(".link-state")).toHaveAttribute("title", await page.evaluate(() => "Last checked " + new Date().toLocaleTimeString()));
  // A stale page is fetched again on a later poll, without forcing another phone read.
  await poll(page);
  await expect.poll(() => world.listCalls, {timeout: 8000}).toBeGreaterThan(settled + 1);
  world.stale = false;
  const unforced = world.listCalls;
  await poll(page);
  await expect.poll(() => world.listCalls, {timeout: 8000}).toBeGreaterThan(unforced);
  const quiet = world.listCalls;
  await poll(page, 3);
  expect(world.listCalls).toBe(quiet);
  expect(world.forcedCalls).toBe(2);
});

test("a list read before the phone was ready is read again once it connects", async ({page}) => {
  const world = await prepare(page, [], {ready: false});
  await expect.poll(() => world.listCalls).toBeGreaterThan(0);
  const offline = world.listCalls;
  world.rows = [{id: "a", label: "Arrived on connect", last_message_at: iso(1)}];
  world.ready = true;
  await poll(page);
  await expect(row(page, "Arrived on connect")).toBeVisible({timeout: 8000});
  expect(world.listCalls).toBe(offline + 1);
});

test("a partial list is retried after a spacing interval and then left alone", async ({page}) => {
  const world = await prepare(page, [{id: "a", label: "Partial first", last_message_at: iso(5)}]);
  world.partial = true;
  // Read the partial page 1 ms after a polling tick. Each turn below then ends
  // exactly on a tick, so a poll's freshness check (which runs after its
  // requests, at the turn's end) happens 3,000k - 1 ms after the read.
  await page.clock.runFor(1);
  await page.evaluate(() => { Object.defineProperty(document, "visibilityState", {configurable: true, value: "visible"}); document.dispatchEvent(new Event("visibilitychange")); });
  await expect(page.getByText(/didn.t load/)).toBeVisible();
  const partialAt = world.listCalls;
  world.partial = false;
  world.rows = [...world.rows, {id: "b", label: "Retried later", last_message_at: iso(1)}];
  // Ten polls, the last checked 29,999 ms after the partial read: none may retry.
  await poll(page, 1, 2999);
  await poll(page, 9);
  expect(world.listCalls).toBe(partialAt);
  await expect(row(page, "Retried later")).toHaveCount(0);
  // The next poll, at 32,999 ms, is past the gap and retries exactly once.
  await poll(page);
  await expect(row(page, "Retried later")).toBeVisible();
  expect(world.listCalls).toBe(partialAt + 1);
  const complete = world.listCalls;
  await poll(page, 12);
  expect(world.listCalls).toBe(complete);
});

test("viewing an unread conversation marks it read through its newest message", async ({page}) => {
  const newest = iso(2);
  const world = await prepare(page, [{id: "a", label: "Unread viewing", last_message_at: newest, unread: true}], {
    messages: [{id: "m1", direction: "incoming", text: "Synthetic hello", at: iso(3)}, {id: "m2", direction: "incoming", text: "Synthetic again", at: newest}],
  });
  await row(page, "Unread viewing").getByRole("button", {name: /Unread viewing/}).click();
  await expect(page.getByText("Synthetic again", {exact: true})).toBeVisible();
  await expect.poll(() => world.readCalls.length, {timeout: 10000}).toBe(1);
  expect(world.readCalls[0].id).toBe("a");
  expect(Date.parse(world.readCalls[0].through)).toBe(Date.parse(newest));
  await poll(page, 2);
  expect(world.readCalls).toHaveLength(1);
});

test("an already read conversation is not marked again", async ({page}) => {
  const world = await prepare(page, [{id: "a", label: "Already read", last_message_at: iso(2)}], {
    messages: [{id: "m1", direction: "incoming", text: "Synthetic old", at: iso(3)}],
  });
  await row(page, "Already read").getByRole("button", {name: /Already read/}).click();
  await expect(page.getByText("Synthetic old", {exact: true})).toBeVisible();
  await poll(page, 2);
  expect(world.readCalls).toHaveLength(0);
});

test("read marking stays off when the setting is off", async ({page}) => {
  const world = await prepare(page, [{id: "a", label: "Receipts off", last_message_at: iso(2), unread: true}], {
    readReceipts: false,
    messages: [{id: "m1", direction: "incoming", text: "Synthetic quiet", at: iso(2)}],
  });
  await row(page, "Receipts off").getByRole("button", {name: /Receipts off/}).click();
  await expect(page.getByText("Synthetic quiet", {exact: true})).toBeVisible();
  await poll(page, 2);
  expect(world.readCalls).toHaveLength(0);
});

test("the read receipt setting is on by default and can be turned off", async ({page}) => {
  await prepare(page, [{id: "a", label: "Setting chat", last_message_at: iso(2)}]);
  await page.getByRole("button", {name: "Settings", exact: true}).click();
  await page.getByRole("button", {name: "Messages & privacy"}).first().click();
  const toggle = page.getByRole("switch", {name: "Mark messages read when I view them"});
  await expect(toggle).toBeChecked();
  await toggle.click();
  await expect(toggle).not.toBeChecked();
});
