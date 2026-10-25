import { test, expect, devices, type Page } from "@playwright/test";
import { readFileSync } from "node:fs";

function ownerToken() {
  const descriptor = JSON.parse(
    readFileSync(".test-state/connection.json", "utf8"),
  );
  return readFileSync(descriptor.owner, "utf8");
}
async function login(page: Page, remember = false) {
  await page.goto("/");
  await page.getByLabel("Owner credential").fill(ownerToken());
  const keep = page.getByRole("switch", {
    name: "Keep me signed in",
  });
  if ((await keep.isChecked()) !== remember) await keep.click();
  await page.getByRole("button", { name: "Sign in" }).click();
  await expect(
    page.getByRole("heading", { name: "Home", exact: true }),
  ).toBeVisible();
}

test.describe("installed app", () => {
  test.use({ serviceWorkers: "allow" });

  test("is installable and reopens offline without caching API data", async ({
    page,
    context,
  }) => {
    const manifest = await (
      await page.request.get("/manifest.webmanifest")
    ).json();
    expect(manifest).toMatchObject({
      name: "SmsBot",
      display: "standalone",
      start_url: "/",
    });
    for (const icon of manifest.icons)
      expect((await page.request.get(icon.src)).ok()).toBe(true);
    expect(
      manifest.icons.some((i: { purpose: string }) => i.purpose === "maskable"),
    ).toBe(true);

    await login(page, true);
    // Installing caches the page and its bundles, so the first offline
    // launch works without another online visit.
    await page.evaluate(() => navigator.serviceWorker.ready);
    const cached = await page.evaluate(async () => {
      const keys = await caches.keys();
      const urls: string[] = [];
      for (const k of keys)
        for (const r of await (await caches.open(k)).keys())
          urls.push(new URL(r.url).pathname);
      return urls;
    });
    expect(cached).toContain("/");
    expect(cached.some((u) => u.startsWith("/assets/"))).toBe(true);
    expect(cached.some((u) => u.startsWith("/api/"))).toBe(false);

    await context.setOffline(true);
    await page.reload();
    // The shell still opens and reports the lost connection.
    await expect(page.getByText("SmsBot").first()).toBeVisible();
    await context.setOffline(false);
  });
});

test("remembered sign-in stays until signed out everywhere", async ({
  page,
  context,
}) => {
  await login(page, true);
  const cookie = (await context.cookies()).find(
    (c) => c.name === "smsbot_session",
  )!;
  const days = (cookie.expires * 1000 - Date.now()) / 86_400_000;
  expect(days).toBeGreaterThan(390);
  expect(cookie.httpOnly).toBe(true);
  await page
    .getByRole("navigation", { name: "Main navigation" })
    .getByRole("button", { name: "Settings" })
    .click();
  await page
    .getByRole("navigation", { name: "Settings sections" })
    .getByRole("button", { name: "Devices" })
    .click();
  await page.getByRole("button", { name: "Sign out everywhere" }).click();
  await expect(page.getByLabel("Owner credential")).toBeVisible();
});

test.describe("phone layout", () => {
  const { defaultBrowserType: _, ...pixel } = devices["Pixel 7"];
  test.use({ ...pixel, serviceWorkers: "block" });

  test("bottom navigation gives way to a full-screen thread", async ({
    page,
  }) => {
    await login(page);
    const nav = page.getByRole("navigation", { name: "Main navigation" });
    await nav.getByRole("button", { name: "Conversations" }).tap();
    await expect(nav).toBeVisible();
    // The list only refreshes on events, so enroll through the UI rather than the API.
    const enroll = page.getByRole("switch", {
      name: "Bot replies for Demo conversation",
    });
    if (!(await enroll.isChecked())) {
      await enroll.tap();
      await expect(enroll).toBeChecked();
    }
    await page
      .getByRole("button", { name: /Demo conversation/ })
      .first()
      .tap();
    const box = page.getByLabel("Message", { exact: true });
    await expect(box).toBeVisible();
    await expect(nav).toBeHidden();
    // The thread scrolls inside itself; the page behind it never moves, so the
    // header cannot slide away when the browser bar changes the viewport.
    expect(
      await page.evaluate(() => getComputedStyle(document.documentElement).overflowY),
    ).toBe("hidden");
    // Touch keyboards: Enter adds a line rather than sending.
    await box.fill("first line");
    await box.press("Enter");
    await expect(box).toHaveValue("first line\n");
    await box.fill("");
    const size = await box.evaluate((el) => getComputedStyle(el).fontSize);
    expect(size).toBe("16px");
    await page.getByRole("button", { name: "Back to conversations" }).tap();
    await expect(nav).toBeVisible();
  });
});
