import { test, expect, type Page } from "@playwright/test";
import { readFileSync } from "node:fs";
import type { Configuration } from "../src/api";
const initial: Configuration = {
  settings: {
    default_model: "fixture/model",
    hourly_limit: 10,
    daily_limit: 100,
    daily_budget: 1,
    quiet_enabled: false,
    quiet_start: 22,
    quiet_end: 7,
    timezone: "UTC",
    memory_hours: 24,
    retention_days: 30,
    max_chars: 1000,
    favorites: [],
    model_generation: {},
  },
  commands: [
    {
      name: "hello",
      description: "Synthetic greeting",
      instructions: "hello",
      type: "fixed",
      enabled: true,
      model: "",
      profile_id: "",
      memory: false,
      approval: false,
      allowed_conversations: [],
    },
  ],
  profiles: [],
};
async function setup(page: Page) {
  let config = structuredClone(initial);
  await page.route("**/api/v1/config", (route) => {
    if (route.request().method() === "PUT")
      config = route.request().postDataJSON();
    return route.fulfill({ json: config });
  });
  await page.route("**/api/v1/models", (route) =>
    route.fulfill({
      json: [
        {
          id: "fixture/model",
          name: "Fixture model",
          context_length: 4096,
          parameters: [],
          has_reasoning: false,
          reasoning_mandatory: false,
          reasoning_efforts: [],
        },
      ],
    }),
  );
  await page.route("**/api/v1/provider-key", (route) =>
    route.fulfill({ json: { configured: false } }),
  );
  await page.route("**/api/v1/compatible-provider", (route) =>
    route.fulfill({
      json: { base_url: "https://api.openai.com/v1", configured: false },
    }),
  );
  const descriptor = JSON.parse(
    readFileSync(".test-state/connection.json", "utf8"),
  );
  await page.goto("/");
  await page
    .getByLabel("Owner credential")
    .fill(readFileSync(descriptor.owner, "utf8"));
  await page.getByRole("button", { name: "Open dashboard" }).click();
  await expect(
    page.getByRole("heading", { name: "Home", exact: true }),
  ).toBeVisible();
  return () => config;
}
test("draft navigation requires an explicit choice and discard restores saved configuration", async ({
  page,
}) => {
  await setup(page);
  await page.getByRole("button", { name: "Commands", exact: true }).click();
  await page
    .getByLabel("Command 1 description", { exact: true })
    .fill("Unsaved draft");
  await page.getByRole("button", { name: "Activity", exact: true }).click();
  const dialog = page.getByRole("alertdialog", {
    name: "Leave without saving?",
  });
  await expect(dialog).toBeVisible();
  await expect(
    dialog.getByRole("button", { name: "Keep editing" }),
  ).toBeFocused();
  await page.keyboard.press("Escape");
  await expect(dialog).not.toBeVisible();
  await expect(
    page.getByLabel("Command 1 description", { exact: true }),
  ).toHaveValue("Unsaved draft");
  await page.getByRole("button", { name: "Discard draft" }).click();
  await expect(
    page.getByLabel("Command 1 description", { exact: true }),
  ).toHaveValue("Synthetic greeting");
  await page
    .getByLabel("Command 1 description", { exact: true })
    .fill("Another draft");
  await page.getByRole("button", { name: "Activity", exact: true }).click();
  await dialog.getByRole("button", { name: "Discard changes" }).click();
  await expect(
    page.getByRole("heading", { name: "Activity", exact: true }),
  ).toBeVisible();
  await page.getByRole("button", { name: "Commands", exact: true }).click();
  await expect(
    page.getByLabel("Command 1 description", { exact: true }),
  ).toHaveValue("Synthetic greeting");
});
test("command templates and duplicates remain disabled with unique names", async ({
  page,
}) => {
  const config = await setup(page);
  await page.getByRole("button", { name: "Commands", exact: true }).click();
  await page
    .getByRole("button", { name: "AI assistant", exact: false })
    .click();
  await page
    .getByRole("button", { name: "Duplicate command 1", exact: true })
    .click();
  await expect(page.getByLabel("Enable command 2")).not.toBeChecked();
  await expect(
    page.getByLabel("Command 2 approval", { exact: true }),
  ).toBeChecked();
  await expect(page.getByLabel("Enable command 3")).not.toBeChecked();
  await page.getByRole("button", { name: "Save configuration" }).click();
  await expect(page.getByRole("status")).toContainText("Configuration saved");
  expect(config().commands.map((c) => c.name)).toEqual([
    "hello",
    "ask",
    "hello_copy",
  ]);
  expect(
    config()
      .commands.slice(1)
      .every((c) => !c.enabled),
  ).toBe(true);
  await page.getByLabel("Search commands").fill("ask");
  await expect(page.getByLabel("Command 1 name", { exact: true })).toHaveCount(
    0,
  );
  await expect(page.getByLabel("Command 2 name", { exact: true })).toHaveValue(
    "ask",
  );
});
test("activity filters distinguish approvals and unknown outcomes without inventing cost", async ({
  page,
}) => {
  await setup(page);
  await page.route("**/api/v1/activity", (route) =>
    route.fulfill({
      json: [
        {
          id: "approval",
          conversation_id: "fixture-thread",
          command: "check",
          state: "awaiting_approval",
          created_at: "2026-01-01T00:00:00Z",
        },
        {
          id: "uncertain",
          conversation_id: "fixture-thread",
          command: "ask",
          state: "unknown_submission",
          created_at: "2026-01-01T00:00:00Z",
          model: "fixture/model",
          prompt_tokens: 2,
          completion_tokens: 3,
        },
      ],
    }),
  );
  await page.getByRole("button", { name: "Activity", exact: true }).click();
  await expect(page.locator(".journal summary")).toHaveCount(2);
  await expect(page.locator(".activity-metrics")).toContainText(
    "1 request with unavailable cost",
  );
  await page.getByRole("button", { name: /Needs review/ }).click();
  await expect(page.locator(".journal summary")).toHaveCount(1);
  await expect(page.locator(".journal summary")).toContainText("!check");
  await page.getByRole("button", { name: /Attention/ }).click();
  await expect(page.locator(".journal summary")).toContainText(
    "unknown submission",
  );
  await page.locator(".journal summary").click();
  await expect(page.locator(".event-detail")).toContainText("fixture/model");
  await expect(page.locator(".event-detail")).toContainText("5 tokens");
  await expect(page.locator(".event-detail")).toContainText(
    "0 reasoning tokens",
  );
  await expect(page.locator(".event-detail")).toContainText("Cost unavailable");
  await expect(page.getByRole("button", { name: /retry/i })).toHaveCount(0);
  await page.getByLabel("Search activity").fill("nothing");
  await expect(page.getByText("Nothing matches this view")).toBeVisible();
  await page.getByRole("button", { name: "Clear activity filters" }).click();
  await expect(page.locator(".journal summary")).toHaveCount(2);
});
test("model search reaches beyond the first page and retains a selected model", async ({
  page,
}) => {
  const cfg = await setup(page);
  const models = Array.from({ length: 100 }, (_, i) => ({
    id: "fixture/model-" + i,
    name: "Synthetic model " + i,
    context_length: 4096,
    parameters: [],
    has_reasoning: false,
    reasoning_mandatory: false,
    reasoning_efforts: [],
  }));
  await page.route("**/api/v1/models", (route) =>
    route.fulfill({ json: models }),
  );
  await page.getByRole("button", { name: "Settings", exact: true }).click();
  await page
    .getByLabel("Search default model", { exact: true })
    .fill("model 99");
  await page
    .getByLabel("Default model", { exact: true })
    .selectOption("fixture/model-99");
  await page
    .getByLabel("Search default model", { exact: true })
    .fill("model 1");
  await expect(page.getByLabel("Default model", { exact: true })).toHaveValue(
    "fixture/model-99",
  );
  await page.getByRole("button", { name: "Save configuration" }).click();
  await expect(page.getByRole("status")).toContainText("Configuration saved");
  expect(cfg().settings.default_model).toBe("fixture/model-99");
});
test("conversation filters preserve enrollment and theme switches do not write browser storage", async ({
  page,
}) => {
  await setup(page);
  await page.getByRole("button", { name: "Switch to light theme" }).click();
  await expect(page.locator("html")).toHaveAttribute("data-theme", "light");
  expect(
    await page.evaluate(() => ({
      local: localStorage.length,
      session: sessionStorage.length,
    })),
  ).toEqual({ local: 0, session: 0 });
  await page.route("**/api/v1/conversations", (route) =>
    route.fulfill({
      json: [
        {
          id: "one",
          participant_id: "one",
          label: "Synthetic Alpha",
          protocol: "fixture",
          group: false,
          writable: true,
          enrolled: true,
        },
        {
          id: "two",
          participant_id: "two",
          label: "Synthetic Group",
          protocol: "fixture",
          group: true,
          writable: false,
          enrolled: false,
        },
      ],
    }),
  );
  await page
    .getByRole("button", { name: "Conversations", exact: true })
    .click();
  await expect(page.locator("article.conversation")).toHaveCount(2, {
    timeout: 10000,
  });
  await page.getByRole("button", { name: "Enrolled", exact: true }).click();
  await expect(page.locator("article.conversation")).toHaveCount(1);
  await page.getByLabel("Search conversations").fill("missing");
  await expect(page.getByText("No conversations found")).toBeVisible();
  await page.getByLabel("Search conversations").fill("");
  await page.getByRole("button", { name: "All", exact: true }).click();
  await expect(
    page
      .locator("article.conversation")
      .filter({ hasText: "Synthetic Group" })
      .getByRole("button", { name: "Enroll", exact: true }),
  ).toBeDisabled();
});

