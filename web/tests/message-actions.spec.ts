import {test, expect, type Page} from "@playwright/test";
import {readFileSync} from "node:fs";

async function prepare(page: Page, protocol = "RCS") {
  const at = new Date().toISOString();
  const world = {compose: [] as Record<string, unknown>[], reactions: [] as Record<string, unknown>[], actionState: "queued", capabilities: {reply: protocol === "RCS", react: protocol === "RCS", edit: false, version: "synthetic-version", edit_reason: "editing_unavailable_in_companion_transport"}};
  await page.route("**/api/v1/state", async route => {const response = await route.fetch();if(!response.ok())return route.fulfill({response});await route.fulfill({json: {...await response.json(), paused: false, transport_ready: true}});});
  await page.route("**/api/v1/inbox/settings", async route => {const response = await route.fetch();if(!response.ok())return route.fulfill({response});await route.fulfill({json: {...await response.json(), enabled: true, scope: "all"}});});
  await page.route("**/api/v1/inbox/conversations", route => route.fulfill({json: {conversations: [{id: "synthetic-actions", participant_id: "synthetic-peer", label: "Avery Quinn", protocol, writable: true, archived: true}], next_cursor: ""}}));
  await page.route("**/api/v1/inbox/conversations/*/history?*", route => route.fulfill({json: {messages: [{id: "original", text: "Are we still on for lunch?", direction: "incoming", at, protocol, reactions: [{emoji: "👍", count: 1, mine: true}]}, {id: "reply", text: "Yes, see you at noon.", direction: "outgoing", at, protocol, status: "read", quote: {id: "original", text: "Are we still on for lunch?", direction: "incoming", available: true}}], next_cursor: "", change_cursor: 0}}));
  await page.route("**/api/v1/inbox/conversations/*/changes?*", route => route.fulfill({json: {messages: [], cursor: 0, more: false}}));
  await page.route("**/api/v1/inbox/conversations/*/messages/*/actions", route => route.fulfill({json: world.capabilities}));
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
  const rect = await panel.boundingBox();expect(rect).not.toBeNull();expect(rect!.x).toBe(0);expect(rect!.y + rect!.height).toBeLessThanOrEqual(845);
  await page.screenshot({path: "test-results/message-actions-mobile.png"});
  await page.keyboard.press("Escape");await expect(panel).toHaveCount(0);
});
