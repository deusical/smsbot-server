import { test, expect, type Page } from "@playwright/test";
import { readFileSync } from "node:fs";
test("older pages preserve the viewport and new messages do not jump the reader", async ({
  page,
}) => {
  await login(page);
  await configure(page, true);
  let latest = false;
  const messages = Array.from({ length: 100 }, (_, i) => ({
    id: `synthetic-row-${i}`,
    direction: i % 2 ? "outgoing" : "incoming",
    origin: "phone",
    at: new Date(Date.UTC(2026, 9, 13, 12, 0, i)).toISOString(),
    text: `Synthetic scroll message ${i} with enough text for a chat bubble.`,
  }));
  await page.route(
    "**/api/v1/inbox/conversations/demo-chat/history?*",
    (route) => {
      const older = new URL(route.request().url()).searchParams.has("cursor");
      return route.fulfill({
        json: {
          messages: older
            ? messages.slice(0, 50)
            : [
                ...messages.slice(50),
                ...(latest
                  ? [
                      {
                        ...messages[99],
                        id: "synthetic-new-row",
                        text: "Synthetic new live message",
                        at: new Date(Date.UTC(2026, 9, 13, 13)).toISOString(),
                      },
                    ]
                  : []),
              ],
          next_cursor: older ? "" : "synthetic-older",
        },
      });
    },
  );
  try {
    await page
      .getByRole("button", { name: "Conversations", exact: true })
      .click();
    await page
      .locator(".conversation")
      .filter({ hasText: "Demo conversation" })
      .getByRole("button", { name: /Demo conversation/ })
      .click();
    const scroller = page.locator(".chat-messages");
    await expect(scroller.locator(".bubble")).toHaveCount(50);
    await scroller.evaluate((el) => {
      el.scrollTop = 400;
    });
    const before = await scroller.evaluate(
      (el) => el.scrollHeight - el.scrollTop,
    );
    await page
      .getByRole("button", { name: "Load older messages", exact: true })
      .click();
    await expect(scroller.locator(".bubble")).toHaveCount(100);
    const after = await scroller.evaluate(
      (el) => el.scrollHeight - el.scrollTop,
    );
    expect(Math.abs(before - after)).toBeLessThan(3);
    latest = true;
    await expect(
      page.getByRole("button", { name: "New messages ↓", exact: true }),
    ).toBeVisible();
    const top = await scroller.evaluate((el) => el.scrollTop);
    expect(top).toBeGreaterThan(400);
    await page
      .getByRole("button", { name: "New messages ↓", exact: true })
      .click();
    expect(
      await scroller.evaluate(
        (el) => el.scrollHeight - el.scrollTop - el.clientHeight,
      ),
    ).toBeLessThan(3);
  } finally {
    await configure(page, false);
  }
});
const headers = { Origin: "http://127.0.0.1:8788" };
async function login(page: Page) {
  const c = JSON.parse(readFileSync(".test-state/connection.json", "utf8"));
  await page.goto("/");
  await page.getByLabel("Owner credential").fill(readFileSync(c.owner, "utf8"));
  await page.getByRole("button", { name: "Open dashboard" }).click();
  await expect(
    page.getByRole("heading", { name: "Home", exact: true }),
  ).toBeVisible();
}
async function configure(page: Page, enabled: boolean) {
  const s = await (await page.request.get("/api/v1/inbox/settings")).json();
  expect(
    (
      await page.request.put("/api/v1/inbox/settings", {
        headers,
        data: {
          ...s,
          enabled,
          load_older: enabled,
          automatic_replies: !enabled,
        },
      })
    ).ok(),
  ).toBeTruthy();
  await page.request.post("/api/v1/pause", { headers, data: { paused: true } });
}
test("owner captures ordinary text while paused and sends manually without automation", async ({
  page,
}) => {
  await login(page);
  await configure(page, true);
  const originalConversations = await (
    await page.request.get("/api/v1/conversations")
  ).json();
  const wasEnrolled = originalConversations.find(
    (c: { id: string; enrolled: boolean }) => c.id === "demo-chat",
  )?.enrolled;
  await page.request.delete("/api/v1/enrollment/demo-chat", { headers });
  try {
    await page.request.post("/api/v1/fixture/incoming", {
      headers,
      data: {
        id: "synthetic-chat-message",
        conversation_id: "demo-chat",
        participant_id: "demo-peer",
        text: "Synthetic conversation text",
      },
    });
    await page
      .getByRole("button", { name: "Conversations", exact: true })
      .click();
    await page
      .locator(".conversation")
      .filter({ hasText: "Demo conversation" })
      .getByRole("button", { name: /Demo conversation/ })
      .click();
    await expect(
      page
        .locator(".bubble.incoming")
        .filter({ hasText: "Synthetic conversation text" }),
    ).toBeVisible();
    await expect(
      page.getByRole("button", { name: "Send message", exact: true }),
    ).toBeDisabled();
    await page
      .getByRole("button", { name: /Enable bot|Resume sending/, exact: true })
      .click();
    await page.getByLabel("Manual message").fill("Synthetic web reply");
    await page
      .getByRole("button", { name: "Send message", exact: true })
      .click();
    await page
      .getByRole("button", { name: "Confirm send", exact: true })
      .click();
    await expect(
      page
        .locator(".bubble.outgoing")
        .filter({ hasText: "Synthetic web reply" }),
    ).toBeVisible();
    await expect(
      page
        .locator(".bubble.outgoing")
        .filter({ hasText: "Synthetic web reply" }),
    ).toContainText("You · Web");
    await page.evaluate(() => window.scrollTo(0, 0));
    await page.screenshot({
      path: "../docs/images/server-conversation-inbox.png",
      fullPage: true,
    });
  } finally {
    await configure(page, false);
    if (wasEnrolled)
      await page.request.post("/api/v1/enrollment", {
        headers,
        data: { conversation_id: "demo-chat", participant_id: "demo-peer" },
      });
  }
});
test("conversation preferences save explicitly and survive polling", async ({
  page,
}) => {
  await login(page);
  await configure(page, false);
  await page.reload();
  await expect(
    page.getByRole("heading", { name: "Home", exact: true }),
  ).toBeVisible();
  await page.getByRole("button", { name: "Settings", exact: true }).click();
  await page
    .getByLabel("Show all conversation messages", { exact: true })
    .check();
  await page
    .getByLabel("Load older phone history when scrolling up", { exact: true })
    .check();
  await page.getByLabel("Automatic command replies", { exact: true }).uncheck();
  await page
    .getByRole("button", { name: "Save conversation settings", exact: true })
    .click();
  await expect(
    page.getByRole("button", {
      name: "Save conversation settings",
      exact: true,
    }),
  ).toBeDisabled();
  const s = await (await page.request.get("/api/v1/inbox/settings")).json();
  expect(s.enabled).toBe(true);
  expect(s.load_older).toBe(true);
  expect(s.automatic_replies).toBe(false);
  await configure(page, false);
});
test("scrolling older history preserves messages and never routes imported commands", async ({
  page,
}) => {
  await login(page);
  await configure(page, true);
  try {
    await page
      .getByRole("button", { name: "Conversations", exact: true })
      .click();
    await page
      .locator(".conversation")
      .filter({ hasText: "Demo conversation" })
      .getByRole("button", { name: /Demo conversation/ })
      .click();
    await page
      .getByRole("button", { name: "Load older messages", exact: true })
      .click();
    await expect(
      page
        .locator(".bubble.incoming")
        .filter({ hasText: "Historical synthetic input must never execute" }),
    ).toBeVisible();
    await expect(
      page.getByText(
        "No more available phone text. This does not establish complete history.",
      ),
    ).toBeVisible();
  } finally {
    await configure(page, false);
  }
});

