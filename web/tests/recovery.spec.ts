import {test, expect} from "@playwright/test";
import {readFileSync} from "node:fs";

test("reconnection preserves Live and owner pause remains authoritative", async ({page}) => {
 let paused = false, ready = false, connection = "reconnecting";
 await page.route("**/api/v1/state", async route => {
  const response = await route.fetch();
  if (!response.ok()) { await route.fulfill({response}); return; }
  await route.fulfill({json:{...await response.json(), paused, transport_ready:ready, connection_state:connection}});
 });
 await page.route("**/api/v1/pause", async route => {
  paused = route.request().postDataJSON().paused;
  const response = await page.request.get("/api/v1/state");
  await route.fulfill({json:{...await response.json(), paused, transport_ready:ready, connection_state:connection}});
 });
 const descriptor = JSON.parse(readFileSync(".test-state/connection.json", "utf8"));
 await page.goto("/");
 await page.getByLabel("Owner credential").fill(readFileSync(descriptor.owner,"utf8"));
 await page.getByRole("button",{name:"Sign in",exact:true}).click();
 await expect(page.locator(".link-state")).toHaveText("Reconnecting to phone");
 await expect(page.locator(".hero-pill")).toHaveText("LIVE");
 await expect(page.locator(".hero-line")).toContainText("will resume after verification");
 await page.getByRole("button",{name:"Pause sending",exact:true}).click();
 await expect(page.locator(".hero-pill")).toHaveText("PAUSED");
 await expect(page.locator(".hero-line")).toContainText("stay paused");
 ready=true;connection="connected";
 await expect(page.locator(".link-state")).toHaveText("Phone connected");
 await expect(page.locator(".hero-pill")).toHaveText("PAUSED");
 paused=false;
 await expect(page.locator(".hero-pill")).toHaveText("LIVE");
 ready=false;connection="session_conflict";paused=true;
 await expect(page.locator(".link-state")).toHaveText("Messages session conflict");
 await expect(page.locator(".hero-line")).toContainText("Sending is blocked");
});
