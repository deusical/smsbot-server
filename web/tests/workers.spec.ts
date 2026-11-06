import { test, expect, signIn } from "./fixtures";

test("Workers are added, saved with a stored token, refused off Tailscale and removed", async ({ page }) => {
  await signIn(page);
  await page.getByRole("navigation", { name: "Main navigation" }).getByRole("button", { name: "Settings" }).click();
  await page.getByRole("navigation", { name: "Settings sections" }).getByRole("button", { name: "Workers" }).click();
  await expect(page.getByRole("heading", { name: "Workers" })).toBeVisible();

  await page.getByRole("button", { name: "Add worker" }).click();
  await page.getByLabel("Name").fill("laptop");
  await page.getByLabel("Address").fill("http://192.168.1.20:8767/download");
  await page.getByRole("button", { name: "Save workers" }).click();
  await expect(page.getByRole("alert")).toContainText("Tailscale");

  await page.getByLabel("Address").fill("http://100.92.5.20:8767/download");
  await page.getByLabel("Token").fill("synthetic-worker-token-for-offline-tests-only");
  await page.getByRole("button", { name: "Save workers" }).click();
  await expect(page.getByText("Workers saved.")).toBeVisible();
  // Saved, with its token stored but never shown; nothing answers there.
  await expect(page.getByLabel("Token")).toHaveValue("");
  await expect(page.getByLabel("Token")).toHaveAttribute("placeholder", "Stored");
  await expect(page.getByText("Offline")).toBeVisible();

  await page.getByRole("button", { name: "Remove" }).click();
  await page.getByRole("button", { name: "Save workers" }).click();
  await expect(page.getByText("Workers saved.")).toBeVisible();
  await expect(page.getByLabel("Name")).toHaveCount(0);
});
