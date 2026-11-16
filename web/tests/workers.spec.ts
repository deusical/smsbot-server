import { test, expect, signIn } from "./fixtures";

test("Workers are added, saved with a stored token, refused off Tailscale and removed", async ({ page }) => {
  const workerGets: string[] = [];
  page.on("request", (req) => {
    const url = new URL(req.url());
    if (req.method() === "GET" && url.pathname === "/api/v1/workers") {
      workerGets.push(url.pathname + url.search);
    }
  });
  await signIn(page);
  await page.getByRole("navigation", { name: "Main navigation" }).getByRole("button", { name: "Settings" }).click();
  await page.getByRole("navigation", { name: "Settings sections" }).getByRole("button", { name: "Workers" }).click();
  await expect(page.getByRole("heading", { name: "Workers" })).toBeVisible();
  expect(workerGets).toEqual(["/api/v1/workers"]);

  const refreshed = page.waitForResponse((res) => res.url().endsWith("/api/v1/workers?refresh=1") && res.request().method() === "GET");
  await page.locator(".refresh-bar").getByRole("button", { name: "Refresh" }).click();
  await refreshed;
  expect(workerGets).toEqual(["/api/v1/workers", "/api/v1/workers?refresh=1"]);

  await page.getByRole("button", { name: "Add worker" }).click();
  await page.getByLabel("Name").fill("laptop");
  await page.getByLabel("Address").fill("http://192.168.1.20:8767/download");
  await page.getByRole("button", { name: "Save workers" }).click();
  await expect(page.getByRole("alert")).toContainText("Tailscale");

  await page.getByLabel("Address").fill("http://100.92.5.20:8767/download");
  await page.getByLabel("Token").fill("synthetic-worker-token-for-offline-tests-only");
  await page.getByRole("button", { name: "Save workers" }).click();
  await expect(page.getByText("Workers saved.")).toBeVisible({ timeout: 10000 });
  // Saved, with its token stored but never shown; nothing answers there.
  await expect(page.getByLabel("Token")).toHaveValue("");
  await expect(page.getByLabel("Token")).toHaveAttribute("placeholder", "Stored");
  await expect(page.getByText("Offline")).toBeVisible();

  await page.getByRole("button", { name: "Remove" }).click();
  await page.getByRole("button", { name: "Save workers" }).click();
  await expect(page.getByText("Workers saved.")).toBeVisible();
  await expect(page.getByLabel("Name")).toHaveCount(0);
});
