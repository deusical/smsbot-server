import { test, expect, type Page } from "@playwright/test";
import { readFileSync } from "node:fs";

const headers = { Origin: "http://127.0.0.1:8788" };
type Inbox = Record<string, unknown>;
const getInbox = async (page: Page): Promise<Inbox> => (await page.request.get("/api/v1/inbox/settings")).json();
async function putInbox(page: Page, settings: Inbox) {
  const current = await getInbox(page);
  const saved = await page.request.put("/api/v1/inbox/settings", { headers, data: { ...settings, revision: current.revision } });
  expect(saved.ok()).toBeTruthy();
}
const setMembers = (page: Page, members: string[]) => page.request.post("/api/v1/fixture/group-members", { headers, data: { members } });
async function login(page: Page) {
  const c = JSON.parse(readFileSync(".test-state/connection.json", "utf8"));
  await page.goto("/");
  await page.getByLabel("Owner credential").fill(readFileSync(c.owner, "utf8"));
  await page.getByRole("button", { name: "Sign in", exact: true }).click();
  await expect(page.getByRole("navigation", { name: "Main navigation" })).toBeVisible();
}
async function openGroup(page: Page) {
  await page.getByRole("navigation", { name: "Main navigation" }).getByRole("button", { name: "Conversations", exact: true }).click();
  await page.locator(".conversation").filter({ hasText: "Group example" }).getByRole("button").first().click();
  await expect(page.getByRole("heading", { name: "Group example" })).toBeVisible();
}
const jobs = async (page: Page): Promise<{ command: string; state: string; conversation_id: string }[]> => (await page.request.get("/api/v1/activity")).json();

// Sending is on, groups are saved, and the demo group has its usual two people.
async function prepare(page: Page) {
  await login(page);
  const original = await getInbox(page);
  await putInbox(page, { ...original, enabled: true, scope: "all", groups: true, load_older: false });
  await page.request.post("/api/v1/pause", { headers, data: { paused: false } });
  await setMembers(page, ["demo-member-a", "demo-member-b"]);
  return original;
}
async function restore(page: Page, original: Inbox) {
  await setMembers(page, ["demo-member-a", "demo-member-b"]);
  await page.request.post("/api/v1/pause", { headers, data: { paused: true } });
  await putInbox(page, original);
}

test("the owner can send to a group, seeing who is in it", async ({ page }) => {
  const original = await prepare(page);
  try {
    await page.reload();
    await openGroup(page);
    await expect(page.locator(".thread-title")).toContainText("Group, 2 people: Alex, Sam");
    await page.getByLabel("Message", { exact: true }).fill("Synthetic hello group");
    await page.getByRole("button", { name: "Send message", exact: true }).click();
    await expect.poll(async () => (await jobs(page)).filter((j) => j.conversation_id === "demo-group" && j.state === "submitted").length, { timeout: 15000 }).toBe(1);
  } finally {
    await restore(page, original);
  }
});

test("a group that changed before the send is refused, and nothing goes to the new people", async ({ page }) => {
  const original = await prepare(page);
  try {
    await page.reload();
    await openGroup(page);
    const before = (await jobs(page)).filter((j) => j.conversation_id === "demo-group").length;
    // Someone joins after the owner looked, before they send.
    await setMembers(page, ["demo-member-a", "demo-member-b", "demo-member-c"]);
    await page.getByLabel("Message", { exact: true }).fill("Synthetic message that must not go");
    await page.getByRole("button", { name: "Send message", exact: true }).click();
    await expect(page.locator(".bubble.pending")).toContainText("the people in this group changed");
    expect((await jobs(page)).filter((j) => j.conversation_id === "demo-group")).toHaveLength(before);
  } finally {
    await restore(page, original);
  }
});

test("when the people in a group change while it is open, sending waits until they are looked at", async ({ page }) => {
  let changed = false;
  let revision = 0;
  await page.route("**/api/v1/state", async (route) => {
    const response = await route.fetch();
    if (!response.ok()) return route.fulfill({ response });
    await route.fulfill({ json: { ...(await response.json()), conversations_revision: `group-${revision++}` } });
  });
  await page.route(/\/api\/v1\/inbox\/conversations(\?refresh=1)?$/, async (route) => {
    const response = await route.fetch();
    const body = await response.json();
    if (changed)
      body.conversations = body.conversations.map((c: { id: string; members?: unknown[] }) =>
        c.id === "demo-group" ? { ...c, roster: "group:changed", members: [...(c.members ?? []), { id: "demo-member-z", name: "Zed" }] } : c,
      );
    await route.fulfill({ json: body });
  });
  const original = await prepare(page);
  try {
    await page.reload();
    await openGroup(page);
    await expect(page.locator(".group-changed")).toHaveCount(0);
    changed = true;
    const banner = page.locator(".group-changed");
    await expect(banner).toContainText("Zed", { timeout: 15000 });
    await page.getByLabel("Message", { exact: true }).fill("Synthetic draft");
    await expect(page.getByRole("button", { name: "Send message", exact: true })).toBeDisabled();
    await expect(page.locator(".composer-blocked")).toContainText("people in this group changed");
    await banner.getByRole("button", { name: "Send to these people" }).click();
    await expect(banner).toHaveCount(0);
    await expect(page.getByRole("button", { name: "Send message", exact: true })).toBeEnabled();
  } finally {
    await restore(page, original);
  }
});
