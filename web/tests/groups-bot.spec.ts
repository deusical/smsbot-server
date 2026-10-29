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
// A command from a member of the demo group, as the phone would report it.
async function say(page: Page, member: string, text: string) {
  const r = await page.request.post("/api/v1/fixture/incoming", {
    headers,
    data: { id: `synthetic-${member}-${Date.now()}-${Math.random()}`, conversation_id: "demo-group", participant_id: member, text },
  });
  expect(r.ok()).toBeTruthy();
  return (await r.json()).accepted as boolean;
}
const groupJobs = async (page: Page): Promise<{ command: string; state: string; conversation_id: string }[]> =>
  ((await (await page.request.get("/api/v1/activity")).json()) as { conversation_id: string; command: string; state: string }[]).filter((j) => j.conversation_id === "demo-group");

test("the owner chooses who may use the bot in a group, and a change of people silences it until approved", async ({ page }) => {
  await login(page);
  const original = await getInbox(page);
  await putInbox(page, { ...original, enabled: true, scope: "all", groups: true, load_older: false, automatic_replies: true });
  await page.request.post("/api/v1/pause", { headers, data: { paused: false } });
  await setMembers(page, ["demo-member-a", "demo-member-b"]);
  try {
    await page.reload();
    await openGroup(page);
    const chip = page.locator(".group-bot").getByRole("button").first();
    await expect(chip).toHaveText("Bot off");
    // Nobody is enrolled yet.
    expect(await say(page, "demo-member-a", "!ping")).toBe(false);

    await chip.click();
    await page.getByRole("switch", { name: "Bot replies for Alex" }).click();
    await expect(chip).toHaveText("Bot on for Alex");

    // Alex may run commands; Sam, not enrolled, may not.
    expect(await say(page, "demo-member-b", "!ping")).toBe(false);
    expect(await say(page, "demo-member-a", "!ping")).toBe(true);
    await expect.poll(async () => (await groupJobs(page)).filter((j) => j.command === "!ping" && j.state === "submitted").length, { timeout: 15000 }).toBe(1);

    // Someone joins: the bot goes quiet, even for Alex.
    await setMembers(page, ["demo-member-a", "demo-member-b", "demo-member-c"]);
    expect(await say(page, "demo-member-a", "!ping")).toBe(false);
    await page.reload();
    await openGroup(page);
    await expect(chip).toHaveText("Bot paused: people changed");
    await chip.click();
    await expect(page.getByRole("alert").filter({ hasText: "the bot is silent here" })).toBeVisible();
    await page.getByRole("button", { name: "Approve these people" }).click();
    await expect(chip).toHaveText("Bot on for Alex");
    expect(await say(page, "demo-member-a", "!ping")).toBe(true);
  } finally {
    await page.request.delete("/api/v1/enrollment/demo-group", { headers });
    await setMembers(page, ["demo-member-a", "demo-member-b"]);
    await page.request.post("/api/v1/pause", { headers, data: { paused: true } });
    await putInbox(page, original);
  }
});

test("the owner can make a group share one AI conversation", async ({ page }) => {
  await login(page);
  const original = await getInbox(page);
  await putInbox(page, { ...original, enabled: true, scope: "all", groups: true, load_older: false });
  const shared = async () =>
    ((await (await page.request.get("/api/v1/inbox/conversations")).json()).conversations as { id: string; shared_ai?: boolean }[]).find((c) => c.id === "demo-group")?.shared_ai ?? false;
  try {
    await page.reload();
    await openGroup(page);
    await page.locator(".group-bot").getByRole("button").first().click();
    const toggle = page.getByRole("switch", { name: "One AI conversation for everyone" });
    await expect(toggle).not.toBeChecked();
    await expect(page.getByText("Each person has their own AI conversation.")).toBeVisible();
    await toggle.click();
    await expect(toggle).toBeChecked();
    await expect(page.getByText("Only you can clear it with !new.")).toBeVisible();
    expect(await shared()).toBe(true);
    await toggle.click();
    await expect(toggle).not.toBeChecked();
    expect(await shared()).toBe(false);
  } finally {
    await page.request.put("/api/v1/enrollment/demo-group/shared", { headers, data: { shared: false } });
    await putInbox(page, original);
  }
});
