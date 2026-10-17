import {test, type Page} from "@playwright/test";
import {readFileSync} from "node:fs";

// Regenerates the documentation screenshots from synthetic data only:
//   DOCS_SCREENSHOTS=1 npx playwright test tests/docs-screenshots.spec.ts
test.skip(!process.env.DOCS_SCREENSHOTS, "set DOCS_SCREENSHOTS=1 to regenerate documentation screenshots");

const out = (name: string) => `../docs/images/${name}.png`;
const minutes = (n: number) => new Date(Date.now() - n * 60000).toISOString();

const people = [
  {id: "p1", label: "Avery Quinn", last: 2, unread: true, preview: "Are we still on for lunch?", fromMe: false},
  {id: "p2", label: "Sam Rivera", last: 14, pinned: true, preview: "Sounds good, thanks!", fromMe: true},
  {id: "p3", label: "Jordan Lee", last: 95, preview: "Sent you the photos", fromMe: false},
  {id: "p4", label: "Taylor Morgan", last: 60 * 26, preview: "See you Thursday", fromMe: true},
  {id: "p5", label: "Casey Brooks", last: 60 * 24 * 4, preview: "Happy birthday!", fromMe: false},
];

async function prepare(page: Page, options: {live?: boolean} = {}) {
  await page.route("**/api/v1/state", async route => {
    const response = await route.fetch();
    if (!response.ok()) return route.fulfill({response});
    await route.fulfill({json: {...await response.json(), paused: !options.live, transport_ready: true, conversations_revision: "docs"}});
  });
  await page.route("**/api/v1/inbox/settings", async route => {
    const response = await route.fetch();
    await route.fulfill({json: {...await response.json(), enabled: true, scope: "all", archive_retention_days: 0}});
  });
  await page.route("**/api/v1/inbox/conversations", route => route.fulfill({json: {conversations: people.map(p => ({id: p.id, participant_id: `peer-${p.id}`, label: p.label, protocol: "RCS", writable: true, archived: true, enrolled: p.id === "p1" || p.id === "p2", last_message_at: minutes(p.last), unread: p.unread, pinned: p.pinned, preview: p.preview, preview_from_me: p.fromMe})), next_cursor: ""}}));
  await page.route("**/api/v1/inbox/conversations/*/history?*", route => route.fulfill({json: {messages: [
    {id: "m1", direction: "outgoing", text: "Hey! Are you free later this week?", at: minutes(180), origin: "web", status: "read"},
    {id: "m2", direction: "incoming", text: "Yes, Thursday works for me.", at: minutes(170)},
    {id: "m3", direction: "outgoing", text: "Great, lunch at noon?", at: minutes(165), origin: "web", status: "delivered"},
    {id: "m4", direction: "incoming", text: "Are we still on for lunch?", at: minutes(2)},
  ], next_cursor: ""}}));
  await page.route("**/api/v1/activity", route => route.fulfill({json: [
    {id: "j1", conversation_id: "p1", command: "!ask", state: "awaiting_approval", created_at: minutes(1)},
    {id: "j2", conversation_id: "p2", command: "!status", state: "submitted", created_at: minutes(30)},
    {id: "j3", conversation_id: "p1", command: "!ping", state: "submitted", created_at: minutes(90)},
    {id: "j4", conversation_id: "p2", command: "!hello", state: "submitted", created_at: minutes(300)},
  ]}));
  await page.route("**/api/v1/inbox/archive", route => route.fulfill({json: [
    {conversation: {id: "p1", label: "Avery Quinn"}, state: "running", since: "", imported: 12, oldest: "", last_sync: minutes(1), checked: 240, pages: 6, updated: 3, scanned_through: minutes(60 * 24 * 40), last_progress_at: minutes(1)},
    {conversation: {id: "p2", label: "Sam Rivera"}, state: "available_pages_exhausted", since: "", imported: 410, oldest: "", last_sync: minutes(5), checked: 1850, pages: 38, updated: 17},
  ]}));
  await page.route("**/api/v1/inbox/archive/stats", route => route.fulfill({json: {original_bytes: 31457280, original_files: 42, database_bytes: 5242880, export_bytes: 0, temporary_bytes: 0, total_bytes: 36700160, messages: 2260, quota_bytes: 10737418240, measured_at: minutes(0)}}));
  await page.route("**/api/v1/inbox/archive/files/policy", route => route.fulfill({json: {enabled: true, max_file_mb: 0, quota_mb: 10240}}));
  await page.route("**/api/v1/inbox/archive/backup", route => route.request().method() === "GET" ? route.fulfill({json: null}) : route.fulfill({json: null}));
  const descriptor = JSON.parse(readFileSync(".test-state/connection.json", "utf8"));
  await page.goto("/");
  await page.getByLabel("Owner credential").fill(readFileSync(descriptor.owner, "utf8"));
  await page.getByRole("button", {name: "Sign in", exact: true}).click();
  await page.getByRole("heading", {name: "Home", exact: true}).waitFor();
}

const go = async (page: Page, name: string) => {
  await page.getByRole("button", {name, exact: true}).first().click();
  await page.waitForTimeout(700);
};

test("dark desktop screenshots", async ({page}) => {
  await page.setViewportSize({width: 1280, height: 800});
  await page.emulateMedia({colorScheme: "dark"});
  await prepare(page, {live: true});
  await page.screenshot({path: out("home")});
  await go(page, "Commands");
  await page.screenshot({path: out("commands")});
});

test("light desktop screenshots", async ({page}) => {
  await page.setViewportSize({width: 1280, height: 800});
  await page.emulateMedia({colorScheme: "light"});
  await prepare(page, {live: true});
  await go(page, "Conversations");
  await page.locator(".conversation").filter({hasText: "Avery Quinn"}).getByRole("button", {name: /Avery Quinn/}).click();
  await page.waitForTimeout(800);
  await page.screenshot({path: out("conversations")});
  await go(page, "Archive");
  await page.setViewportSize({width: 1280, height: 1100});
  await page.waitForTimeout(500);
  await page.screenshot({path: out("archive")});
});

test("mobile screenshot", async ({page}) => {
  await page.setViewportSize({width: 390, height: 800});
  await page.emulateMedia({colorScheme: "light"});
  await prepare(page, {live: true});
  await go(page, "Conversations");
  await page.screenshot({path: out("mobile")});
});
