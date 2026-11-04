import { test, expect, type Page, nextPoll, pollUntil, signIn } from "./fixtures";
import type { Configuration, Job, State, Conversation } from "../src/lib/api";

async function login(page: Page) {
  await signIn(page);
  await expect(
    page.getByRole("heading", { name: "Home", exact: true }),
  ).toBeVisible();
}
/** IDs of the bot "pong" replies the server already holds for the demo conversation. */
async function savedPongs(page: Page) {
  const response = await page.request.get("/api/v1/conversations/demo-chat/history?limit=50&search=pong");
  expect(response.ok()).toBeTruthy();
  const history = (await response.json()) as { messages: { id: string; direction: string }[] };
  return new Set(history.messages.filter((m) => m.direction === "outgoing").map((m) => m.id));
}
test("authenticated command journey and sign-out", async ({
  page,
}) => {
  await login(page);
  // The journey turns these on from the UI, so start from off and leave them off.
  const headers = { Origin: new URL(page.url()).origin };
  const reset = async () => {
    await page.request.post("/api/v1/pause", { headers, data: { paused: true } });
    await page.request.delete("/api/v1/enrollment/demo-chat", { headers });
  };
  await reset();
  await page.reload();
  try {
  await page
    .getByRole("button", { name: "Conversations", exact: true })
    .click();
  const demo = page
    .locator("li.conversation")
    .filter({ hasText: "Demo conversation" });
  const enroll = demo.getByRole("switch", {
    name: "Bot replies for Demo conversation",
  });
  // The switch reflects server state, so it flips once enrollment is confirmed.
  await enroll.click();
  await expect(enroll).toBeChecked();
  const power = page.getByRole("switch", { name: "Bot sending" });
  await power.click();
  await expect(power).toBeChecked();
  await demo.getByRole("button", { name: /Demo conversation/ }).click();
  // Earlier runs against the same server may have left replies: the newest
  // outgoing message must be a new one.
  const before = await savedPongs(page);
  await page.getByRole("button", { name: "Simulate !ping" }).click();
  await pollUntil(page, async () => {
    const latest = page.locator(".bubble.outgoing").last();
    await expect(latest).toContainText("pong", { timeout: 500 });
    expect(before.has((await latest.getAttribute("data-message-id")) ?? "")).toBe(false);
  });
  await page.getByRole("button", { name: "Activity", exact: true }).click();
  await expect(page.locator(".feed-row").first()).toContainText("Submitted");
  await page.getByRole("button", { name: "Settings", exact: true }).click();
  await page.getByRole("button", { name: "Sign out", exact: true }).click();
  await expect(page.getByLabel("Owner credential")).toBeVisible();
  expect(await page.evaluate(() => localStorage.length)).toBe(0);
  } finally {
    // Signed out: restore through a fresh session.
    await signIn(page);
    await reset();
  }
});
for (const settings of [
  { name: "mobile light", width: 390, height: 844, scheme: "light" as const },
  { name: "desktop dark", width: 1440, height: 1000, scheme: "dark" as const },
]) {
  test(settings.name + " layout", async ({ page }) => {
    await page.setViewportSize({
      width: settings.width,
      height: settings.height,
    });
    await page.emulateMedia({ colorScheme: settings.scheme });
    await login(page);
    // The dashboard follows the system color scheme until toggled.
    await expect(page.locator("html")).toHaveAttribute(
      "data-theme",
      settings.scheme,
    );
    for (const name of [
      "Home",
      "Conversations",
      "Commands",
      "Activity",
      "Settings",
    ]) {
      await page.getByRole("button", { name, exact: true }).click();
      expect(
        await page.evaluate(
          () => document.documentElement.scrollWidth <= window.innerWidth,
        ),
      ).toBe(true);
    }
    await page.getByRole("button", { name: "Home", exact: true }).click();
    await page.screenshot({
      path: `test-results/${settings.name.replace(" ", "-")}.png`,
      fullPage: true,
    });
  });
}
test("actual fixture AI activity exposes flat telemetry and omitted zero token fields", async ({
  page,
}) => {
  await login(page);
  const headers = { Origin: new URL(page.url()).origin };
  const config = (await (
    await page.request.get("/api/v1/config")
  ).json()) as Configuration;
  const state = (await (
    await page.request.get("/api/v1/state")
  ).json()) as State;
  const conversations = (
    (await (
      await page.request.get("/api/v1/inbox/conversations")
    ).json()) as { conversations: Conversation[] }
  ).conversations;
  const target = conversations.find((c) => c.id === "demo-chat")!;
  const earlier = new Set(
    ((await (await page.request.get("/api/v1/activity")).json()) as Job[]).map((j) => j.id),
  );
  const ours = (jobs: Job[]) => jobs.find((j) => j.command.includes("e2eusage") && !earlier.has(j.id));
  try {
    expect(
      (
        await page.request.post("/api/v1/enrollment", {
          headers,
          data: {
            conversation_id: target.id,
            participant_id: target.participant_id,
          },
        })
      ).ok(),
    ).toBe(true);
    const updated = structuredClone(config);
    updated.commands.push({
      name: "e2eusage",
      description: "Synthetic telemetry check",
      instructions: "Produce a synthetic reply.",
      type: "ai",
      enabled: true,
      model: "fixture/model",
      profile_id: "",
      memory: false,
      approval: false,
      allowed_conversations: [target.id],
    });
    expect(
      (
        await page.request.put("/api/v1/config", { headers, data: updated })
      ).ok(),
    ).toBe(true);
    expect(
      (
        await page.request.post("/api/v1/pause", {
          headers,
          data: { paused: false },
        })
      ).ok(),
    ).toBe(true);
    expect(
      (
        await page.request.post("/api/v1/fixture/incoming", {
          headers,
          data: {
            conversation_id: target.id,
            participant_id: target.participant_id,
            text: "!e2eusage synthetic",
          },
        })
      ).ok(),
    ).toBe(true);
    await expect
      .poll(async () => {
        const jobs = (await (
          await page.request.get("/api/v1/activity")
        ).json()) as Job[];
        return ours(jobs)?.state;
      })
      .toBe("submitted");
    const jobs = (await (
      await page.request.get("/api/v1/activity")
    ).json()) as Job[];
    const job = ours(jobs)!;
    expect(job.model).toBe("fixture/model");
    expect(job.cost).toBe(0);
    expect(job.prompt_tokens).toBeUndefined();
    expect(job.reasoning_tokens).toBeUndefined();
    expect(job).not.toHaveProperty("usage");
    await page.getByRole("button", { name: "Activity", exact: true }).click();
    // Earlier runs may have left rows: once all of them and this test's are
    // listed, the newest (first) row is this test's job.
    const rows = page.locator(".feed-row").filter({ hasText: "e2eusage" });
    await nextPoll(page);
    await expect(rows).toHaveCount(jobs.filter((j) => j.command.includes("e2eusage")).length);
    const entry = rows.first();
    await entry.locator(".feed-main").click();
    await expect(entry).toContainText("fixture/model");
    await expect(entry).toContainText("0 tokens");
    await expect(entry).toContainText("0 reasoning tokens");
    await expect(entry).toContainText("$0.000000");
    await expect(page.locator(".activity-metrics")).toContainText("$0.0000");
  } finally {
    await page.request.post("/api/v1/pause", {
      headers,
      data: { paused: true },
    });
    // Restore the original commands on top of the revision this test saved.
    const current = (await (await page.request.get("/api/v1/config")).json()) as Configuration;
    expect(
      (await page.request.put("/api/v1/config", { headers, data: { ...config, revision: current.revision } })).ok(),
    ).toBe(true);
    if (!target.enrolled)
      await page.request.delete("/api/v1/enrollment/" + target.id, { headers });
    if (!state.paused)
      await page.request.post("/api/v1/pause", {
        headers,
        data: { paused: false },
      });
  }
});

