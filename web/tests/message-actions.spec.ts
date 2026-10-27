import {test, expect, type Page} from "@playwright/test";
import {readFileSync} from "node:fs";

async function prepare(page: Page, protocol = "RCS", actionGate?: Promise<void>) {
  const at = new Date().toISOString();
  const world = {compose: [] as Record<string, unknown>[], reactions: [] as Record<string, unknown>[], actionState: "queued", capabilities: {reply: protocol === "RCS", react: protocol === "RCS", edit: false, version: "synthetic-version", edit_reason: "editing_unavailable_in_companion_transport"}};
  await page.route("**/api/v1/state", async route => {const response = await route.fetch();if(!response.ok())return route.fulfill({response});await route.fulfill({json: {...await response.json(), paused: false, transport_ready: true}});});
  await page.route("**/api/v1/inbox/settings", async route => {const response = await route.fetch();if(!response.ok())return route.fulfill({response});await route.fulfill({json: {...await response.json(), enabled: true, scope: "all"}});});
  await page.route(/\/api\/v1\/inbox\/conversations(\?refresh=1)?$/, route => route.fulfill({json: {conversations: [{id: "synthetic-actions", participant_id: "synthetic-peer", label: "Avery Quinn", protocol, writable: true, archived: true}], next_cursor: ""}}));
  await page.route("**/api/v1/inbox/conversations/*/history?*", route => route.fulfill({json: {messages: [{id: "original", text: "Are we still on for lunch?", direction: "incoming", at, protocol, reactions: [{emoji: "👍", count: 1, mine: true}]}, {id: "reply", text: "Yes, see you at noon.", direction: "outgoing", at, protocol, status: "read", quote: {id: "original", text: "Are we still on for lunch?", direction: "incoming", available: true}}], next_cursor: "", change_cursor: 0}}));
  await page.route("**/api/v1/inbox/conversations/*/changes?*", route => route.fulfill({json: {messages: [], cursor: 0, more: false}}));
  await page.route("**/api/v1/inbox/conversations/*/messages/*/actions", async route => {await actionGate;await route.fulfill({json: world.capabilities});});
  await page.route("**/api/v1/inbox/conversations/*/messages/*/reactions", route => {world.reactions.push(route.request().postDataJSON());return route.fulfill({status: 202, json: {id: "synthetic-reaction"}});});
  await page.route("**/api/v1/inbox/conversations/*/actions/*", route => route.fulfill({json: {id: "synthetic-reaction", state: world.actionState}}));
  await page.route("**/api/v1/inbox/conversations/*/compose", route => {world.compose.push(route.request().postDataJSON());return route.fulfill({status: 202, json: {id: "synthetic-compose"}});});
  await page.route("**/api/v1/inbox/conversations/*/read", route => route.fulfill({json: {ok: true}}));
  const descriptor = JSON.parse(readFileSync(".test-state/connection.json", "utf8"));
  await page.goto("/");await page.getByLabel("Owner credential").fill(readFileSync(descriptor.owner, "utf8"));await page.getByRole("button", {name: "Sign in", exact: true}).click();
  await page.getByRole("button", {name: "Conversations", exact: true}).click();
  await page.locator(".conversation").filter({hasText: "Avery Quinn"}).getByRole("button", {name: /Avery Quinn/}).click();
  await expect(page.locator('[data-message-id="original"]')).toBeVisible();
  return world;
}
const original = (page: Page) => page.locator('[data-message-id="original"]');