test("lost compose acknowledgement retries the same intent without a second reply", async ({
  page,
}) => {
  await login(page);
  await configure(page, true);
  const ids: string[] = [];
  await page.route(
    "**/api/v1/inbox/conversations/demo-chat/compose",
    async (route) => {
      ids.push(route.request().postDataJSON().request_id);
      const response = await route.fetch();
      expect(response.ok()).toBeTruthy();
      if (ids.length === 1) await route.abort("failed");
      else await route.fulfill({ response });
    },
  );
  try {
    await page
      .getByRole("button", { name: "Conversations", exact: true })
      .click();
    await page
      .locator(".conversation")
      .filter({ hasText: "Demo conversation" })
      .getByRole("button", { name: /Demo conversation/ })
      .click();
    await page
      .getByRole("button", { name: "Resume sending", exact: true })
      .click();
    const message = `Synthetic retry ${Date.now()}`;
    await page.getByLabel("Manual message").fill(message);
    await page
      .getByRole("button", { name: "Send message", exact: true })
      .click();
    await page
      .getByRole("button", { name: "Confirm send", exact: true })
      .click();
    await expect(
      page.getByRole("button", { name: "Send message", exact: true }),
    ).toBeEnabled();
    await expect(page.getByLabel("Manual message")).toHaveValue(message);
    await page
      .getByRole("button", { name: "Send message", exact: true })
      .click();
    await page
      .getByRole("button", { name: "Confirm send", exact: true })
      .click();
    await expect(page.getByLabel("Manual message")).toHaveValue("");
    expect(ids).toHaveLength(2);
    expect(ids[1]).toBe(ids[0]);
    await expect(
      page.locator(".bubble.outgoing").filter({ hasText: message }),
    ).toHaveCount(1);
  } finally {
    await configure(page, false);
  }
});