test("unauthenticated API does not disclose state", async ({ request }) => {
  expect((await request.get("/api/v1/activity")).status()).toBe(401);
});

test("slow conversation refresh does not overlap and failure is specific", async ({
  page,
}) => {
  let fail = true;
  let active = 0;
  let maximum = 0;
  let started = 0;
  let release = () => {};
  await page.route(/\/api\/v1\/(?:inbox\/)?conversations(?:\?|$)/, async (route) => {
    active++;
    started++;
    maximum = Math.max(maximum, active);
    try {
      // A failing refresh stays pending until the test releases it.
      if (fail) await new Promise<void>((resolve) => { release = resolve; });
      if (fail)
        await route.fulfill({
          status: 503,
          contentType: "application/json",
          body: '{"error":"transport_unavailable"}',
        });
      else await route.continue();
    } finally {
      active--;
    }
  });
  // Intercept the initial fetch before login can populate the list cache.
  await login(page);
  await expect.poll(() => started).toBe(1);
  // Two polling intervals pass while that refresh is pending; neither may start
  // another. Give an overlapping request real time to reach the route.
  await nextPoll(page);
  await nextPoll(page);
  await page.waitForTimeout(300);
  expect(started).toBe(1);
  release();
  await expect(page.getByRole("alert")).toContainText("Could not refresh conversations.");
  expect(maximum).toBe(1);
  await expect(page.getByRole("alert")).not.toContainText("Connection lost");
  fail = false;
  release();
  await nextPoll(page);
  await expect(page.getByRole("alert")).toHaveCount(0);
  expect(maximum).toBe(1);
});

