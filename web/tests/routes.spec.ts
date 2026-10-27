import { test, expect, type Page } from "@playwright/test";
import { readFileSync } from "node:fs";

async function login(page: Page, path = "/") {
  const descriptor = JSON.parse(readFileSync(".test-state/connection.json", "utf8"));
  await page.goto(path);
  await page.getByLabel("Owner credential").fill(readFileSync(descriptor.owner, "utf8"));
  await page.getByRole("button", { name: "Sign in", exact: true }).click();
  // Signed in before any API call is made on the session's behalf.
  await expect(page.getByRole("navigation", { name: "Main navigation" })).toBeVisible();
}
const nav = (page: Page) => page.getByRole("navigation", { name: "Main navigation" });
const heading = (page: Page, name: string) =>
  expect(page.getByRole("heading", { name, exact: true, level: 1 })).toBeVisible();

test("each page has its own URL and Back and Forward move between them", async ({ page }) => {
  await login(page);
  await heading(page, "Home");
  await expect(page).toHaveURL(/\/$/);
  await nav(page).getByRole("button", { name: "Settings", exact: true }).click();
  await expect(page).toHaveURL(/\/settings$/);
  await expect(page).toHaveTitle("Settings · SmsBot");
  await nav(page).getByRole("button", { name: "Activity", exact: true }).click();
  await expect(page).toHaveURL(/\/activity$/);
  await page.goBack();
  await expect(page).toHaveURL(/\/settings$/);
  await heading(page, "Settings");
  await page.goBack();
  await expect(page).toHaveURL(/\/$/);
  await heading(page, "Home");
  await expect(page).toHaveTitle("SmsBot");
  await page.goForward();
  await expect(page).toHaveURL(/\/settings$/);
  await heading(page, "Settings");
});

test("reloading or opening a page's URL shows that page", async ({ page }) => {
  await login(page);
  await nav(page).getByRole("button", { name: "Commands", exact: true }).click();
  await expect(page).toHaveURL(/\/commands$/);
  await page.reload();
  await heading(page, "Commands");
  await expect(page).toHaveURL(/\/commands$/);
  // Signing in at a deep link lands on it.
  await page.context().clearCookies();
  await page.goto("/archive");
  await expect(page.getByLabel("Owner credential")).toBeVisible();
  const descriptor = JSON.parse(readFileSync(".test-state/connection.json", "utf8"));
  await page.getByLabel("Owner credential").fill(readFileSync(descriptor.owner, "utf8"));
  await page.getByRole("button", { name: "Sign in", exact: true }).click();
  await heading(page, "Archive");
  await expect(page).toHaveURL(/\/archive$/);
  // An unknown path shows Home and tidies the address.
  await page.goto("/settings/whatever");
  await heading(page, "Settings");
});

const headers = { Origin: "http://127.0.0.1:8788" };
async function allowInbox(page: Page, enabled: boolean) {
  const s = await (await page.request.get("/api/v1/inbox/settings")).json();
  const saved = await page.request.put("/api/v1/inbox/settings", {
    headers,
    data: { ...s, enabled, load_older: enabled, automatic_replies: !enabled },
  });
  expect(saved.ok()).toBeTruthy();
  await page.request.post("/api/v1/pause", { headers, data: { paused: true } });
}

test("a conversation has a URL and Back returns to the list", async ({ page }) => {
  await login(page);
  await allowInbox(page, true);
  try {
    await nav(page).getByRole("button", { name: "Conversations", exact: true }).click();
    await expect(page).toHaveURL(/\/conversations$/);
    await page.locator(".conversation").filter({ hasText: "Demo conversation" }).getByRole("button", { name: /Demo conversation/ }).click();
    await expect(page).toHaveURL(/\/conversations\/[^/]+$/);
    const opened = page.url();
    await page.goBack();
    await expect(page).toHaveURL(/\/conversations$/);
    await page.goForward();
    await expect(page).toHaveURL(opened);
    await page.reload();
    await expect(page).toHaveURL(opened);
    await heading(page, "Conversations");
    await expect(page.locator(".messages")).toBeVisible();
  } finally {
    await allowInbox(page, false);
  }
});
