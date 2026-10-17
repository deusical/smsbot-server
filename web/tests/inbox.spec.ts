import { test, expect, type Page } from "@playwright/test";
import { readFileSync } from "node:fs";

test.use({trace: "retain-on-failure"});
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
    const scroller = page.locator(".messages");
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
  await page.getByRole("button", { name: "Sign in" }).click();
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

// Retained history may contain many local pages before phone imports begin.
async function loadAvailableHistory(page: Page) {
  await expect(async () => {
    const end = page.locator(".history-end");
    if (!(await end.isVisible())) {
      const older = page.getByRole("button", {name: "Load older messages", exact: true});
      if (await older.isVisible() && await older.isEnabled()) await older.click();
    }
    await expect(end).toBeVisible({timeout: 500});
  }).toPass({timeout: 15000, intervals: [250]});
  await page.locator(".messages").evaluate(el => {el.scrollTop = 0});
}

test("attachment limits save as Unlimited and permit more than four files", async ({
  page,
}) => {
  await login(page);
  const original = await (
    await page.request.get("/api/v1/inbox/settings")
  ).json();
  await configure(page, true);
  await page.reload();
  try {
    await page.getByRole("button", { name: "Settings", exact: true }).click();
    await page
      .getByRole("button", { name: "Messages & privacy", exact: true })
      .click();
    for (const label of [
      "Files per message",
      "Per-file limit (MB)",
      "Combined file limit (MB)",
    ]) {
      await page
        .getByRole("button", { name: `Unlimited ${label}`, exact: true })
        .click();
      await expect(page.getByLabel(label, { exact: true })).toHaveValue("0");
    }
    await page
      .getByRole("button", { name: "Save changes", exact: true })
      .click();
    await expect
      .poll(
        async () =>
          (await (await page.request.get("/api/v1/inbox/settings")).json())
            .attachment_limits,
      )
      .toEqual({ max_files: 0, max_file_mb: 0, max_total_mb: 0 });
    await page.screenshot({ path: "../.build-temp/attachment-limits.png", fullPage: true });
    await page.reload();
    await page
      .getByRole("button", { name: "Conversations", exact: true })
      .click();
    await page
      .locator(".conversation")
      .filter({ hasText: "Demo conversation" })
      .getByRole("button", { name: /Demo conversation/ })
      .click();
    await page
      .getByLabel("Choose files", { exact: true })
      .setInputFiles(
        Array.from({ length: 5 }, (_, i) => ({
          name: `synthetic-unlimited-${i}.txt`,
          mimeType: "text/plain",
          buffer: Buffer.from("Synthetic file"),
        })),
      );
    await expect(
      page.getByRole("button", { name: /Remove synthetic-unlimited-/ }),
    ).toHaveCount(5);
  } finally {
    const current = await (
      await page.request.get("/api/v1/inbox/settings")
    ).json();
    await page.request.put("/api/v1/inbox/settings", {
      headers,
      data: { ...current, attachment_limits: original.attachment_limits },
    });
    await configure(page, false);
  }
});

