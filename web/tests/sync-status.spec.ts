import {test, expect, type Page} from "@playwright/test";
import {readFileSync} from "node:fs";
async function open(page: Page) {
 const descriptor=JSON.parse(readFileSync(".test-state/connection.json","utf8"));
 await page.goto("/");await page.getByLabel("Owner credential").fill(readFileSync(descriptor.owner,"utf8"));
 await page.getByRole("button",{name:"Sign in",exact:true}).click();
 await page.getByRole("button",{name:"Conversations",exact:true}).click();
 await page.locator(".conversation").filter({hasText:"Demo conversation"}).getByRole("button",{name:/Demo conversation/}).click();
}
test("saved import date, independent progress and gap repair status stay understandable",async({page})=>{
 let gapState="queued",ready=true,reason="capture_delayed";
 let record={conversation:{id:"demo-chat"},state:"queued",since:"2026-07-16T07:00:00Z",requested_at:"2026-10-16T12:00:00Z",checked:0,pages:0,imported:0,updated:0,scanned_through:"",last_progress_at:"",last_sync:"2026-10-16T11:00:00Z",oldest:"",refresh_state:"checked_available",refresh_checked_at:"2026-10-16T12:00:00Z"};
 await page.route("**/api/v1/state",async route=>{
  const response=await route.fetch();if(!response.ok()){await route.fulfill({response});return;}
  await route.fulfill({json:{...await response.json(),transport_ready:ready,inbox_capture_gap:gapState!=="checked_available",inbox_capture_gaps:[{conversation:"demo-chat",since:"2026-10-16T11:50:00Z",until:"2026-10-16T12:00:00Z",state:gapState,reason,checked_at:gapState==="checked_available"?"2026-10-16T12:10:00Z":""}]}});
 });
 await page.route("**/api/v1/inbox/settings",async route=>{
  const response=await route.fetch();await route.fulfill({json:{...await response.json(),enabled:true,scope:"all",archive_retention_days:0}});
 });
 await page.route("**/api/v1/inbox/conversations",route=>route.fulfill({json:{conversations:[{id:"demo-chat",participant_id:"demo-peer",label:"Demo conversation",protocol:"RCS",writable:true,archived:true}],next_cursor:""}}));
 await page.route("**/api/v1/inbox/archive",route=>route.fulfill({json:[record]}));
 await page.route("**/api/v1/inbox/conversations/demo-chat/refresh",route=>route.fulfill({json:record}));
 await page.route("**/api/v1/inbox/conversations/demo-chat/sync",async route=>{
  const body=route.request().postDataJSON();record={...record,since:body.since,state:body.cancel?"cancelled":"queued"};await route.fulfill({json:record});
 });
 await page.route("**/api/v1/inbox/conversations/demo-chat/history?*", route => route.fulfill({json:{messages:Array.from({length:50},(_,i)=>({id:`synthetic-status-${i}`,direction:"incoming",text:`Synthetic saved message ${i}`,at:new Date(Date.UTC(2026,8,9,12,i)).toISOString()})),next_cursor:"synthetic-older"}}));
 await open(page);
 await page.getByRole("button",{name:"Archive",exact:true}).click();
 await expect(page.getByRole("status",{name:"Background archive scope"})).toContainText("All individual conversations");
 await page.getByText("Advanced import options",{exact:true}).click();
 await expect(page.getByLabel("History from",{exact:true})).toHaveValue("2026-07-16");
 await expect(page.getByRole("status",{name:"Import progress"})).toContainText("Waiting for a history worker turn");
 await expect(page.getByRole("status",{name:"Recent message checks"})).toContainText("last completed");
 await expect(page.getByText("Possible capture gap:",{exact:false})).toBeVisible();
 await page.getByLabel("History from",{exact:true}).fill("2026-06-01");
 await expect(page.getByText("Unsaved date selection.",{exact:false})).toBeVisible();
 await page.waitForTimeout(3200);
 await expect(page.getByLabel("History from",{exact:true})).toHaveValue("2026-06-01");
 await page.getByRole("button",{name:"Import from selected date",exact:true}).click();
 await expect(page.getByText("Unsaved date selection.",{exact:false})).toHaveCount(0);
 record={...record,state:"running",checked:100,pages:2,updated:4,scanned_through:"2026-08-01T12:00:00Z",last_progress_at:"2026-10-16T12:10:00Z"};
 gapState="checking";
 await expect(page.getByRole("status",{name:"Import progress"})).toContainText("100 messages checked across 2 pages; 0 newly saved; 4 updated");
 await expect(page.getByText("Checking available phone history for this range.",{exact:false})).toBeVisible();
 gapState="checked_available";
 await expect(page.getByText("Available history in this range rechecked",{exact:false})).toBeVisible();
 await expect(page.getByText("Some messages may have been missed",{exact:false})).toHaveCount(0);
 await page.getByRole("button",{name:"Cancel history import",exact:true}).click();
 await expect(page.getByRole("status",{name:"Import progress"})).toContainText("Import cancelled");
 ready=false;gapState="queued";reason="phone_unavailable";
 await expect(page.getByText("the phone was unavailable during live capture",{exact:false})).toBeVisible();
 await expect(page.getByText("Waiting for phone connection and enabled conversation access;",{exact:false})).toBeVisible();
 await page.setViewportSize({width:390,height:844});
 expect(await page.evaluate(()=>document.documentElement.scrollWidth<=window.innerWidth)).toBeTruthy();
});
