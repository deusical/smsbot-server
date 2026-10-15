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
  await page.getByRole("button", { name: "Sign in" }).click();
  await expect(
    page.getByRole("heading", { name: "Home", exact: true }),
  ).toBeVisible();
  return () => config;
}
test("drafts survive navigation and discard restores saved configuration", async ({
  page,
}) => {
  await setup(page);
  await page.getByRole("button", { name: "Commands", exact: true }).click();
  const description = page.getByLabel("Description", { exact: true });
  await description.fill("Unsaved draft");
  const savebar = page.getByRole("region", { name: "Unsaved changes" });
  await expect(savebar).toBeVisible();
  // Navigation keeps the draft instead of interrupting with a dialog.
  await page.getByRole("button", { name: "Activity", exact: true }).click();
  await expect(
    page.getByRole("heading", { name: "Activity", exact: true }),
  ).toBeVisible();
  await expect(page.getByRole("alertdialog")).toHaveCount(0);
  await expect(savebar).toBeVisible();
  await page.getByRole("button", { name: "Commands", exact: true }).click();
  await expect(description).toHaveValue("Unsaved draft");
  await savebar.getByRole("button", { name: "Discard" }).click();
  await expect(description).toHaveValue("Synthetic greeting");
  await expect(savebar).toHaveCount(0);
  // Editing back to the saved value is not a change.
  await description.fill("Another draft");
  await expect(savebar).toBeVisible();
  await description.fill("Synthetic greeting");
  await expect(savebar).toHaveCount(0);
});
test("command templates and duplicates remain disabled with unique names", async ({
  page,
}) => {
  const config = await setup(page);
  await page.getByRole("button", { name: "Commands", exact: true }).click();
  await page.getByRole("button", { name: "New", exact: true }).click();
  await page.getByRole("menuitem", { name: /AI assistant/ }).click();
  await expect(page.getByLabel("Command name")).toHaveValue("ask");
  await expect(page.getByLabel("Enabled", { exact: true })).not.toBeChecked();
  await expect(page.getByLabel("Ask me before replying")).toBeChecked();
  await page.getByRole("button", { name: /!hello/ }).click();
  await page.getByRole("button", { name: "Duplicate command" }).click();
  await expect(page.getByLabel("Command name")).toHaveValue("hello_copy");
  await expect(page.getByLabel("Enabled", { exact: true })).not.toBeChecked();
  await page.getByRole("button", { name: "Save changes" }).click();
  await expect(page.getByRole("status")).toContainText("Changes saved");
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
  await expect(page.locator(".command-item")).toHaveCount(1);
  await expect(page.locator(".command-item")).toContainText("!ask");
});
test("invalid command names are explained before saving", async ({ page }) => {
  const config = await setup(page);
  await page.getByRole("button", { name: "Commands", exact: true }).click();
  await page.getByLabel("Command name").fill("9bad");
  await page.getByRole("button", { name: "Save changes" }).click();
  await expect(
    page.getByRole("region", { name: "Unsaved changes" }).getByRole("alert"),
  ).toContainText("names start with a lowercase letter");
  expect(config().commands[0].name).toBe("hello");
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
  await expect(page.locator(".feed-row")).toHaveCount(2);
  await expect(page.locator(".activity-metrics")).toContainText(
    "1 request with unavailable cost",
  );
  await page.getByRole("button", { name: /^Needs approval/ }).click();
  await expect(page.locator(".feed-row")).toHaveCount(1);
  await expect(page.locator(".feed-row")).toContainText("!check");
  await expect(page.getByRole("button", { name: "Approve" })).toBeVisible();
  await page.getByRole("button", { name: /^Problems/ }).click();
  await expect(page.locator(".feed-row")).toContainText("Uncertain");
  await page.locator(".feed-main").click();
  await expect(page.locator(".feed-detail")).toContainText("fixture/model");
  await expect(page.locator(".feed-detail")).toContainText("5 tokens");
  await expect(page.locator(".feed-detail")).toContainText(
    "0 reasoning tokens",
  );
  await expect(page.locator(".feed-detail")).toContainText("Cost unavailable");
  await expect(page.locator(".feed-detail")).toContainText(
    "retried automatically",
  );
  await expect(page.getByRole("button", { name: /retry/i })).toHaveCount(0);
  await page.getByLabel("Search activity").fill("nothing");
  await expect(page.getByText("Nothing matches this view")).toBeVisible();
  await page.getByRole("button", { name: "Clear filters" }).click();
  await expect(page.locator(".feed-row")).toHaveCount(2);
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
  const picker = page.getByRole("button", { name: /^Default model/ });
  await picker.click();
  await page
    .getByLabel("Search default model", { exact: true })
    .fill("model 99");
  await page.getByRole("option", { name: "Synthetic model 99" }).click();
  await picker.click();
  await page
    .getByLabel("Search default model", { exact: true })
    .fill("model 1");
  await expect(
    page.getByRole("option", { name: "Synthetic model 19" }),
  ).toBeVisible();
  await expect(
    page.getByRole("option", { name: "Synthetic model 98" }),
  ).toHaveCount(0);
  await page.keyboard.press("Escape");
  await expect(picker).toContainText("Synthetic model 99");
  await page.getByRole("button", { name: "Save changes" }).click();
  await expect(page.getByRole("status")).toContainText("Changes saved");
  expect(cfg().settings.default_model).toBe("fixture/model-99");
});
test("conversation filters preserve enrollment and theme switches do not write browser storage", async ({
  page,
}) => {
  await setup(page);
  await page.getByRole("button", { name: "Switch to dark theme" }).click();
  await expect(page.locator("html")).toHaveAttribute("data-theme", "dark");
  expect(
    await page.evaluate(() => ({
      local: localStorage.length,
      session: sessionStorage.length,
    })),
  ).toEqual({ local: 0, session: 0 });
  await page.route("**/api/v1/inbox/conversations", (route) =>
    route.fulfill({
      json: {conversations: [
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
      ],next_cursor:"",partial:false},
    }),
  );
  await page
    .getByRole("button", { name: "Conversations", exact: true })
    .click();
  await expect(page.locator("li.conversation")).toHaveCount(2, {
    timeout: 10000,
  });
  await page.getByRole("button", { name: /^Bot on/ }).click();
  await expect(page.locator("li.conversation")).toHaveCount(1);
  await page.getByLabel("Search conversations").fill("missing");
  await expect(page.getByText("No conversations found")).toBeVisible();
  await page.getByLabel("Search conversations").fill("");
  await page.getByRole("button", { name: "All", exact: true }).click();
  await expect(
    page.getByRole("switch", { name: "Bot replies for Synthetic Group" }),
  ).toBeDisabled();
});

for (const viewport of [
  { name: "desktop", width: 1440, height: 1000 },
  { name: "mobile", width: 390, height: 844 },
]) {
  test(`dark workspace visual coverage ${viewport.name}`, async ({ page }) => {
    await page.emulateMedia({ colorScheme: "dark" });
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
    await expect(page.locator(".hero-pill")).toHaveText("LIVE");
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
          .getByRole("button", { name: /Demo conversation/ })
          .first()
          .click();
      if (screen === "Commands") {
        // Mobile shows the command list first; open the editor explicitly.
        if (viewport.name === "mobile")
          await page.getByRole("button", { name: /!hello/ }).click();
        await expect(page.getByLabel("Command name")).toBeVisible();
      }
      if (screen === "Settings")
        await expect(
          page.getByRole("button", { name: /^Default model/ }),
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
      // A phone thread is full screen; leave it to reach the navigation.
      if (screen === "Conversations" && viewport.name === "mobile")
        await page
          .getByRole("button", { name: "Back to conversations" })
          .click();
    }
  });
}