test("actions load on demand when opening menu and reuse cached capabilities in memory", async ({page}) => {
  const requests: string[] = [];
  page.on("request", request => {if (request.url().endsWith("/messages/original/actions")) requests.push(request.url());});
  await prepare(page);
  const trigger = original(page).getByRole("button", {name: "Message actions", exact: true});
  await expect(trigger).toHaveAttribute("aria-busy", "false");
  expect(requests).toHaveLength(0);
  await trigger.click();
  await expect.poll(() => requests.length).toBe(1);
  // The request is sent before its response renders, so wait for the menu to settle.
  const menu = page.getByRole("dialog", {name: "Message actions", exact: true});
  await expect(menu).toContainText("Reply");
  await expect(menu).not.toContainText("Loading actions");
  await page.keyboard.press("Escape");
  // A cold request after this point would fail: subsequent opening must use cached data in memory.
  await page.route("**/messages/original/actions", route => route.fulfill({status: 503}));
  await trigger.click();
  await expect(menu).toContainText("Reply");
  await expect(menu).not.toContainText("Loading actions");
  await page.keyboard.press("Escape");
  expect(requests).toHaveLength(1);
});

test("opening menu shows loading status for capabilities while local actions remain immediately accessible", async ({page}) => {
  let release!: () => void;
  const gate = new Promise<void>(resolve => {release = resolve;});
  let requests = 0;
  page.on("request", request => {if (request.url().endsWith("/messages/original/actions")) requests++;});
  await prepare(page, "RCS", gate);
  expect(requests).toBe(0);
  await original(page).getByRole("button", {name: "Message actions", exact: true}).click();
  await expect.poll(() => requests).toBe(1);
  const menu = page.getByRole("dialog", {name: "Message actions", exact: true});
  await expect(menu.getByRole("status")).toHaveText("Loading actions...");
  await expect(menu.getByRole("button", {name: "Copy text", exact: true})).toBeVisible();
  await expect(menu.getByRole("button", {name: "Details", exact: true})).toBeVisible();
  await expect(menu.getByRole("button", {name: "Reply", exact: true})).toHaveCount(0);
  expect(requests).toBe(1);
  release();
  await expect(menu.getByRole("button", {name: "Reply", exact: true})).toBeVisible();
  expect(requests).toBe(1);
});

test("failed capabilities show an explicit retry while keeping details and copy text accessible", async ({page}) => {
  await prepare(page);
  // The last registered matching route wins, so replace the helper route.
  await page.route("**/messages/original/actions", route => route.fulfill({status: 503}));
  await page.reload();
  await page.getByRole("button", {name: "Conversations", exact: true}).click();
  await page.locator(".conversation").filter({hasText: "Avery Quinn"}).getByRole("button", {name: /Avery Quinn/}).click();
  await original(page).getByRole("button", {name: "Message actions", exact: true}).click();
  const menu = page.getByRole("dialog", {name: "Message actions", exact: true});
  await expect(menu.getByRole("alert")).toContainText("unavailable");
  await expect(menu.getByRole("button", {name: "Details", exact: true})).toBeVisible();
  await expect(menu.getByRole("button", {name: "Copy text", exact: true})).toBeVisible();
  let release!: () => void;
  const gate = new Promise<void>(resolve => {release = resolve;});
  await page.route("**/messages/original/actions", async route => {await gate;await route.fulfill({json: {reply: true, react: true, edit: false, version: "retry-version"}});});
  await menu.getByRole("button", {name: "Try again", exact: true}).focus();
  await page.keyboard.press("Enter");
  await expect(menu.getByRole("status")).toHaveText("Loading actions...");
  await page.keyboard.press("Tab");
  await expect(menu.getByRole("button", {name: "Close message actions", exact: true})).toBeFocused();
  release();
  await expect(menu.getByRole("button", {name: "Reply", exact: true})).toBeVisible();
});

