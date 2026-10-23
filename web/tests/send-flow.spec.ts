import {test, expect, type Page, type Route} from "@playwright/test";
import {readFileSync} from "node:fs";
type Msg = {id:string;direction:string;text:string;at:string;origin?:string;status?:string};
async function prepare(page: Page) {
  const world = {
    messages: [] as Msg[],
    job: null as null | {id:string;conversation_id:string;command:string;state:string;created_at:string;transaction_id?:string;code?:string},
    composeGate: null as null | Promise<void>,
    composeCalls: [] as {text:string;request_id:string}[],
  };
  await page.route("**/api/v1/state", async route => {
    const response = await route.fetch();
    if (!response.ok()) return route.fulfill({response});
    await route.fulfill({json: {...await response.json(), paused: false, transport_ready: true}});
  });
  await page.route("**/api/v1/inbox/settings", async route => {
    const response = await route.fetch();
    await route.fulfill({json: {...await response.json(), enabled: true, scope: "all", archive_retention_days: 0}});
  });
  await page.route(/\/api\/v1\/inbox\/conversations(\?refresh=1)?$/, route => route.fulfill({json: {conversations: [{id: "a", label: "Send chat", participant_id: "peer-a", protocol: "RCS", writable: true, archived: true, enrolled: false, last_message_at: new Date().toISOString()}], next_cursor: ""}}));
  await page.route("**/api/v1/inbox/conversations/a/history?*", route => route.fulfill({json: {messages: world.messages, next_cursor: ""}}));
  await page.route("**/api/v1/activity", route => route.fulfill({json: world.job ? [world.job] : []}));
  await page.route("**/api/v1/inbox/conversations/a/compose", async (route: Route) => {
    world.composeCalls.push(route.request().postDataJSON());
    if (world.composeGate) await world.composeGate;
    await route.fulfill({status: 202, json: {id: "job-1", queued: true}});
  });
  const descriptor = JSON.parse(readFileSync(".test-state/connection.json", "utf8"));
  await page.goto("/");
  await page.getByLabel("Owner credential").fill(readFileSync(descriptor.owner, "utf8"));
  await page.getByRole("button", {name: "Sign in", exact: true}).click();
  await page.getByRole("button", {name: "Conversations", exact: true}).click();
  await page.locator(".conversation").getByRole("button", {name: /Send chat/}).click();
  await expect(page.getByLabel("Message", {exact: true})).toBeVisible();
  return world;
}
const job = (state: string, extra = {}) => ({id: "job-1", conversation_id: "a", command: "manual", state, created_at: new Date().toISOString(), ...extra});

test("sending clears the box at once and follows sending, sent and saved states", async ({page}) => {
  const world = await prepare(page);
  let release!: () => void;
  world.composeGate = new Promise<void>(resolve => {release = resolve;});
  const box = page.getByLabel("Message", {exact: true});
  await box.fill("Synthetic instant text");
  await page.getByRole("button", {name: "Send message", exact: true}).click();
  await expect(box).toHaveValue("");
  const bubble = page.locator(".bubble.pending");
  await expect(bubble).toContainText("Synthetic instant text");
  await expect(bubble).toContainText("Sending");
  release();
  world.job = job("submitting");
  await expect(bubble).toContainText("Sending");
  world.messages = [{id: "txn-1", direction: "outgoing", text: "Synthetic instant text", at: new Date().toISOString(), origin: "web", status: "submitted"}];
  world.job = job("submitted", {transaction_id: "txn-1"});
  await expect(page.locator(".bubble.pending")).toHaveCount(0, {timeout: 10000});
  const saved = page.locator(".bubble.outgoing").filter({hasText: "Synthetic instant text"});
  await expect(saved).toHaveCount(1);
  await expect(saved.locator(".msg-status")).toHaveText("Sent");
  world.messages = [{...world.messages[0], status: "delivered"}];
  await expect(saved.locator(".msg-status")).toHaveText("Delivered", {timeout: 10000});
  world.messages = [{...world.messages[0], status: "read"}];
  await expect(saved.locator(".msg-status")).toHaveText("Read", {timeout: 10000});
});

test("a definitively failed send can be edited and resent", async ({page}) => {
  const world = await prepare(page);
  const box = page.getByLabel("Message", {exact: true});
  await box.fill("Synthetic failing text");
  await page.getByRole("button", {name: "Send message", exact: true}).click();
  world.job = job("failed", {code: "not_submitted"});
  const bubble = page.locator(".bubble.pending");
  await expect(bubble.getByRole("alert")).toContainText("Not sent", {timeout: 10000});
  await bubble.getByRole("button", {name: "Edit and resend"}).click();
  await expect(box).toHaveValue("Synthetic failing text");
  await expect(page.locator(".bubble.pending")).toHaveCount(0);
});

