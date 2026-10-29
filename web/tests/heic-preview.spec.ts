import { test, expect } from "@playwright/test";
import { readFileSync } from "node:fs";

// A 1x1 JPEG, standing in for what the server makes of a HEIC photo.
const jpeg = Buffer.from(
  "/9j/4AAQSkZJRgABAQEASABIAAD/2wBDAP//////////////////////////////////////////////////////////////////////////////////////wgALCAABAAEBAREA/8QAFBABAAAAAAAAAAAAAAAAAAAAAP/aAAgBAQABPxA=",
  "base64",
);

test("a HEIC photo is shown inline, from the preview the server makes", async ({ page }) => {
  const at = new Date().toISOString();
  const requested: string[] = [];
  await page.route("**/api/v1/state", async (route) => {
    const response = await route.fetch();
    if (!response.ok()) return route.fulfill({ response });
    await route.fulfill({ json: { ...(await response.json()), paused: false, transport_ready: true } });
  });
  await page.route("**/api/v1/inbox/settings", async (route) => {
    const response = await route.fetch();
    if (!response.ok()) return route.fulfill({ response });
    await route.fulfill({ json: { ...(await response.json()), enabled: true, scope: "all" } });
  });
  await page.route(/\/api\/v1\/inbox\/conversations(\?refresh=1)?$/, (route) =>
    route.fulfill({ json: { conversations: [{ id: "synthetic-chat", participant_id: "synthetic-peer", label: "Avery Quinn", protocol: "RCS", writable: true, archived: true }], next_cursor: "" } }),
  );
  await page.route("**/api/v1/inbox/conversations/*/history?*", (route) =>
    route.fulfill({
      json: {
        messages: [{ id: "photo", text: "", direction: "incoming", at, protocol: "RCS", attachments: [{ id: "part-0", name: "IMG_8862.heic", mime: "image/heic", size: 1999072, archive_state: "saved" }] }],
        next_cursor: "",
        change_cursor: 0,
      },
    }),
  );
  await page.route("**/api/v1/inbox/conversations/*/changes?*", (route) => route.fulfill({ json: { messages: [], cursor: 0, more: false } }));
  await page.route("**/api/v1/inbox/conversations/*/read", (route) => route.fulfill({ json: { ok: true } }));
  await page.route("**/attachments/part-0?*", (route) => {
    requested.push(route.request().url());
    return route.fulfill({ status: 200, contentType: "image/jpeg", body: jpeg });
  });
  const descriptor = JSON.parse(readFileSync(".test-state/connection.json", "utf8"));
  await page.goto("/");
  await page.getByLabel("Owner credential").fill(readFileSync(descriptor.owner, "utf8"));
  await page.getByRole("button", { name: "Sign in", exact: true }).click();
  await page.getByRole("button", { name: "Conversations", exact: true }).click();
  await page.locator(".conversation").filter({ hasText: "Avery Quinn" }).getByRole("button", { name: /Avery Quinn/ }).click();
  const photo = page.getByRole("img", { name: "IMG_8862.heic" });
  await expect(photo).toBeVisible();
  await expect.poll(() => requested.some((url) => url.includes("inline=1"))).toBe(true);
  await expect(photo).toHaveJSProperty("naturalWidth", 1);
});