test("RCS menu, quoted reply, cancel, details and focus", async ({page}) => {
  const world = await prepare(page);
  await original(page).hover();await original(page).getByRole("button", {name: "Message actions", exact: true}).click();
  const menu = page.getByRole("dialog", {name: "Message actions", exact: true});
  await expect(menu.getByRole("button", {name: "React 👍", exact: true})).toBeVisible();
  await expect(menu.getByRole("button", {name: "Edit", exact: true})).toHaveCount(0);
  await page.screenshot({path: "test-results/message-actions-desktop.png"});
  await menu.getByRole("button", {name: "Reply", exact: true}).click();
  await expect(page.locator(".reply-composer-preview")).toContainText("Replying to Avery Quinn");
  await expect(page.getByRole("textbox", {name: "Message", exact: true})).toBeFocused();
  await page.getByRole("textbox", {name: "Message", exact: true}).fill("Synthetic reply");
  await page.getByRole("button", {name: "Cancel reply"}).click();
  await expect(page.getByRole("textbox", {name: "Message", exact: true})).toHaveValue("Synthetic reply");
  await original(page).getByRole("button", {name: "Message actions", exact: true}).click();await menu.getByRole("button", {name: "Reply", exact: true}).click();
  await page.getByRole("textbox", {name: "Message", exact: true}).press("Enter");
  await expect.poll(() => world.compose.length).toBe(1);expect(world.compose[0].reply_to).toBe("original");
  await expect(page.locator(".reply-composer-preview")).toHaveCount(0);
  await page.getByRole("button", {name: "Jump to original message"}).click();await expect(original(page)).toHaveClass(/message-highlight/);
  await original(page).getByRole("button", {name: "Message actions", exact: true}).click();await menu.getByRole("button", {name: "Details", exact: true}).click();
  await expect(page.getByRole("dialog", {name: "Message details"})).toContainText("RCS");await page.keyboard.press("Escape");
  await expect(original(page).getByRole("button", {name: "Message actions", exact: true})).toBeFocused();
});

test("reaction remove, uncertain status, and Unicode picker", async ({page}) => {
  const world = await prepare(page);
  await original(page).hover();await original(page).getByRole("button", {name: "Message actions", exact: true}).click();
  await page.getByRole("button", {name: "React 👍", exact: true}).click();await expect.poll(() => world.reactions.length).toBe(1);
  expect(world.reactions[0].operation).toBe("remove");
  world.actionState = "unknown_submission";
  await expect(original(page)).toContainText("Reaction outcome uncertain", {timeout: 6000});
  await expect(original(page).getByRole("button", {name: "Retry"})).toHaveCount(0);
  world.actionState = "reaction_confirmed";await expect(original(page).locator(".reaction-progress")).toHaveCount(0, {timeout: 6000});
  await original(page).getByRole("button", {name: "Message actions", exact: true}).click();
  await page.getByRole("button", {name: "More emoji"}).click();await page.getByRole("button", {name: "React 👍🏽", exact: true}).click();
  await expect.poll(() => world.reactions.length).toBe(2);expect(world.reactions[1].emoji).toBe("👍🏽");expect(world.reactions[1].operation).toBe("switch");
});

test("SMS shares the menu without native RCS actions", async ({page}) => {
  await prepare(page, "SMS");await original(page).hover();await original(page).getByRole("button", {name: "Message actions", exact: true}).click();
  const menu = page.getByRole("dialog", {name: "Message actions", exact: true});
  await expect(menu.getByRole("button", {name: "Copy text", exact: true})).toBeVisible();
  await expect(menu.getByRole("button", {name: "Details", exact: true})).toBeVisible();
  await expect(menu.getByRole("button", {name: "Reply", exact: true})).toHaveCount(0);await expect(menu.getByLabel("Choose a reaction")).toHaveCount(0);
});

test("mobile actions use a bottom sheet inside the viewport", async ({page}) => {
  await page.setViewportSize({width: 390, height: 844});await prepare(page);
  await original(page).getByRole("button", {name: "Message actions", exact: true}).click();
  const panel = page.getByRole("dialog", {name: "Message actions", exact: true});await expect(panel).toBeVisible();
  // The sheet slides up; judge its resting position.
  await expect.poll(async () => {const box = await panel.boundingBox();return box ? box.y + box.height : Infinity;}).toBeLessThanOrEqual(845);
  const rect = await panel.boundingBox();expect(rect).not.toBeNull();expect(rect!.x).toBe(0);
  await page.screenshot({path: "test-results/message-actions-mobile.png"});
  await page.keyboard.press("Escape");await expect(panel).toHaveCount(0);
});