test("settings conflict can reload the saved revision without reloading the dashboard", async ({
  page,
}) => {
  await login(page);
  await configure(page, false);
  await page.reload();
  await page.getByRole("button", { name: "Settings", exact: true }).click();
  await page
    .getByLabel("Show all conversation messages", { exact: true })
    .check();
  // Another owner session wins the revision while this draft stays open.
  await configure(page, false);
  const settings = await (
    await page.request.get("/api/v1/inbox/settings")
  ).json();
  await page.waitForResponse(
    async (response) =>
      response.url().endsWith("/api/v1/inbox/settings") &&
      response.request().method() === "GET" &&
      (await response.json()).revision === settings.revision,
  );
  await page
    .getByRole("button", { name: "Save conversation settings", exact: true })
    .click();
  await expect(
    page.getByRole("alert").filter({ hasText: "settings changed reload" }),
  ).toBeVisible();
  await expect(
    page.getByLabel("Show all conversation messages", { exact: true }),
  ).toBeChecked();
  await page
    .getByRole("button", {
      name: "Reload saved conversation settings",
      exact: true,
    })
    .click();
  await expect(
    page.getByLabel("Show all conversation messages", { exact: true }),
  ).not.toBeChecked();
  await page
    .getByLabel("Show all conversation messages", { exact: true })
    .check();
  await page
    .getByRole("button", { name: "Save conversation settings", exact: true })
    .click();
  await expect(
    page.getByRole("button", {
      name: "Save conversation settings",
      exact: true,
    }),
  ).toBeDisabled();
  expect(
    (await (await page.request.get("/api/v1/inbox/settings")).json()).enabled,
  ).toBe(true);
  await configure(page, false);
});

test("background history polling queues an older-page click and keeps pause usable", async ({
  page,
}) => {
  await login(page);
  await configure(page, true);
  let release!: () => void;
  const held = new Promise<void>((resolve) => (release = resolve));
  let pollStarted!: () => void;
  const started = new Promise<void>((resolve) => (pollStarted = resolve));
  let first = true;
  let olderRequests = 0;
  await page.route(
    "**/api/v1/inbox/conversations/demo-chat/history?*",
    async (route) => {
      const older = new URL(route.request().url()).searchParams.has("cursor");
      if (older) olderRequests++;
      else if (!first) {
        pollStarted();
        await held;
      }
      first = false;
      await route.fulfill({
        json: {
          messages: [
            {
              id: older ? "synthetic-old" : "synthetic-current",
              direction: "incoming",
              at: older ? "2026-10-13T10:00:00Z" : "2026-10-13T11:00:00Z",
              text: older ? "Synthetic older row" : "Synthetic current row",
            },
          ],
          next_cursor: older ? "" : "synthetic-cursor",
        },
      });
    },
  );
  try {
    await page
      .getByRole("button", { name: "Conversations", exact: true })
      .click();
    await page
      .locator(".conversation")
      .filter({ hasText: "Demo conversation" })
      .getByRole("button", { name: /Demo conversation/ })
      .click();
    await expect(page.locator(".bubble")).toHaveCount(1);
    await started;
    const older = page.getByRole("button", {
      name: "Load older messages",
      exact: true,
    });
    await expect(older).toBeEnabled();
    await older.click();
    await expect(older).toBeDisabled();
    await expect(
      page.getByRole("button", { name: "Resume sending", exact: true }),
    ).toBeEnabled();
    release();
    await expect(page.locator(".bubble")).toHaveCount(2);
    expect(olderRequests).toBe(1);
  } finally {
    release();
    await configure(page, false);
  }
});
