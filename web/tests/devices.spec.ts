import { test, expect, type Page, signIn, nextPoll } from "./fixtures";

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
  await expect(page.getByRole("button", { name: "Replace phone" })).toHaveCount(0);
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


test("Devices guides phone replacement through approval, cancellation and retry", async ({ page }) => {
  await page.route("**/api/v1/state", async route => {
    const response = await route.fetch();
    if (!response.ok()) return route.fulfill({ response });
    await route.fulfill({ json: { ...(await response.json()), fixture: false, paused: true } });
  });
  let status = { state: "idle", emoji: "", maintenance: false };
  const inputs: string[] = [];
  await page.route("**/api/v1/phone-pairing", async route => {
    const method = route.request().method();
    if (method === "POST") {
      inputs.push(route.request().postDataJSON().cookies);
      status = { state: "pairing", emoji: "synthetic-emoji", maintenance: true };
      await route.fulfill({ status: 202, json: { state: "pairing" } });
    } else {
      if (method === "DELETE") status = { state: "failed", emoji: "", maintenance: false };
      await route.fulfill({ json: status });
    }
  });
  await openDevices(page);
  await page.getByRole("button", { name: "Replace phone", exact: true }).click();
  const input = page.getByRole("textbox", { name: "Copied Google request" });
  await input.fill("synthetic copied request");
  await page.getByRole("button", { name: "Pause and pair new phone" }).click();
  await expect(input).toHaveCount(0);
  await nextPoll(page);
  await expect(page.getByRole("status").filter({ hasText: "synthetic-emoji" })).toBeVisible();
  await page.getByRole("button", { name: "Cancel pairing" }).click();
  await nextPoll(page);
  await expect(input).toHaveValue("");
  await expect(page.getByRole("alert").filter({ hasText: "The service reconnected and remains paused" })).toBeVisible();
  await input.fill("synthetic retry request");
  await page.getByRole("button", { name: "Pause and pair new phone" }).click();
  await expect(page.getByRole("button", { name: "Cancel pairing" })).toBeVisible();
  status = { state: "complete", emoji: "", maintenance: false };
  await nextPoll(page);
  await expect(page.getByRole("status").filter({ hasText: "Phone paired" })).toContainText("Automation stays paused");
  await expect(page.getByRole("button", { name: "Refresh devices" })).toBeVisible();
  expect(inputs).toEqual(["synthetic copied request", "synthetic retry request"]);
});
