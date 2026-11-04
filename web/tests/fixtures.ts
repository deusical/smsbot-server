import {test as base, expect, type Page} from "@playwright/test";
import {readFileSync} from "node:fs";
import {connectionFile, origin} from "./server";
export * from "@playwright/test";

// Every context gets Playwright's fake clock before any page loads. It runs at
// normal speed, so a test that never touches it behaves as with real time, but
// a test that waits for the dashboard to poll can say so and skip the wait:
//
//   await nextPoll(page);   // instead of sitting through a 3 s polling interval
//
// Tests may still install the clock themselves (for a fixed start time); doing so
// resets it.
export const test = base.extend({
  context: async ({context}, use) => {
    await context.clock.install();
    await use(context);
    // A poll may still be in a route handler when the test ends; detach handlers
    // so its disposed response is not reported against the finished test.
    await Promise.all(context.pages().map(p => p.unrouteAll({behavior: "ignoreErrors"})));
    await context.unrouteAll({behavior: "ignoreErrors"});
  },
});

/** The dashboard's polling interval for state, conversations and history. */
export const pollInterval = 3000;

/**
 * Advances page time by one polling interval, firing every timer due in it, so
 * the next poll starts now. Server-side work is unaffected; follow it with the
 * usual `expect`, which waits for the response.
 */
export async function nextPoll(page: Page, ms = pollInterval) {
  await page.clock.runFor(ms);
}

/**
 * For checks that something did NOT happen during a poll: advances one interval,
 * then gives any request the poll started a moment of real time to reach a route
 * handler. Asserting right after `nextPoll` alone could pass before a wrong
 * request arrived.
 */
export async function pollAndSettle(page: Page, ms = pollInterval) {
  await nextPoll(page, ms);
  await page.waitForTimeout(300);
}

/**
 * For UI that shows server work which may still be finishing when the next poll
 * goes out (a bot reply, an export): advances one interval per attempt until
 * `check` passes. Give `check` a short timeout so each attempt is quick.
 */
export async function pollUntil(page: Page, check: () => Promise<unknown>, timeout = 15000) {
  await expect(async () => {
    await nextPoll(page);
    await check();
  }).toPass({timeout, intervals: [100]});
}

/** The owner credential the fixture server for this worker was set up with. */
export function ownerToken() {
  return readFileSync(JSON.parse(readFileSync(connectionFile, "utf8")).owner, "utf8");
}

/**
 * Signs in through the API and opens `path`. The sign-in form itself is covered
 * by mobile.spec.ts and routes.spec.ts; other tests skip typing into it.
 */
export async function signIn(page: Page, path = "/") {
  const response = await page.request.post("/api/v1/login", {headers: {Origin: origin}, data: {token: ownerToken()}});
  expect(response.ok(), "owner sign-in").toBeTruthy();
  await page.goto(path);
}
