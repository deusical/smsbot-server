import { test, expect, type Page, signIn } from "./fixtures";

const nav = (page: Page, name: string) =>
  page.getByRole("navigation", { name: "Main navigation" }).getByRole("button", { name });

/** Holds every matching request until release() is called. */
function hold(page: Page, pattern: string) {
  let release!: () => void;
  const gate = new Promise<void>((resolve) => (release = resolve));
  return page
    .route(pattern, async (route) => {
      await gate;
      await route.continue();
    })
    .then(() => release);
}

test("settings that load slowly show progress, then say it is slow, then appear", async ({ page }) => {
  const release = await hold(page, "**/api/v1/config");
  await signIn(page);
  await nav(page, "Settings").click();
  // A fast load shows nothing; a wait shows what is loading.
  await page.clock.runFor(400);
  await expect(page.getByRole("status")).toContainText("Loading settings…");
  await expect(page.getByRole("button", { name: "Replace phone" })).toHaveCount(0);
  await page.clock.runFor(6500);
  await expect(page.getByText("This is taking longer than usual")).toBeVisible();
  release();
  await expect(page.getByRole("navigation", { name: "Settings sections" })).toBeVisible();
  await expect(page.getByText("Loading settings…")).toHaveCount(0);
});

test("settings that fail to load say why, offer to try again and keep trying", async ({ page }) => {
  let failing = true;
  let calls = 0;
  await page.route("**/api/v1/config", async (route) => {
    calls++;
    if (failing) return route.fulfill({ status: 503, json: { error: "service_in_maintenance" } });
    await route.continue();
  });
  await signIn(page);
  await nav(page, "Settings").click();
  const alert = page.getByRole("alert").filter({ hasText: "Couldn’t load settings" });
  await expect(alert).toContainText("service in maintenance");
  // It tries again by itself, a little later.
  const before = calls;
  await page.clock.runFor(8500);
  await expect.poll(() => calls).toBeGreaterThan(before);
  // The button retries at once, and a recovered server shows the settings.
  failing = false;
  await alert.getByRole("button", { name: "Try again" }).click();
  await expect(page.getByRole("navigation", { name: "Settings sections" })).toBeVisible();
  await expect(alert).toHaveCount(0);
});

test("Replace phone is offered when settings cannot load, not while they are loading", async ({ page }) => {
  await page.route("**/api/v1/state", async (route) => {
    const response = await route.fetch();
    if (!response.ok()) return route.fulfill({ response });
    await route.fulfill({ json: { ...(await response.json()), fixture: false } });
  });
  await page.route("**/api/v1/phone-pairing", (route) => route.fulfill({ json: { state: "idle" } }));
  let failing = false;
  let release!: () => void;
  const gate = new Promise<void>((resolve) => (release = resolve));
  await page.route("**/api/v1/config", async (route) => {
    await gate;
    if (failing) return route.fulfill({ status: 503, json: { error: "service_in_maintenance" } });
    await route.continue();
  });
  await signIn(page);
  await nav(page, "Settings").click();
  await page.clock.runFor(400);
  await expect(page.getByRole("status")).toContainText("Loading settings…");
  await expect(page.getByRole("button", { name: "Replace phone" })).toHaveCount(0);
  failing = true;
  release();
  await expect(page.getByRole("alert")).toContainText("Couldn’t load settings");
  await expect(page.getByRole("button", { name: "Replace phone" })).toBeVisible();
});

test("Activity does not claim there are no requests while it is still loading", async ({ page }) => {
  const release = await hold(page, "**/api/v1/activity");
  await signIn(page);
  await nav(page, "Activity").click();
  await page.clock.runFor(400);
  await expect(page.getByRole("status")).toContainText("Loading activity…");
  await expect(page.getByText("No requests yet")).toHaveCount(0);
  release();
  await expect(page.getByText("Loading activity…")).toHaveCount(0);
});

test("the conversation list does not claim there are none while it is still loading", async ({ page }) => {
  const release = await hold(page, "**/api/v1/activity");
  await signIn(page);
  await nav(page, "Conversations").click();
  await page.clock.runFor(400);
  await expect(page.getByText("No conversations yet")).toHaveCount(0);
  release();
  await expect(page.getByText("Loading conversations…")).toHaveCount(0);
});

const workers = {
  config: { workers: [{ name: "lenovo", endpoint: "http://100.64.0.1:8767/download" }] },
  status: [{ name: "lenovo", endpoint: "http://100.64.0.1:8767/download", token: true, online: true, tasks: {} }],
  local: false,
};

test("a tab you already opened shows what it had at once while a slow refresh runs", async ({ page }) => {
  let calls = 0;
  let release!: () => void;
  const gate = new Promise<void>((resolve) => (release = resolve));
  await page.route("**/api/v1/workers", async (route) => {
    calls++;
    if (calls > 1) await gate;
    await route.fulfill({ json: workers });
  });
  await signIn(page);
  await nav(page, "Settings").click();
  const tabs = page.getByRole("navigation", { name: "Settings sections" });
  await tabs.getByRole("button", { name: "Workers" }).click();
  await expect(page.getByText("lenovo").first()).toBeVisible();
  await expect(page.getByText("Updated just now")).toBeVisible();
  // Leave and come back: the workers are there immediately, not a loading screen.
  await tabs.getByRole("button", { name: "Limits & quiet hours" }).click();
  await tabs.getByRole("button", { name: "Workers" }).click();
  await expect(page.getByText("lenovo").first()).toBeVisible();
  await expect(page.getByText("Loading workers…")).toHaveCount(0);
  await expect(page.getByText("Updating…")).toBeVisible();
  release();
  await expect(page.getByText("Updated just now")).toBeVisible();
});

test("a failed refresh keeps the workers on screen and Refresh tries again", async ({ page }) => {
  let failing = false;
  await page.route("**/api/v1/workers", async (route) =>
    failing ? route.fulfill({ status: 500, json: { error: "boom" } }) : route.fulfill({ json: workers }),
  );
  await signIn(page);
  await nav(page, "Settings").click();
  await page.getByRole("navigation", { name: "Settings sections" }).getByRole("button", { name: "Workers" }).click();
  await expect(page.getByText("Updated just now")).toBeVisible();
  failing = true;
  await page.getByRole("button", { name: "Refresh" }).click();
  await expect(page.getByText(/Couldn’t update\. Showing what was loaded/)).toBeVisible();
  await expect(page.getByText("lenovo").first()).toBeVisible();
  failing = false;
  await page.getByRole("button", { name: "Refresh" }).click();
  await expect(page.getByText("Updated just now")).toBeVisible();
});