test("conversation files show incoming images, download documents and recover historical files", async ({
  page,
}) => {
  await login(page);
  await configure(page, true);
  const png = Buffer.from(
    "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+aM1sAAAAASUVORK5CYII=",
    "base64",
  );
  try {
    const received = await page.request.post("/api/v1/fixture/incoming", {
      headers,
      data: {
        id: `synthetic-file-${Date.now()}`,
        conversation_id: "demo-chat",
        participant_id: "demo-peer",
        text: "Synthetic file caption",
        attachments: [
          {
            name: "synthetic-image.png",
            mime: "image/png",
            data: png.toString("base64"),
          },
          {
            name: "synthetic-document.pdf",
            mime: "application/pdf",
            data: Buffer.from("%PDF-1.4\nSynthetic document").toString(
              "base64",
            ),
          },
        ],
      },
    });
    expect(received.ok()).toBeTruthy();
    await page
      .getByRole("button", { name: "Conversations", exact: true })
      .click();
    await page
      .locator(".conversation")
      .filter({ hasText: "Demo conversation" })
      .getByRole("button", { name: /Demo conversation/ })
      .click();
    const bubble = page
      .locator(".bubble.incoming")
      .filter({ hasText: "Synthetic file caption" });
    const image = bubble.getByRole("img", { name: "synthetic-image.png" });
    await expect(image).toBeVisible();
    await expect
      .poll(() => image.evaluate((el: HTMLImageElement) => el.naturalWidth))
      .toBe(1);
    const downloadEvent = page.waitForEvent("download");
    await bubble
      .getByRole("button", {
        name: "Download synthetic-document.pdf",
        exact: true,
      })
      .click();
    const download = await downloadEvent;
    expect(download.suggestedFilename()).toBe("synthetic-document.pdf");
    expect(readFileSync((await download.path())!, "utf8")).toBe(
      "%PDF-1.4\nSynthetic document",
    );
    await loadAvailableHistory(page);
    await expect(
      page.getByRole("button", {
        name: "Download synthetic-history.txt",
        exact: true,
      }),
    ).toBeVisible();
    await page.setViewportSize({ width: 390, height: 844 });
    await page.screenshot({
      path: "../.build-temp/conversation-files-mobile.png",
      fullPage: true,
    });
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= window.innerWidth,
      ),
    ).toBeTruthy();
    await page.setViewportSize({ width: 1280, height: 900 });
    await page.screenshot({
      path: "../.build-temp/conversation-files-desktop.png",
      fullPage: true,
    });
  } finally {
    await configure(page, false);
  }
});