for (const viewport of [
  { name: "desktop", width: 1440, height: 1000 },
  { name: "mobile", width: 390, height: 844 },
]) {
  test(`dark workspace visual coverage ${viewport.name}`, async ({ page }) => {
    await page.setViewportSize({
      width: viewport.width,
      height: viewport.height,
    });
    await page.route("**/api/v1/conversations", (route) =>
      route.fulfill({
        json: [
          {
            id: "visual-thread",
            participant_id: "visual-person",
            label: "Demo conversation",
            protocol: "RCS",
            group: false,
            writable: true,
            enrolled: true,
          },
        ],
      }),
    );
    await page.route("**/api/v1/activity", (route) =>
      route.fulfill({
        json: [
          {
            id: "visual-pending",
            conversation_id: "visual-thread",
            command: "!ask",
            state: "awaiting_approval",
            created_at: "2026-01-01T12:00:00Z",
          },
          {
            id: "visual-done",
            conversation_id: "visual-thread",
            command: "!hello",
            state: "submitted",
            created_at: "2026-01-01T11:00:00Z",
          },
        ],
      }),
    );
    await setup(page);
    await page.route("**/api/v1/state", (route) =>
      route.fulfill({
        json: {
          api_version: 1,
          paused: false,
          transport_ready: true,
          fixture: true,
          transport: "fixture",
          capabilities: ["history", "configuration"],
        },
      }),
    );
    await page.route(
      "**/api/v1/conversations/visual-thread/history?**",
      (route) =>
        route.fulfill({
          json: {
            messages: [
              {
                id: "visual-incoming",
                direction: "incoming",
                at: "2026-01-01T12:00:00Z",
                text: "!hello",
              },
              {
                id: "visual-outgoing",
                direction: "outgoing",
                at: "2026-01-01T12:00:01Z",
                text: "Hello! Your synthetic workspace is ready.",
              },
            ],
            next_cursor: "",
          },
        }),
    );
    await expect(page.locator(".hero .pill")).toHaveText("ACTIVE");
    for (const screen of [
      "Home",
      "Conversations",
      "Commands",
      "Activity",
      "Settings",
    ]) {
      await page.getByRole("button", { name: screen, exact: true }).click();
      await expect(
        page.getByRole("heading", { name: screen, exact: true }),
      ).toBeVisible();
      if (screen === "Conversations")
        await page
          .getByRole("button", { name: /Demo conversation RCS/ })
          .click();
      if (screen === "Commands")
        await expect(
          page.getByLabel("Command 1 name", { exact: true }),
        ).toBeVisible();
      if (screen === "Settings")
        await expect(
          page.getByLabel("Default model", { exact: true }),
        ).toBeVisible();
      expect(
        await page.evaluate(
          () => document.documentElement.scrollWidth <= window.innerWidth,
        ),
      ).toBe(true);
      const clipped = await page.locator("nav button").evaluateAll(
        (buttons) =>
          buttons.filter((button) => {
            const r = button.getBoundingClientRect();
            const label = button.querySelector("span")?.getBoundingClientRect();
            return (
              label && (label.left < r.left - 1 || label.right > r.right + 1)
            );
          }).length,
      );
      expect(clipped).toBe(0);
      await page.screenshot({
        path: `test-results/ui-${viewport.name}-${screen.toLowerCase()}.png`,
        animations: "disabled",
      });
    }
  });
}
