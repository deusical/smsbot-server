import { test, expect, type Page } from "@playwright/test";
import { readFileSync } from "node:fs";

// A group whose phone answers are synthetic: this checks what the dashboard asks
// for, that a reply or reaction in a group carries the people the owner saw.
const members = [{ id: "a", name: "Alex" }, { id: "b", name: "Sam" }];
async function prepare(page: Page) {
  const at = new Date().toISOString();
  const world = { compose: [] as Record<string, unknown>[], reactions: [] as Record<string, unknown>[] };
  await page.route("**/api/v1/state", async (route) => {
    const response = await route.fetch();
    if (!response.ok()) return route.fulfill({ response });
    await route.fulfill({ json: { ...(await response.json()), paused: false, transport_ready: true } });
  });
  await page.route("**/api/v1/inbox/settings", async (route) => {
    const response = await route.fetch();
    if (!response.ok()) return route.fulfill({ response });
    await route.fulfill({ json: { ...(await response.json()), enabled: true, scope: "all", groups: true } });
  });
  await page.route(/\/api\/v1\/inbox\/conversations(\?refresh=1)?$/, (route) =>
    route.fulfill({ json: { conversations: [{ id: "synthetic-group", participant_id: "group:synthetic-group", label: "Lunch crew", protocol: "RCS", writable: true, archived: true, group: true, members, roster: "group:synthetic" }], next_cursor: "" } }),
  );
  await page.route("**/api/v1/inbox/conversations/*/history?*", (route) =>
    route.fulfill({ json: { messages: [{ id: "original", text: "Who is coming to lunch?", direction: "incoming", at, protocol: "RCS", sender: "a", reactions: [] }], next_cursor: "", change_cursor: 0 } }),
  );
  await page.route("**/api/v1/inbox/conversations/*/changes?*", (route) => route.fulfill({ json: { messages: [], cursor: 0, more: false } }));
  await page.route("**/api/v1/inbox/conversations/*/messages/*/actions", (route) =>
    route.fulfill({ json: { reply: true, react: true, edit: false, version: "synthetic-version", edit_reason: "editing_unavailable_in_companion_transport" } }),
  );
  await page.route("**/api/v1/inbox/conversations/*/messages/*/reactions", (route) => {
    world.reactions.push(route.request().postDataJSON());
    return route.fulfill({ status: 202, json: { id: "synthetic-reaction" } });
  });
  await page.route("**/api/v1/inbox/conversations/*/actions/*", (route) => route.fulfill({ json: { id: "synthetic-reaction", state: "queued" } }));
  await page.route("**/api/v1/inbox/conversations/*/compose", (route) => {
    world.compose.push(route.request().postDataJSON());
    return route.fulfill({ status: 202, json: { id: "synthetic-compose" } });
  });
  await page.route("**/api/v1/inbox/conversations/*/read", (route) => route.fulfill({ json: { ok: true } }));
  const descriptor = JSON.parse(readFileSync(".test-state/connection.json", "utf8"));
  await page.goto("/");
  await page.getByLabel("Owner credential").fill(readFileSync(descriptor.owner, "utf8"));
  await page.getByRole("button", { name: "Sign in", exact: true }).click();
  await page.getByRole("button", { name: "Conversations", exact: true }).click();
  await page.locator(".conversation").filter({ hasText: "Lunch crew" }).getByRole("button", { name: /Lunch crew/ }).click();
  await expect(page.locator('[data-message-id="original"]')).toBeVisible();
  return world;
}
const original = (page: Page) => page.locator('[data-message-id="original"]');
const menu = (page: Page) => page.getByRole("dialog", { name: "Message actions", exact: true });

test("a reaction in a group is sent with the people the owner saw", async ({ page }) => {
  const world = await prepare(page);
  await original(page).getByRole("button", { name: "Message actions", exact: true }).click();
  await menu(page).getByRole("button", { name: "React 👍", exact: true }).click();
  await expect.poll(() => world.reactions.length).toBe(1);
  expect(world.reactions[0]).toMatchObject({ emoji: "👍", operation: "add", roster: "group:synthetic" });
});

test("a reply in a group is sent with the people the owner saw", async ({ page }) => {
  const world = await prepare(page);
  await original(page).getByRole("button", { name: "Message actions", exact: true }).click();
  await menu(page).getByRole("button", { name: "Reply", exact: true }).click();
  await page.getByLabel("Message", { exact: true }).fill("Count me in");
  await page.getByRole("button", { name: "Send message", exact: true }).click();
  await expect.poll(() => world.compose.length).toBe(1);
  expect(world.compose[0]).toMatchObject({ reply_to: "original", roster: "group:synthetic" });
});