test("file-only sends retain selected files after lost acknowledgement and queue once", async ({
  page,
}) => {
  await login(page);
  await configure(page, true);
  const ids: string[] = [];
  const name = `synthetic-upload-${Date.now()}.txt`;
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
    await expect(
      page.getByRole("button", { name: "Attach files", exact: true }),
    ).toBeVisible();
    await page.getByLabel("Choose files", { exact: true }).setInputFiles({
      name,
      mimeType: "text/plain",
      buffer: Buffer.from("Synthetic uploaded file"),
    });
    await expect(
      page.getByRole("button", { name: `Remove ${name}`, exact: true }),
    ).toBeVisible();
    await expect(
      page.getByRole("button", { name: "Send message", exact: true }),
    ).toBeDisabled();
    await page.getByRole("switch", { name: "Bot sending" }).click();
    await page
      .getByRole("button", { name: "Send message", exact: true })
      .click();
    // The box and file chip clear at once; the pending bubble holds the message.
    await expect(
      page.getByRole("button", { name: `Remove ${name}`, exact: true }),
    ).toHaveCount(0);
    await expect(
      page.getByRole("alert").filter({ hasText: "did not confirm this message" }),
    ).toBeVisible();
    await page.getByRole("button", { name: "Retry sending", exact: true }).click();
    await expect(
      page.locator(".bubble.outgoing").filter({ hasText: name }),
    ).toHaveCount(1);
    expect(ids).toHaveLength(2);
    expect(ids[0]).toBe(ids[1]);
  } finally {
    await configure(page, false);
  }
});
test("owner captures ordinary text while paused and sends manually without automation", async ({
  page,
}) => {
  await login(page);
  await configure(page, true);
  const originalConversations = (
    await (await page.request.get("/api/v1/inbox/conversations")).json()
  ).conversations;
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
    await page.getByRole("switch", { name: "Bot sending" }).click();
    const reply = `Synthetic web reply ${Date.now()}`;
    await page.getByLabel("Message", { exact: true }).fill(reply);
    await page
      .getByRole("button", { name: "Send message", exact: true })
      .click();
    // The pending bubble appears at once and is replaced by the saved message.
    await expect(
      page.locator(".bubble.outgoing").filter({ hasText: reply }),
    ).toBeVisible();
    await expect(
      page.locator(".bubble.outgoing:not(.pending)").filter({ hasText: reply }),
    ).toContainText("Sent from web", { timeout: 15000 });
    await expect(page.locator(".bubble.pending")).toHaveCount(0);
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
  await page.getByRole("button", { name: "Messages & privacy" }).click();
  await page
    .getByLabel("Show all conversation messages", { exact: true })
    .check();
  await page
    .getByLabel("Load older phone history when scrolling up", { exact: true })
    .check();
  await page.getByLabel("Automatic command replies", { exact: true }).uncheck();
  await page.getByRole("button", { name: "Save changes", exact: true }).click();
  await expect(
    page.getByRole("region", { name: "Unsaved changes" }),
  ).toHaveCount(0);
  const s = await (await page.request.get("/api/v1/inbox/settings")).json();
  expect(s.enabled).toBe(true);
  expect(s.load_older).toBe(true);
  expect(s.automatic_replies).toBe(false);
  await configure(page, false);
});
test("scrolling older history preserves messages and never routes imported commands", async ({
  page,
}) => {
  const historyReads: {path:string;status:number;messages?:number;imported?:number;hasNext?:boolean}[]=[];
  page.on("response", async response => {
    const path = new URL(response.url()).pathname;
    if (!path.endsWith("/history") && !path.endsWith("/history/import")) return;
    const item: typeof historyReads[number] = {path,status:response.status()};
    try {
      const body = await response.json();
      item.messages = Array.isArray(body.messages) ? body.messages.length : undefined;
      item.imported = body.imported;
      item.hasNext = !!body.next_cursor;
    } catch { /* HTTP status remains useful without a JSON response. */ }
    historyReads.push(item);
  });
  await login(page);
  await configure(page, true);
  try {
    // Force history beyond two full local pages, independent of test order.
    for (let i = 0; i < 110; i++) {
      expect((await page.request.post("/api/v1/fixture/incoming", {
        headers, data: {id: `history-pagination-${i}`, conversation_id: "demo-chat", participant_id: "demo-peer", text: `Synthetic pagination row ${i}`},
      })).ok()).toBeTruthy();
    }
    const beforeJobs = (await (await page.request.get("/api/v1/activity")).json()).map((job: {id:string}) => job.id).sort();
    await page.reload();
    await expect(page.getByRole("heading", {name: "Home", exact:true})).toBeVisible();
    await page
      .getByRole("button", { name: "Conversations", exact: true })
      .click();
    await page
      .locator(".conversation")
      .filter({ hasText: "Demo conversation" })
      .getByRole("button", { name: /Demo conversation/ })
      .click();
    // Background sync may populate additional local pages before import.
    try {
      await loadAvailableHistory(page);
    } catch (error) {
      console.log("History requests", JSON.stringify(historyReads));
      console.log("History boundary diagnostic", JSON.stringify(await page.evaluate(() => {
        const elements = [".history-end", ".messages", ".older", ".thread"].map(selector => {
          const el = document.querySelector(selector);
          if (!el) return {selector, missing:true};
          const rect = el.getBoundingClientRect(), style = getComputedStyle(el);
          return {selector, rect:{x:rect.x,y:rect.y,width:rect.width,height:rect.height}, display:style.display, visibility:style.visibility, shrink:style.flexShrink, disabled:el instanceof HTMLButtonElement ? el.disabled : null};
        });
        return {elements, note:document.querySelector(".inline-note")?.textContent, alert:document.querySelector(".inline-alert")?.textContent};
      })));
      throw error;
    }

    // Inspect the historical row only after all local and phone pages are loaded.
    await page.locator(".messages").evaluate(el => {el.scrollTop = 0});
    await expect(page.locator(".bubble.incoming").filter({hasText: "Historical synthetic input must never execute"})).toBeVisible();
    await page.locator(".messages").evaluate(el => {el.scrollTop = el.scrollHeight});
    await expect(page.locator(".messages").getByText("Synthetic pagination row 109", {exact:true})).toBeVisible();
    const afterJobs = (await (await page.request.get("/api/v1/activity")).json()).map((job: {id:string}) => job.id).sort();
    expect(afterJobs).toEqual(beforeJobs);
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
    // Wait until the thread uses the inbox compose path (it offers phone history).
    await expect(
      page.getByRole("button", { name: "Load older messages", exact: true }),
    ).toBeVisible();
    await page.getByRole("switch", { name: "Bot sending" }).click();
    const message = `Synthetic retry ${Date.now()}`;
    const box = page.getByLabel("Message", { exact: true });
    await box.fill(message);
    await page
      .getByRole("button", { name: "Send message", exact: true })
      .click();
    await expect(box).toHaveValue("");
    await expect(
      page.getByRole("alert").filter({ hasText: "did not confirm this message" }),
    ).toBeVisible();
    await page.getByRole("button", { name: "Retry sending", exact: true }).click();
    await expect(box).toHaveValue("");
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
  await page.getByRole("button", { name: "Messages & privacy" }).click();
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
  await page.getByRole("button", { name: "Save changes", exact: true }).click();
  await expect(
    page.getByRole("alert").filter({ hasText: "settings changed reload" }),
  ).toBeVisible();
  await expect(
    page.getByLabel("Show all conversation messages", { exact: true }),
  ).toBeChecked();
  // Discard reloads the latest saved revision without reloading the page.
  await page.getByRole("button", { name: "Discard", exact: true }).click();
  await expect(
    page.getByLabel("Show all conversation messages", { exact: true }),
  ).not.toBeChecked();
  await page
    .getByLabel("Show all conversation messages", { exact: true })
    .check();
  await page.getByRole("button", { name: "Save changes", exact: true }).click();
  await expect(
    page.getByRole("region", { name: "Unsaved changes" }),
  ).toHaveCount(0);
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
      page.getByRole("switch", { name: "Bot sending" }),
    ).toBeEnabled();
    release();
    await expect(page.locator(".bubble")).toHaveCount(2);
    expect(olderRequests).toBe(1);
  } finally {
    release();
    await configure(page, false);
  }
});


