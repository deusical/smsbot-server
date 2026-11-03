import { test, expect, type Page } from "@playwright/test";
import { readFileSync } from "node:fs";
import type { Configuration, Conversation, Job, State } from "../src/lib/api";

async function login(page: Page) {
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
}

// A command built only from modules in the step editor, with no
// command-specific code: find the link, download, compress, caption, reply.
test("a command built from steps saves, previews and runs offline", async ({
  page,
}) => {
  await login(page);
  const headers = { Origin: new URL(page.url()).origin };
  const state = (await (
    await page.request.get("/api/v1/state")
  ).json()) as State;
  const target = (
    (await (await page.request.get("/api/v1/inbox/conversations")).json()) as {
      conversations: Conversation[];
    }
  ).conversations.find((c) => c.id === "demo-chat")!;
  try {
    await page.getByRole("button", { name: "Commands", exact: true }).click();
    await page.getByRole("button", { name: "New", exact: true }).click();
    await page.getByRole("menuitem", { name: /Blank/ }).click();
    await page.getByLabel("Command name").fill("grab");

    // Replace the starter text with the video steps.
    await page.getByRole("button", { name: "Remove step 1" }).click();
    const add = page.getByLabel("Add a step");
    for (const title of [
      "Find Douyin/Xiaohongshu link",
      "Download Douyin/Xiaohongshu video",
      "Compress video",
      "Text",
    ])
      await add.selectOption({ label: title });

    // Only compatible outputs are offered: a link can't feed compression.
    const original = page.getByLabel("Original video for step 3");
    await expect(original).toHaveValue("download.source");
    const offered = await original.locator("option").allTextContents();
    expect(offered.join("|")).not.toContain("Link");
    expect(offered.join("|")).not.toContain("Command");

    await page.getByLabel("Encoding preset").selectOption("fast");
    await page.getByLabel("Text", { exact: true }).fill("Here you go: {input}");
    await expect(page.getByLabel("Input for step 4")).toHaveValue(
      "extract_url.url",
    );

    // The reply still points at the removed starter step until it's rewired.
    const reply = page.getByRole("listitem", { name: /Step 5: reply/ });
    await expect(reply.getByRole("alert")).toContainText(
      "no longer comes before",
    );
    await page.getByLabel("Text for step 5").selectOption("template.text");
    await page.getByLabel("File for step 5").selectOption("compress.video");
    await expect(reply.getByRole("alert")).toHaveCount(0);

    await page.screenshot({
      path: "test-results/step-editor.png",
      fullPage: true,
    });
    await page.getByRole("switch", { name: "Enabled", exact: true }).click();
    await page.getByRole("button", { name: "Save changes" }).click();
    await expect(page.getByRole("status")).toContainText("Changes saved");
    const saved = (await (
      await page.request.get("/api/v1/config")
    ).json()) as Configuration;
    const grab = saved.commands.find((c) => c.name === "grab")!;
    expect(grab.kind).toBe("pipeline");
    expect(grab.pipeline!.steps.map((s) => s.module)).toEqual([
      "text.extract_url",
      "video.download",
      "video.compress",
      "text.template",
      "reply",
    ]);
    expect(grab.pipeline!.steps[2].settings).toMatchObject({ preset: "fast" });

    await page
      .getByLabel("Preview input")
      .fill("https://v.douyin.com/synthetic/");
    await page.getByRole("button", { name: "Preview", exact: true }).click();
    await expect(
      page
        .getByRole("status")
        .filter({ hasText: "Here you go: https://v.douyin.com/synthetic/" }),
    ).toBeVisible();
    await expect(
      page.getByRole("list", { name: "Preview steps" }),
    ).toContainText("compress");

    // Run it for real on the offline fixture.
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
            text: "!grab https://v.douyin.com/synthetic/",
          },
        })
      ).ok(),
    ).toBe(true);
    await expect
      .poll(
        async () =>
          (
            (await (await page.request.get("/api/v1/activity")).json()) as Job[]
          ).find((j) => j.command === "!grab")?.state,
        { timeout: 30000 },
      )
      .toBe("submitted");
    // Activity lists the request's steps, all finished.
    await page.getByRole("button", { name: "Activity", exact: true }).click();
    const row = page.locator(".feed-row").filter({ hasText: "!grab" }).first();
    await row.locator(".feed-main").click();
    const steps = row.getByRole("list", { name: "Steps" });
    await expect(steps).toContainText("Compress video");
    await expect(steps.locator("li.done")).toHaveCount(5);
    await page.screenshot({ path: "test-results/activity-steps.png" });
  } finally {
    await page.request.post("/api/v1/pause", {
      headers,
      data: { paused: true },
    });
    const current = (await (
      await page.request.get("/api/v1/config")
    ).json()) as Configuration;
    current.commands = current.commands.filter((c) => c.name !== "grab");
    await page.request.put("/api/v1/config", { headers, data: current });
    if (!target.enrolled)
      await page.request.delete("/api/v1/enrollment/" + target.id, { headers });
    if (!state.paused)
      await page.request.post("/api/v1/pause", {
        headers,
        data: { paused: false },
      });
  }
});

// A template command opens as the steps it already runs, and keeps working.
test("converting an AI command to steps keeps its reply threads", async ({
  page,
}) => {
  await login(page);
  const headers = { Origin: new URL(page.url()).origin };
  const original = (await (
    await page.request.get("/api/v1/config")
  ).json()) as Configuration;
  const seeded = structuredClone(original);
  seeded.commands = [
    ...seeded.commands.filter((c) => c.name !== "tutor"),
    {
      name: "tutor",
      description: "Synthetic tutor",
      instructions: "Teach briefly.",
      type: "ai",
      enabled: false,
      model: "",
      profile_id: "",
      memory: true,
      approval: false,
      allowed_conversations: [],
    },
  ];
  expect(
    (await page.request.put("/api/v1/config", { headers, data: seeded })).ok(),
  ).toBe(true);
  try {
    await page.getByRole("button", { name: "Commands", exact: true }).click();
    await page.getByRole("button", { name: /!tutor/ }).click();
    // It opens as the steps it already runs; editing a step stores it as steps.
    await expect(page.getByRole("list", { name: "Step flow" })).toContainText(
      "Recent memory",
    );
    await expect(page.getByLabel("Module for step 2")).toHaveValue(
      "context.rolling",
    );
    await expect(page.getByLabel("Instructions", { exact: true })).toHaveValue(
      "Teach briefly.",
    );
    await page
      .getByLabel("Instructions", { exact: true })
      .fill("Teach briefly and kindly.");

    await expect(
      page.getByLabel("Continues conversation for step 4"),
    ).toHaveValue("answer.context");
    await page.getByRole("button", { name: "Save changes" }).click();
    await expect(page.getByRole("status")).toContainText("Changes saved");
    const saved = (await (
      await page.request.get("/api/v1/config")
    ).json()) as Configuration;
    const tutor = saved.commands.find((c) => c.name === "tutor")!;
    expect(tutor.kind).toBe("pipeline");
    expect(tutor.memory).toBe(true);
    expect(tutor.pipeline!.steps.map((s) => s.module)).toEqual([
      "message.question",
      "context.rolling",
      "ai.chat",
      "reply",
    ]);
  } finally {
    const current = (await (
      await page.request.get("/api/v1/config")
    ).json()) as Configuration;
    current.commands = current.commands.filter((c) => c.name !== "tutor");
    await page.request.put("/api/v1/config", { headers, data: current });
  }
});
