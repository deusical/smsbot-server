import { test, expect, type Page, signIn } from "./fixtures";

async function openDevices(page: Page) {
  await signIn(page);
  await page.getByRole("navigation", { name: "Main navigation" }).getByRole("button", { name: "Settings" }).click();
  await page.getByRole("navigation", { name: "Settings sections" }).getByRole("button", { name: "Devices" }).click();
  return page.locator(".facts");
}

test("Devices lists the phone's SIMs with only the number ending", async ({ page }) => {
  const facts = await openDevices(page);
  await expect(facts).toContainText("SIM 1 · Demo carrier · number ending 0100");
  await expect(page.getByText("Your phone has more than one SIM")).toHaveCount(0);
  await expect(page.getByText("Moved your number to a different phone?")).toBeVisible();
});

test("Devices asks for a default SIM when the phone has two", async ({ page }) => {
  await page.route("**/api/v1/state", async (route) => {
    const response = await route.fetch();
    if (!response.ok()) return route.fulfill({ response });
    await route.fulfill({ json: { ...(await response.json()), phone_sims: [{ slot: 1, carrier: "Synthetic A", number_ending: "1111" }, { slot: 2, carrier: "Synthetic B" }] } });
  });
  const facts = await openDevices(page);
  await expect(facts).toContainText("SIM 1 · Synthetic A · number ending 1111");
  await expect(facts).toContainText("SIM 2 · Synthetic B");
  await expect(page.getByText("Your phone has more than one SIM")).toBeVisible();
});
