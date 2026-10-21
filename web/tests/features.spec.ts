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
      enabled: false,
      model: "",
      profile_id: "",
      memory: false,
      approval: false,
      allowed_conversations: [],
    },
  ],
  profiles: [],
};
const models = [
  {
    id: "fixture/model",
    name: "Fixture model",
    context_length: 4096,
    parameters: ["temperature"],
    has_reasoning: true,
    reasoning_mandatory: false,
    reasoning_efforts: ["low", "high"],
  },
];
async function setup(page: Page) {
  let config = structuredClone(initial);
  await page.route("**/api/v1/config", async (route) => {
    if (route.request().method() === "PUT")
      config = route.request().postDataJSON();
    await route.fulfill({ json: config });
  });
  await page.route("**/api/v1/models", (route) =>
    route.fulfill({ json: models }),
  );
  await page.route("**/api/v1/provider-key", (route) =>
    route.fulfill({ json: { configured: route.request().method() === "PUT" } }),
  );
  await page.route("**/api/v1/compatible-provider", async (route) => {
    if (route.request().method() === "PUT") {
      const body = route.request().postDataJSON();
      return route.fulfill({
        json: { base_url: body.base_url, configured: !!body.key },
      });
    }
    return route.fulfill({
      json: { base_url: "https://api.openai.com/v1", configured: false },
    });
  });
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

test("natural command forms save AI controls and playground remains offline", async ({
  page,
}) => {
  const configuration = await setup(page);
  let playground: unknown;
  await page.route("**/api/v1/playground", (route) => {
    playground = route.request().postDataJSON();
    return route.fulfill({
      json: {
        text: "Synthetic preview",
        model: "fixture/model",
        simulated: true,
      },
    });
  });
  await page.getByRole("button", { name: "Commands", exact: true }).click();
  await page
    .getByRole("group", { name: "Command type" })
    .getByRole("button", { name: "AI", exact: true })
    .click();
  await page
    .getByLabel("Instructions", { exact: true })
    .fill("Answer the synthetic input.");
  await page.getByLabel("Remember the conversation").check();
  await page.getByLabel("Ask me before replying").check();
  await page.getByLabel("Custom generation settings").check();
  await page.getByLabel("Reasoning", { exact: true }).selectOption("high");
  await page.getByLabel("Temperature", { exact: true }).fill("0.3");
  await page.getByRole("button", { name: "Save changes" }).click();
  await expect(page.getByRole("status")).toContainText("Changes saved");
  expect(configuration().commands[0]).toMatchObject({
    type: "ai",
    memory: true,
    approval: true,
    generation: { reasoning: "high", temperature: 0.3 },
  });
  await page.getByLabel("Preview input").fill("Offline sample");
  await page.getByRole("button", { name: "Preview", exact: true }).click();
  await expect(
    page.getByRole("status").filter({ hasText: "Synthetic preview" }),
  ).toBeVisible();
  expect(playground).toEqual({ command: "hello", input: "Offline sample" });
  await page.evaluate(() => window.scrollTo(0, 0));
  await page.screenshot({
    path: "test-results/server-commands.png",
    fullPage: true,
  });
});

test("settings preserve model defaults, provider key stays transient, recipe import is disabled", async ({
  page,
}) => {
  const configuration = await setup(page);
  let imported: unknown;
  let keyBody: unknown;
  await page.route("**/api/v1/provider-key", (route) => {
    if (route.request().method() === "PUT")
      keyBody = route.request().postDataJSON();
    return route.fulfill({ json: { configured: !!keyBody } });
  });
  await page.route("**/api/v1/recipes/import", (route) => {
    imported = route.request().postDataJSON();
    return route.fulfill({ json: initial });
  });
  await page.getByRole("button", { name: "Settings", exact: true }).click();
  await page.getByLabel("Reasoning", { exact: true }).selectOption("low");
  await page.getByLabel("Reasoning", { exact: true }).selectOption("default");
  await page.getByRole("button", { name: "Limits & quiet hours" }).click();
  await page.getByLabel("Daily AI budget").fill("2.5");
  await page.getByLabel("Quiet hours", { exact: true }).check();
  await page.getByRole("button", { name: "Save changes" }).click();
  await expect(page.getByRole("status")).toContainText("Changes saved");
  expect(configuration().settings).toMatchObject({
    daily_budget: 2.5,
    quiet_enabled: true,
    model_generation: { "fixture/model": { reasoning: "default" } },
  });
  await page.getByRole("button", { name: "Providers", exact: true }).click();
  await page
    .getByLabel("OpenRouter API key", { exact: true })
    .fill("SYNTHETIC_PROVIDER_KEY");
  await page.getByRole("button", { name: "Save key" }).click();
  await expect(
    page.getByLabel("OpenRouter API key", { exact: true }),
  ).toHaveValue("");
  expect(keyBody).toEqual({ key: "SYNTHETIC_PROVIDER_KEY" });
  expect(
    await page.evaluate(() => ({
      local: localStorage.length,
      session: sessionStorage.length,
    })),
  ).toEqual({ local: 0, session: 0 });
  await page.getByRole("button", { name: "Import & export" }).click();
  await page
    .getByLabel("Import recipe")
    .setInputFiles({
      name: "fixture-recipe.json",
      mimeType: "application/json",
      buffer: Buffer.from(JSON.stringify(initial)),
    });
  await expect(
    page.getByRole("status").filter({ hasText: "Commands are disabled" }),
  ).toBeVisible();
  expect(imported).toEqual({ configuration: initial });
});

test("compatible endpoint key is transient and catalog refresh is explicit", async ({
  page,
}) => {
  await setup(page);
  let submitted: unknown;
  let refreshes = 0;
  await page.route("**/api/v1/compatible-provider", async (route) => {
    if (route.request().method() !== "PUT") return route.fallback();
    submitted = route.request().postDataJSON();
    const body = submitted as { base_url: string; key: string };
    return route.fulfill({
      json: { base_url: body.base_url, configured: !!body.key },
    });
  });
  await page.route("**/api/v1/models/refresh-compatible", (route) => {
    refreshes++;
    return route.fulfill({
      json: [
        ...models,
        {
          id: "compatible:gemini-3.8-flash-high",
          name: "gemini-3.8-flash-high",
          provider: "compatible",
          remote_id: "gemini-3.8-flash-high",
          context_length: 8192,
          parameters: [],
          has_reasoning: false,
          reasoning_mandatory: false,
          reasoning_efforts: [],
        },
      ],
    });
  });
  await page.getByRole("button", { name: "Settings", exact: true }).click();
  await page.getByRole("button", { name: "Providers", exact: true }).click();
  await page.getByLabel("Base URL").fill("http://127.0.0.1:8317/v1");
  await page
    .getByLabel("API key", { exact: true })
    .fill("SYNTHETIC_ACCESS_KEY");
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await expect(page.getByLabel("API key", { exact: true })).toHaveValue("");
  expect(submitted).toEqual({
    base_url: "http://127.0.0.1:8317/v1",
    key: "SYNTHETIC_ACCESS_KEY",
  });
  await page.getByRole("button", { name: "AI models", exact: true }).click();
  await page.getByRole("button", { name: "Refresh compatible" }).click();
  await expect(page.getByText("Compatible models refreshed.")).toBeVisible();
  expect(refreshes).toBe(1);
  await page.getByRole("button", { name: /^Default model/ }).click();
  await expect(page.getByLabel("Search default model")).toBeVisible();
  const options = await page.locator(".option-name").allTextContents();
  expect(options).toContain("gemini-3.8-flash-high · Compatible API");
  expect(
    await page.evaluate(() => ({
      local: localStorage.length,
      session: sessionStorage.length,
    })),
  ).toEqual({ local: 0, session: 0 });
});

test("history search and pagination preserve messages, compose sends on Enter", async ({
  page,
}) => {
  await setup(page);
  let compose: unknown;
  await page.route(/\/api\/v1\/inbox\/conversations(\?refresh=1)?$/, (route) =>
    route.fulfill({
      json: {
        conversations: [
          {
            id: "fixture-thread",
            participant_id: "fixture-person",
            label: "Synthetic thread",
            protocol: "fixture",
            group: false,
            writable: true,
            enrolled: true,
          },
        ],
        next_cursor: "",
      },
    }),
  );
  await page.route("**/api/v1/state", (route) =>
    route.fulfill({
      json: {
        api_version: 1,
        paused: false,
        transport_ready: true,
        transport: "fixture",
        fixture: true,
        capabilities: ["archive_history"],
      },
    }),
  );
  await page.route(
    "**/api/v1/conversations/fixture-thread/history?**",
    (route) => {
      const url = new URL(route.request().url());
      return route.fulfill({
        json: url.searchParams.get("search")
          ? {
              messages: [
                {
                  id: "match",
                  direction: "incoming",
                  at: "2026-01-01T00:00:00Z",
                  text: "Matched synthetic text",
                },
              ],
              next_cursor: "",
            }
          : url.searchParams.get("cursor")
            ? {
                messages: [
                  {
                    id: "older",
                    direction: "incoming",
                    at: "2026-01-01T00:00:00Z",
                    text: "Older synthetic text",
                  },
                ],
                next_cursor: "",
              }
            : {
                messages: [
                  {
                    id: "recent",
                    direction: "outgoing",
                    at: "2026-01-02T00:00:00Z",
                    text: "Recent synthetic text",
                  },
                ],
                next_cursor: "older-page",
              },
      });
    },
  );
  await page.route(
    "**/api/v1/conversations/fixture-thread/compose",
    (route) => {
      compose = route.request().postDataJSON();
      return route.fulfill({ json: { id: "fixture-job", queued: true } });
    },
  );
  await expect(page.locator(".hero-pill")).toHaveText("LIVE", {
    timeout: 10000,
  });
  await page
    .getByRole("button", { name: "Conversations", exact: true })
    .click();
  await page.getByRole("button", { name: /Synthetic thread/ }).click();
  await expect(page.locator(".messages")).toContainText(
    "Recent synthetic text",
  );
  await page.getByRole("button", { name: "Load older messages" }).click();
  await expect(page.locator(".messages")).toContainText("Older synthetic text");
  await expect(page.locator(".messages")).toContainText(
    "Recent synthetic text",
  );
  await page.getByRole("button", { name: "Search this conversation" }).click();
  // Search runs as you type.
  await page.getByLabel("Search messages").fill("Matched");
  await expect(page.locator(".messages")).toContainText(
    "Matched synthetic text",
  );
  await expect(page.locator(".messages")).not.toContainText(
    "Recent synthetic text",
  );
  const box = page.getByLabel("Message", { exact: true });
  await box.fill("Explicit synthetic reply");
  // Enter sends directly; Shift+Enter would add a line instead.
  await box.press("Enter");
  await expect(box).toHaveValue("");
  await expect(page.getByRole("alertdialog")).toHaveCount(0);
  expect(compose).toEqual({ text: "Explicit synthetic reply" });
});

test("approval failures remain visible and requests can be cancelled", async ({
  page,
}) => {
  await setup(page);
  let cancelled = false;
  await page.route("**/api/v1/activity", (route) =>
    route.fulfill({
      json: [
        {
          id: "fixture-approval",
          conversation_id: "fixture-thread",
          command: "hello",
          state: cancelled ? "cancelled" : "awaiting_approval",
          created_at: "2026-01-01T00:00:00Z",
        },
      ],
    }),
  );
  await page.route("**/api/v1/jobs/fixture-approval/approve", (route) =>
    route.fulfill({ status: 409, json: { error: "approval_expired" } }),
  );
  await page.route("**/api/v1/jobs/fixture-approval/cancel", (route) => {
    cancelled = true;
    return route.fulfill({ json: { cancelled: true } });
  });
  await page.getByRole("button", { name: "Activity", exact: true }).click();
  await expect(page.locator(".feed-row").first()).toContainText(
    "Needs approval",
    { timeout: 10000 },
  );
  await page.getByRole("button", { name: "Approve", exact: true }).click();
  await expect(page.getByRole("alert")).toContainText("approval expired");
  await page.getByRole("button", { name: "Cancel !hello" }).click();
  await expect(page.locator(".feed-row").first()).toContainText("Cancelled");
});

test("unsupported saved temperature can be removed and unknown reasoning is not offered", async ({
  page,
}) => {
  await setup(page);
  let saved = structuredClone(initial);
  saved.settings.model_generation = {
    "fixture/model": {
      reasoning: "default",
      answer_length: "short",
      tokens: 2048,
      temperature: 0.2,
    },
  };
  await page.route("**/api/v1/config", (route) => {
    if (route.request().method() === "PUT")
      saved = route.request().postDataJSON();
    return route.fulfill({ json: saved });
  });
  await page.route("**/api/v1/models", (route) =>
    route.fulfill({
      json: [
        {
          ...models[0],
          parameters: [],
          has_reasoning: false,
          reasoning_efforts: [],
        },
      ],
    }),
  );
  await page.getByRole("button", { name: "Settings", exact: true }).click();
  await expect(
    page.getByRole("button", { name: "Clear temperature" }),
  ).toBeVisible();
  expect(
    await page
      .getByLabel("Reasoning", { exact: true })
      .locator("option")
      .allTextContents(),
  ).toEqual(["Model default"]);
  await page.getByRole("button", { name: "Clear temperature" }).click();
  await page.getByRole("button", { name: "Save changes" }).click();
  await expect(page.getByRole("status")).toContainText("Changes saved");
  expect(
    saved.settings.model_generation["fixture/model"].temperature,
  ).toBeUndefined();
});
