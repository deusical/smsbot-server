import { test, expect, type Page } from "@playwright/test";
import { readFileSync } from "node:fs";

// Headless browsers cannot reach a real push service, so the browser's push
// subscription is stubbed; registration, settings and storage are real.
test.use({ serviceWorkers: "allow", permissions: ["notifications"] });

const headers = { Origin: "http://127.0.0.1:8788" };

async function login(page: Page) {
  await page.addInitScript(() => {
    // Like a real browser, the subscription survives reloads.
    const fake = {
      endpoint: "https://fcm.googleapis.com/fcm/send/synthetic-this-browser",
      options: { applicationServerKey: null },
      toJSON: () => ({ endpoint: fake.endpoint, keys: { p256dh: "synthetic-p256dh", auth: "synthetic-auth" } }),
      unsubscribe: async () => { localStorage.removeItem("synthetic-push"); return true; },
    };
    PushManager.prototype.subscribe = async function () { localStorage.setItem("synthetic-push", "1"); return fake as unknown as PushSubscription; };
    PushManager.prototype.getSubscription = async function () { return (localStorage.getItem("synthetic-push") ? fake : null) as PushSubscription | null; };
  });
  await page.goto("/");
  const d = JSON.parse(readFileSync(".test-state/connection.json", "utf8"));
  await page.getByLabel("Owner credential").fill(readFileSync(d.owner, "utf8"));
  await page.getByRole("button", { name: "Sign in" }).click();
  await expect(page.getByRole("heading", { name: "Home", exact: true })).toBeVisible();
}

async function devices(page: Page) {
  return (await (await page.request.get("/api/v1/notifications")).json()).devices as { id: string; label: string; prefs: { messages: string; connection: boolean } }[];
}

test("each device turns notifications on and keeps its own settings", async ({ page }) => {
  await login(page);
  for (const d of await devices(page)) await page.request.delete(`/api/v1/notifications/devices/${d.id}`, { headers });
  // Another device registered earlier.
  const other = await (await page.request.post("/api/v1/notifications/devices", { headers, data: { endpoint: "https://fcm.googleapis.com/fcm/send/synthetic-phone", keys: { p256dh: "k", auth: "a" }, label: "Spare phone" } })).json();

  await page.getByRole("navigation", { name: "Main navigation" }).getByRole("button", { name: "Settings", exact: true }).click();
  await page.getByRole("navigation", { name: "Settings sections" }).getByRole("button", { name: "Notifications" }).click();
  await page.getByLabel("Device name").fill("Legion");
  await page.getByRole("button", { name: "Turn on notifications" }).click();
  await expect(page.getByText("On", { exact: true })).toBeVisible();

  const mine = page.locator(".group").filter({ hasText: "This device" });
  await mine.getByRole("group", { name: "Message notifications" }).getByRole("button", { name: "Off" }).click();
  await mine.getByRole("switch", { name: "Phone connection" }).click();
  await expect.poll(async () => (await devices(page)).find((d) => d.label === "Legion")?.prefs.messages).toBe("off");
  const after = await devices(page);
  expect(after.find((d) => d.label === "Legion")?.prefs.connection).toBe(false);
  expect(after.find((d) => d.id === other.id)?.prefs).toMatchObject({ messages: "all", connection: true });

  const others = page.locator(".group").filter({ hasText: "Other devices" });
  await expect(others).toContainText("Spare phone");
  await expect(others).toContainText("All messages");
  await others.getByRole("button", { name: "Edit" }).click();
  await others.getByRole("group", { name: "Message notifications" }).getByRole("button", { name: "Bot on" }).click();
  await expect.poll(async () => (await devices(page)).find((d) => d.id === other.id)?.prefs.messages).toBe("bot_on");
  await others.getByRole("button", { name: "Remove" }).click();
  await expect(page.locator(".group").filter({ hasText: "Other devices" })).toHaveCount(0);

  // Reopening Settings recognises this browser as the same device.
  await page.reload();
  await page.getByRole("navigation", { name: "Main navigation" }).getByRole("button", { name: "Settings", exact: true }).click();
  await page.getByRole("navigation", { name: "Settings sections" }).getByRole("button", { name: "Notifications" }).click();
  await expect(page.getByRole("button", { name: "Turn off on this device" })).toBeVisible();
  await page.screenshot({ path: "test-results/notifications-settings.png", fullPage: true });
  await page.getByRole("button", { name: "Turn off on this device" }).click();
  await expect(page.getByRole("button", { name: "Turn on notifications" })).toBeVisible();
  expect(await devices(page)).toHaveLength(0);
});

test("a notification link opens its conversation", async ({ page }) => {
  await login(page);
  await page.goto("/?open=conversation&id=demo-chat");
  await expect(page.getByRole("heading", { name: "Conversations", exact: true })).toBeVisible();
  await expect(page).toHaveURL(/\/conversations\/demo-chat$/);
});

test("on a phone, a long inbox and unbreakable names stay inside the screen", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  const conversations = Array.from({ length: 120 }, (_, i) => ({ id: `synthetic-${i}`, participant_id: `peer-${i}`, label: i === 7 ? "7777777774*827*48458777777**5755847085787707*8" : `Synthetic contact ${i}`, protocol: "RCS", writable: true, archived: false, enrolled: false }));
  await page.route(/\/api\/v1\/inbox\/conversations(\?.*)?$/, (route) => route.fulfill({ json: { conversations, next_cursor: "" } }));
  await login(page);
  for (const d of await devices(page)) await page.request.delete(`/api/v1/notifications/devices/${d.id}`, { headers });
  const nav = page.getByRole("navigation", { name: "Main navigation" });
  await nav.getByRole("button", { name: "Settings", exact: true }).click();
  await page.getByRole("navigation", { name: "Settings sections" }).getByRole("button", { name: "Notifications" }).click();
  const name = page.getByLabel("Device name");
  await expect(name).toBeVisible();
  expect((await name.boundingBox())!.height).toBeGreaterThanOrEqual(34);
  await page.getByRole("button", { name: "Turn on notifications" }).click();
  const picker = page.getByRole("group", { name: "Muted conversations" });
  await expect(picker.getByRole("button", { name: /7777777774/ })).toBeAttached();
  expect((await picker.boundingBox())!.height).toBeLessThanOrEqual(250);
  const wide = await page.evaluate(() => {
    const w = document.getElementById("workspace")!;
    return { doc: document.documentElement.scrollWidth - innerWidth, workspace: w.scrollWidth - w.clientWidth };
  });
  expect(wide).toEqual({ doc: 0, workspace: 0 });
  // Muting puts the conversation first and finding narrows the list.
  await picker.getByRole("button", { name: "Synthetic contact 99" }).click();
  await expect(picker.getByRole("button").first()).toHaveAccessibleName(/Synthetic contact 99/);
  await page.getByRole("searchbox", { name: "Find a conversation" }).fill("contact 42");
  await expect(picker.getByRole("button")).toHaveCount(1);
  await page.screenshot({ path: "test-results/notifications-phone.png" });
  for (const d of await devices(page)) await page.request.delete(`/api/v1/notifications/devices/${d.id}`, { headers });
});
