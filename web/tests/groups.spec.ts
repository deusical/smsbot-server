import { test, expect, type Page } from "@playwright/test";
import { readFileSync } from "node:fs";

const headers = { Origin: "http://127.0.0.1:8788" };
async function login(page: Page, path = "/") {
  const c = JSON.parse(readFileSync(".test-state/connection.json", "utf8"));
  await page.goto(path);
  await page.getByLabel("Owner credential").fill(readFileSync(c.owner, "utf8"));
  await page.getByRole("button", { name: "Sign in" }).click();
  await expect(page.getByRole("button", { name: "Sign out" }).or(page.getByRole("navigation", { name: "Main navigation" }))).toBeVisible();
}
type Inbox = Record<string, unknown>;
const getInbox = async (page: Page): Promise<Inbox> => (await page.request.get("/api/v1/inbox/settings")).json();
async function putInbox(page: Page, settings: Inbox) {
  const current = await getInbox(page);
  const saved = await page.request.put("/api/v1/inbox/settings", { headers, data: { ...settings, revision: current.revision } });
  expect(saved.ok()).toBeTruthy();
}
// Captures everything, with or without groups, and puts the owner's settings
// back afterwards so other tests start from the same place.
async function setGroups(page: Page, groups: boolean) {
  await putInbox(page, { ...(await getInbox(page)), enabled: true, scope: "all", groups, load_older: false });
  await page.request.post("/api/v1/pause", { headers, data: { paused: true } });
}

test("a group is saved with who sent each message and shown that way", async ({ page }) => {
  await login(page);
  const original = await getInbox(page);
  await setGroups(page, true);
  try {
    for (const [id, sender, text] of [
      ["synthetic-group-1", "demo-member-a", "Synthetic hello from Alex"],
      ["synthetic-group-2", "demo-member-b", "Synthetic hello from Sam"],
    ]) {
      const received = await page.request.post("/api/v1/fixture/incoming", {
        headers,
        data: { id: `${id}-${Date.now()}`, conversation_id: "demo-group", participant_id: sender, text },
      });
      expect(received.ok()).toBeTruthy();
    }
    await page.getByRole("navigation", { name: "Main navigation" }).getByRole("button", { name: "Conversations", exact: true }).click();
    const row = page.locator(".conversation").filter({ hasText: "Group example" });
    await row.getByRole("button").first().click();
    await expect(page.getByRole("heading", { name: "Group example" })).toBeVisible();
    await expect(page.locator(".thread-title")).toContainText("Group, 2 people");
    const bubble = (text: string) => page.locator(".bubble").filter({ hasText: text });
    await expect(bubble("Synthetic hello from Alex").locator(".bubble-sender")).toHaveText("Alex");
    await expect(bubble("Synthetic hello from Sam").locator(".bubble-sender")).toHaveText("Sam");
    // The bot never runs in a group in this version.
    await expect(page.getByRole("switch", { name: "Bot replies for Group example" })).toBeDisabled();
  } finally {
    await putInbox(page, original);
  }
});

test("Settings offers to include groups when capturing everything", async ({ page }) => {
  await login(page);
  const original = await getInbox(page);
  await setGroups(page, false);
  // Start from the settings just saved.
  await page.reload();
  await page.getByRole("navigation", { name: "Main navigation" }).getByRole("button", { name: "Settings", exact: true }).click();
  await page.getByRole("navigation", { name: "Settings sections" }).getByRole("button", { name: "Messages & privacy" }).click();
  const toggle = page.getByRole("switch", { name: "Include group chats" });
  await expect(toggle).not.toBeChecked();
  await toggle.click();
  await page.getByRole("button", { name: "Save changes" }).click();
  await expect.poll(async () => (await getInbox(page)).groups).toBe(true);
  await putInbox(page, original);
});