test("an uncertain send never offers retry or edit", async ({page}) => {
  const world = await prepare(page);
  await page.getByLabel("Message", {exact: true}).fill("Synthetic uncertain text");
  await page.getByRole("button", {name: "Send message", exact: true}).click();
  world.job = job("unknown_submission");
  const bubble = page.locator(".bubble.pending");
  await expect(bubble.getByRole("alert")).toContainText("Not confirmed", {timeout: 10000});
  await expect(bubble.getByRole("button", {name: "Retry sending"})).toHaveCount(0);
  await expect(bubble.getByRole("button", {name: "Edit and resend"})).toHaveCount(0);
  await bubble.getByRole("button", {name: "Dismiss"}).click();
  await expect(page.locator(".bubble.pending")).toHaveCount(0);
});

test("messages sent in quick succession post in order with distinct keys", async ({page}) => {
  const world = await prepare(page);
  const box = page.getByLabel("Message", {exact: true});
  for (const text of ["Synthetic first", "Synthetic second"]) {
    await box.fill(text);
    await page.getByRole("button", {name: "Send message", exact: true}).click();
    await expect(box).toHaveValue("");
  }
  await expect.poll(() => world.composeCalls.map(c => c.text)).toEqual(["Synthetic first", "Synthetic second"]);
  expect(new Set(world.composeCalls.map(c => c.request_id)).size).toBe(2);
});

test("pasted images show a thumbnail before sending", async ({page}) => {
  await prepare(page);
  const png = Buffer.from("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==", "base64");
  await page.getByLabel("Choose files", {exact: true}).setInputFiles({name: "synthetic.png", mimeType: "image/png", buffer: png});
  const chip = page.locator(".selected-attachment.has-thumb");
  await expect(chip.locator("img")).toBeVisible();
  const box = await chip.boundingBox();
  expect(box?.width ?? 0).toBeGreaterThan(100);
  await expect(page.getByRole("button", {name: "Remove synthetic.png", exact: true})).toBeVisible();
});

test("a double click or held Enter sends exactly once", async ({page}) => {
  const world = await prepare(page);
  const box = page.getByLabel("Message", {exact: true});
  await box.fill("Synthetic once only");
  await page.getByRole("button", {name: "Send message", exact: true}).evaluate(button => {
    (button as HTMLButtonElement).click();
    (button as HTMLButtonElement).click();
  });
  await expect(box).toHaveValue("");
  await box.fill("Synthetic held enter");
  await box.evaluate(el => {
    for (const repeat of [false, true, true])
      el.dispatchEvent(new KeyboardEvent("keydown", {key: "Enter", bubbles: true, cancelable: true, repeat}));
  });
  await expect.poll(() => world.composeCalls.map(c => c.text)).toEqual(["Synthetic once only", "Synthetic held enter"]);
  await page.waitForTimeout(500);
  expect(world.composeCalls).toHaveLength(2);
});

test("a send whose job never appears stops saying Sending and asks the owner to check", async ({page}) => {
  await page.clock.install();
  const world = await prepare(page);
  await page.getByLabel("Message", {exact: true}).fill("Synthetic lost job");
  await page.getByRole("button", {name: "Send message", exact: true}).click();
  await expect.poll(() => world.composeCalls.length).toBe(1);
  await expect(page.locator(".bubble.pending")).toContainText("Sending");
  await page.clock.runFor(35000);
  const bubble = page.locator(".bubble.pending");
  await expect(bubble.getByRole("alert")).toContainText("Not confirmed");
  await expect(bubble.getByRole("button", {name: "Retry sending"})).toHaveCount(0);
  await bubble.getByRole("button", {name: "Dismiss"}).click();
  await expect(page.locator(".bubble.pending")).toHaveCount(0);
});

test("a pending send is still followed while another page is open", async ({page}) => {
  await page.clock.install();
  const world = await prepare(page);
  // Timers advance only when driven, so the follower cannot race the driven poll.
  await page.clock.pauseAt(await page.evaluate(() => Date.now() + 1000));
  await page.getByLabel("Message", {exact: true}).fill("Synthetic followed text");
  await page.getByRole("button", {name: "Send message", exact: true}).click();
  await expect.poll(() => world.composeCalls.length).toBe(1);
  await page.getByRole("button", {name: "Home", exact: true}).click();
  world.messages = [{id: "txn-9", direction: "outgoing", text: "Synthetic followed text", at: new Date().toISOString(), origin: "web", status: "submitted"}];
  world.job = job("submitted", {transaction_id: "txn-9"});
  // The follower polls the job while Home is open; run its turn instead of waiting.
  const followed = page.waitForResponse(async response => new URL(response.url()).pathname === "/api/v1/activity" && (await response.text()).includes('"submitted"'));
  await page.clock.runFor(1000);
  await followed;
  await page.getByRole("button", {name: "Conversations", exact: true}).click();
  await page.locator(".conversation").getByRole("button", {name: /Send chat/}).click();
  await expect(page.locator(".bubble.outgoing").filter({hasText: "Synthetic followed text"})).toHaveCount(1);
  await expect(page.locator(".bubble.pending")).toHaveCount(0);
});