test("unarchived inbox keeps polling snapshots for latest messages", async ({ page }) => {
  await login(page);
  await configure(page, true);
  let snapshots = 0;
  let changes = 0;
  await page.route("**/api/v1/inbox/conversations/demo-chat/history?*", route => {
    snapshots++;
    return route.fulfill({ json: {
      messages: [{ id: "snapshot-initial", direction: "incoming", text: "Synthetic initial snapshot", at: "2026-01-01T00:00:00Z" },
        ...(snapshots > 1 ? [{ id: "snapshot-latest", direction: "incoming", text: "Synthetic latest snapshot", at: "2026-01-01T00:01:00Z" }] : [])],
      next_cursor: "",
    } });
  });
  await page.route("**/api/v1/inbox/conversations/demo-chat/changes?*", route => {
    changes++;
    return route.fulfill({ status: 409, json: { error: "archive_unavailable" } });
  });
  try {
    await page.reload();
    await page.getByRole("button", { name: "Conversations", exact: true }).click();
    await page.locator(".conversation").filter({ hasText: "Demo conversation" }).getByRole("button", { name: /Demo conversation/ }).click();
    await expect(page.getByText("Synthetic initial snapshot", { exact: true })).toBeVisible();
    await expect(page.getByText("Synthetic latest snapshot", { exact: true })).toBeVisible({ timeout: 10000 });
    expect(snapshots).toBeGreaterThan(1);
    expect(changes).toBe(0);
  } finally {
    await configure(page, false);
  }
});
