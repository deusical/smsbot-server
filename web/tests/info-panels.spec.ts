import {test, expect, type Page} from "@playwright/test";
import {readFileSync} from "node:fs";
async function prepare(page: Page) {
  let archiveReads=0, mutations=0;
  const record={conversation:{id:"demo-chat"},state:"running",since:"",imported:0,oldest:"",last_sync:"2026-10-16T12:00:00Z",error:""};
  await page.route("**/api/v1/inbox/settings",async route=>{const response=await route.fetch();await route.fulfill({json:{...await response.json(),enabled:true,scope:"all",archive_retention_days:0}});});
  await page.route("**/api/v1/inbox/conversations",route=>route.fulfill({json:{conversations:[{id:"demo-chat",participant_id:"demo-peer",label:"Demo conversation",protocol:"RCS",writable:true,archived:true}],next_cursor:""}}));
  await page.route("**/api/v1/inbox/archive",route=>{archiveReads++;return route.fulfill({json:[record]});});
  await page.route("**/api/v1/inbox/conversations/demo-chat/refresh",route=>route.fulfill({json:record}));
  await page.route("**/api/v1/inbox/conversations/demo-chat/sync",route=>{mutations++;return route.fulfill({json:record});});
  await page.route("**/api/v1/inbox/conversations/demo-chat/history?*",route=>route.fulfill({json:{messages:[{id:"synthetic-notice-message",direction:"incoming",text:"Synthetic saved message",at:"2026-10-16T12:00:00Z"}],next_cursor:""}}));
  const descriptor=JSON.parse(readFileSync(".test-state/connection.json","utf8"));
  await page.goto("/");await page.getByLabel("Owner credential").fill(readFileSync(descriptor.owner,"utf8"));await page.getByRole("button",{name:"Sign in",exact:true}).click();
  const openThread=async()=>{
    await page.getByRole("button",{name:"Conversations",exact:true}).click();
    await page.locator(".conversation").filter({hasText:"Demo conversation"}).getByRole("button",{name:/Demo conversation/}).click();
  };
  await openThread();
  return {record,openThread,reads:()=>archiveReads,mutations:()=>mutations};
}
test("routine info stays hidden across polls, navigation and reload while errors remain visible",async({page})=>{
  const app=await prepare(page);
  await expect(page.getByRole("status",{name:"Background archive scope"})).toBeVisible();
  await page.getByRole("button",{name:"Hide archive info",exact:true}).click();
  await page.getByRole("button",{name:"Hide sync details",exact:true}).click();
  await expect(page.getByRole("status",{name:"Background archive scope"})).toBeHidden();
  await expect(page.getByRole("status",{name:"Recent message checks"})).toBeHidden();
  const before=app.reads();
  await expect.poll(app.reads).toBeGreaterThan(before);
  await expect(page.getByRole("button",{name:"Show sync details",exact:true})).toBeVisible();
  await page.getByRole("button",{name:"Home",exact:true}).click();await app.openThread();
  await expect(page.getByRole("button",{name:"Show archive info",exact:true})).toBeVisible();
  await expect(page.getByRole("button",{name:"Show sync details",exact:true})).toBeVisible();
  await page.reload();await app.openThread();
  await expect(page.getByRole("button",{name:"Show archive info",exact:true})).toBeVisible();
  await expect(page.getByRole("button",{name:"Show sync details",exact:true})).toBeVisible();
  await page.screenshot({path:".test-state/info-panels-collapsed.png"});
  app.record.error="history_fetch_failed";app.record.state="retrying";
  await expect(page.getByRole("alert")).toContainText("A history check failed");
  await expect(page.getByRole("button",{name:"Show sync details",exact:true})).toBeVisible();
  expect(app.mutations()).toBe(0);
  await page.getByRole("button",{name:"Review sync issue",exact:true}).click();
  await expect(page.getByRole("status",{name:"Recent message checks"})).toBeVisible();
  await page.getByRole("button",{name:"Show archive info",exact:true}).click();
  await expect(page.getByRole("status",{name:"Background archive scope"})).toBeVisible();
  await page.setViewportSize({width:390,height:844});
  const box=await page.locator(".messages").boundingBox();expect(box?.height).toBeGreaterThan(100);
});
test("dismissal still works when tab preference storage is unavailable",async({page})=>{
  await page.addInitScript(()=>Object.defineProperty(window,"sessionStorage",{get(){throw new Error("synthetic blocked storage");}}));
  const app=await prepare(page);
  await page.getByRole("button",{name:"Hide archive info",exact:true}).click();
  await page.getByRole("button",{name:"Hide sync details",exact:true}).click();
  await page.getByRole("button",{name:"Home",exact:true}).click();await app.openThread();
  await expect(page.getByRole("button",{name:"Show archive info",exact:true})).toBeVisible();
  await expect(page.getByRole("button",{name:"Show sync details",exact:true})).toBeVisible();
  expect(app.mutations()).toBe(0);
});