for (const heldEndpoint of ["conversations", "state"]) {
  test(`pause button updates both ways with a pending ${heldEndpoint} poll`, async ({
    page,
  }) => {
    await login(page);
    const toggle = page.getByRole("switch", { name: "Bot sending" });
    await expect(toggle).toBeEnabled();
    if ((await toggle.getAttribute("aria-checked")) === "true") {
      await toggle.click();
      await expect(toggle).not.toBeChecked();
      await expect(toggle).toBeEnabled();
    }
    let release!: () => void;
    let started!: () => void;
    const held = new Promise<void>((resolve) => {
      release = resolve;
    });
    const pending = new Promise<void>((resolve) => {
      started = resolve;
    });
    if (heldEndpoint === "conversations") {
      // The list is refetched only when the reported revision changes.
      let revision = 0;
      await page.route("**/api/v1/state", async (route) => {
        const response = await route.fetch();
        if (!response.ok()) return route.fulfill({ response });
        await route.fulfill({ json: { ...(await response.json()), conversations_revision: `test-${revision++}` } });
      });
    }
    await page.route(
      heldEndpoint === "conversations" ? /\/api\/v1\/(?:inbox\/)?conversations(?:\?|$)/ : "**/api/v1/state",
      async (route) => {
        const response = await route.fetch();
        started();
        await held;
        await route.fulfill({ response });
      },
      { times: 1 },
    );
    try {
      await pending;
      await toggle.click();
      await expect(toggle).toBeChecked();
      await expect(toggle).toBeEnabled();
      await expect(page.locator(".hero-pill")).toHaveText("LIVE");
      expect(
        (await (await page.request.get("/api/v1/state")).json()).paused,
      ).toBe(false);
      if (heldEndpoint === "state") {
        // The captured paused=true response must not overwrite the pause response.
        const resumed = page.waitForResponse("**/api/v1/activity");
        release();
        await resumed;
        await expect(toggle).toBeChecked();
        await expect(page.locator(".hero-pill")).toHaveText("LIVE");
      }
      await toggle.click();
      await expect(toggle).not.toBeChecked();
      await expect(toggle).toBeEnabled();
      await expect(page.locator(".hero-pill")).toHaveText("PAUSED");
      expect(
        (await (await page.request.get("/api/v1/state")).json()).paused,
      ).toBe(true);
    } finally {
      release();
    }
  });
}

test("rejected pause change leaves the displayed state unchanged", async ({
  page,
}) => {
  await login(page);
  const toggle = page.getByRole("switch", { name: "Bot sending" });
  await expect(toggle).toBeEnabled();
  if ((await toggle.getAttribute("aria-checked")) === "true") {
    await toggle.click();
    await expect(toggle).not.toBeChecked();
    await expect(toggle).toBeEnabled();
  }
  await page.route("**/api/v1/pause", (route) =>
    route.fulfill({
      status: 503,
      contentType: "application/json",
      body: '{"error":"transport_unavailable"}',
    }),
  );
  await toggle.click();
  await expect(page.getByRole("alert")).toContainText("transport unavailable");
  await expect(toggle).not.toBeChecked();
  await expect(page.locator(".hero-pill")).toHaveText("PAUSED");
  expect((await (await page.request.get("/api/v1/state")).json()).paused).toBe(
    true,
  );
});

test("startup readiness recovers while initial activity loading is pending", async ({
  page,
}) => {
  let ready = false;
  let release!: () => void;
  const held = new Promise<void>((resolve) => (release = resolve));
  await page.route("**/api/v1/state", async (route) => {
    const response = await route.fetch();
    const state = await response.json();
    await route.fulfill({
      response,
      json: { ...state, paused: true, transport_ready: ready },
    });
  });
  await page.route("**/api/v1/activity", async (route) => {
    await held;
    await route.continue();
  }, { times: 1 });
  try {
    await login(page);
    const toggle = page.getByRole("switch", { name: "Bot sending" });
    await expect(toggle).toBeDisabled();
    await expect(page.getByText("Phone offline", { exact: true })).toBeVisible();
    ready = true;
    // Readiness must advance on the next state poll, without a reload or
    // waiting for activity/discovery.
    await nextPoll(page);
    await expect(toggle).toBeEnabled({ timeout: 8000 });
    await expect(toggle).not.toBeChecked();
    await toggle.click();
    await expect(toggle).toBeChecked();
    await expect(page.locator(".hero-pill")).toHaveText("LIVE");
  } finally {
    release();
    await page.request.post("/api/v1/pause", {
      headers: { Origin: new URL(page.url()).origin },
      data: { paused: true },
    });
  }
});