type Box = {x: number; y: number; width: number; height: number};
const overlaps = (a: Box, b: Box) => !(a.x + a.width <= b.x || b.x + b.width <= a.x || a.y + a.height <= b.y || b.y + b.height <= a.y);

for (const id of ["original", "reply"]) {
  test(`the menu opens beside the ${id} message instead of covering it`, async ({page}) => {
    await page.setViewportSize({width: 1280, height: 800});await prepare(page);
    const bubble = page.locator(`[data-message-id="${id}"]`);
    await bubble.hover();await bubble.getByRole("button", {name: "Message actions", exact: true}).click();
    const panel = page.getByRole("dialog", {name: "Message actions", exact: true});await expect(panel.getByRole("button", {name: "Reply", exact: true})).toBeVisible();
    const [menu, message] = [await panel.boundingBox(), await bubble.boundingBox()];
    expect(menu).not.toBeNull();expect(message).not.toBeNull();
    expect(overlaps(menu!, message!)).toBe(false);
    expect(menu!.x).toBeGreaterThanOrEqual(0);expect(menu!.y).toBeGreaterThanOrEqual(0);
    expect(menu!.x + menu!.width).toBeLessThanOrEqual(1280);expect(menu!.y + menu!.height).toBeLessThanOrEqual(800);
  });
}

test("a single reaction shows no count and the details view can go back", async ({page}) => {
  await prepare(page);
  await expect(original(page).locator(".reaction-chip")).toHaveText("👍");
  await original(page).hover();await original(page).getByRole("button", {name: "Message actions", exact: true}).click();
  await page.getByRole("dialog", {name: "Message actions", exact: true}).getByRole("button", {name: "Details", exact: true}).click();
  await expect(page.getByRole("dialog", {name: "Message details"})).toBeVisible();
  await page.getByRole("button", {name: "Back to actions", exact: true}).click();
  await expect(page.getByRole("dialog", {name: "Message actions", exact: true}).getByRole("button", {name: "Reply", exact: true})).toBeVisible();
});

test("touch: tapping the message opens the bottom sheet", async ({browser}) => {
  const context = await browser.newContext({baseURL: "http://127.0.0.1:8788", serviceWorkers: "block", viewport: {width: 390, height: 844}, hasTouch: true, isMobile: true});
  try {
    const page = await context.newPage();await prepare(page);
    await original(page).locator("p").tap();
    const panel = page.getByRole("dialog", {name: "Message actions", exact: true});await expect(panel.getByRole("button", {name: "Reply", exact: true})).toBeVisible();
    const [sheet, message] = [await panel.boundingBox(), await original(page).boundingBox()];
    expect(sheet).not.toBeNull();expect(message).not.toBeNull();
    // The message is scrolled clear of the sheet, so it stays visible as context.
    expect(message!.y + message!.height).toBeLessThanOrEqual(sheet!.y);
  } finally {await context.close();}
});

test("unavailable actions say why and are asked for again on the next open", async ({page}) => {
  const world = await prepare(page);
  world.capabilities = {...world.capabilities, reply: false, react: false, reason: "message_protocol_unsupported"} as typeof world.capabilities;
  await original(page).hover();await original(page).getByRole("button", {name: "Message actions", exact: true}).click();
  const menu = page.getByRole("dialog", {name: "Message actions", exact: true});
  await expect(menu.getByRole("status")).toHaveText("Reply and reactions appear once your phone confirms this message.");
  await expect(menu.getByRole("button", {name: "Reply", exact: true})).toHaveCount(0);
  await page.keyboard.press("Escape");
  // The phone confirms the message: the next open shows the actions.
  world.capabilities = {...world.capabilities, reply: true, react: true, reason: undefined} as typeof world.capabilities;
  await original(page).hover();await original(page).getByRole("button", {name: "Message actions", exact: true}).click();
  await expect(menu.getByRole("button", {name: "Reply", exact: true})).toBeVisible();
  await expect(menu.getByRole("status")).toHaveCount(0);
});
