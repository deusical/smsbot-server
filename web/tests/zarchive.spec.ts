import { test, expect, type Page } from "@playwright/test";
import { readFileSync } from "node:fs";
const headers = { Origin: "http://127.0.0.1:8788" };
async function setup(page: Page) {
 const c = JSON.parse(readFileSync(".test-state/connection.json", "utf8"));
 await page.goto("/");
 await page.getByLabel("Owner credential").fill(readFileSync(c.owner,"utf8"));
 await page.getByRole("button",{name:"Sign in",exact:true}).click();
 await expect(page.getByRole("heading",{name:"Home",exact:true})).toBeVisible();
 const cfg = await (await page.request.get("/api/v1/inbox/settings")).json();
 expect((await page.request.put("/api/v1/inbox/settings",{headers,data:{...cfg,enabled:true,scope:"selected",conversations:[{conversation:"demo-chat",participant:"demo-peer"}],archive_retention_days:0,load_older:false,automatic_replies:false}})).ok()).toBeTruthy();
 return cfg;
}
async function open(page:Page){
 await page.getByRole("button",{name:"Conversations",exact:true}).click();
 await page.locator(".conversation").filter({hasText:"Demo conversation"}).getByRole("button",{name:/Demo conversation/}).click();
}
async function emit(page:Page,id:string,text:string){
 expect((await page.request.post("/api/v1/fixture/incoming",{headers,data:{id,conversation_id:"demo-chat",participant_id:"demo-peer",text}})).ok()).toBeTruthy();
}
test("local changes show edits and a burst beyond one latest page",async({page})=>{
 const previous=await setup(page);
 try{
  await emit(page,"archive-original","Archive original text");
  await page.reload();await open(page);
  await expect(page.locator(".bubble").filter({hasText:"Archive original text"})).toBeVisible();
  for(let i=0;i<115;i++)await emit(page,`archive-burst-${i}`,`Archive burst item ${i}`);
  await emit(page,"archive-original","Archive edited text");
  await expect(page.locator(".bubble").filter({hasText:"Archive edited text"})).toHaveCount(1,{timeout:10000});
  await expect(page.locator(".bubble").filter({hasText:"Archive original text"})).toHaveCount(0);
  await expect(page.locator(".bubble").filter({hasText:/^Archive burst item /})).toHaveCount(115,{timeout:10000});
  // Stop collection; owner-readable archive remains available after reloading.
  const cfg=await(await page.request.get("/api/v1/inbox/settings")).json();
  await page.request.put("/api/v1/inbox/settings",{headers,data:{...cfg,enabled:false}});
  await page.reload();await open(page);
  await expect(page.getByRole("heading",{name:"Demo conversation",exact:true})).toBeVisible();
 }finally{
  const cfg=await(await page.request.get("/api/v1/inbox/settings")).json();
  await page.request.put("/api/v1/inbox/settings",{headers,data:{...previous,revision:cfg.revision}});
 }
});
test("large loaded archive bounds rendered rows",async({page})=>{
 const previous=await setup(page);
 const messages=Array.from({length:2000},(_,i)=>({id:`archive-long-${i}`,direction:"incoming",text:`Archive long row ${i}`,at:new Date(1700000000000+i*1000).toISOString()}));
 await page.route("**/api/v1/inbox/conversations/demo-chat/history?*",route=>route.fulfill({json:{messages,next_cursor:"",change_cursor:0}}));
 await page.route("**/api/v1/inbox/conversations/demo-chat/changes?*",route=>route.fulfill({json:{messages:[],cursor:0,more:false}}));
 try{
  await page.reload();await open(page);
  await expect(page.locator(".bubble").first()).toBeVisible();
  expect(await page.locator(".bubble").count()).toBeLessThan(100);
  await page.locator(".messages").evaluate(el=>{el.scrollTop=0});
  await expect(page.getByText("Archive long row 0",{exact:true})).toBeVisible();
  expect(await page.locator(".bubble").count()).toBeLessThan(100);
 }finally{
  const cfg=await(await page.request.get("/api/v1/inbox/settings")).json();
  await page.request.put("/api/v1/inbox/settings",{headers,data:{...previous,revision:cfg.revision}});
 }
});


test("enrolled archived conversation composes with collection stopped",async({page})=>{
 const previous=await setup(page);
 const before=await(await page.request.get("/api/v1/conversations")).json();
 const wasEnrolled=before.find((c:{id:string,enrolled:boolean})=>c.id==="demo-chat")?.enrolled;
 try{
  await emit(page,"archive-stopped-compose","Synthetic archived text");
  let cfg=await(await page.request.get("/api/v1/inbox/settings")).json();
  await page.request.put("/api/v1/inbox/settings",{headers,data:{...cfg,enabled:false}});
  await page.request.post("/api/v1/enrollment",{headers,data:{conversation_id:"demo-chat",participant_id:"demo-peer"}});
  await page.request.post("/api/v1/pause",{headers,data:{paused:false}});
  await page.reload();await open(page);
  await page.getByLabel("Message",{exact:true}).fill("Synthetic enrolled archive reply");
  const sending=page.waitForRequest("**/api/v1/conversations/demo-chat/compose");
  await page.getByRole("button",{name:"Send message",exact:true}).click();
  await sending;
  await expect(page.locator(".bubble.outgoing").filter({hasText:"Synthetic enrolled archive reply"})).toBeVisible({timeout:10000});
 }finally{
  const cfg=await(await page.request.get("/api/v1/inbox/settings")).json();
  await page.request.put("/api/v1/inbox/settings",{headers,data:{...previous,revision:cfg.revision}});
  await page.request.post("/api/v1/pause",{headers,data:{paused:true}});
  if(!wasEnrolled)await page.request.delete("/api/v1/enrollment/demo-chat",{headers});
 }
});

test("lost inbox acknowledgement keeps its route and key after collection stops",async({page})=>{
 const previous=await setup(page);
 const before=await(await page.request.get("/api/v1/conversations")).json();
 const wasEnrolled=before.find((c:{id:string,enrolled:boolean})=>c.id==="demo-chat")?.enrolled;
 let legacyCalls=0;const keys:string[]=[];
 await page.route("**/api/v1/conversations/demo-chat/compose",route=>{legacyCalls++;return route.continue()});
 await page.route("**/api/v1/inbox/conversations/demo-chat/compose",async route=>{
  keys.push(route.request().postDataJSON().request_id);
  const response=await route.fetch();expect(response.ok()).toBeTruthy();
  if(keys.length===1)await route.abort("failed");else await route.fulfill({response});
 });
 try{
  await page.request.post("/api/v1/enrollment",{headers,data:{conversation_id:"demo-chat",participant_id:"demo-peer"}});
  await page.request.post("/api/v1/pause",{headers,data:{paused:false}});
  await page.reload();await open(page);
  await page.getByLabel("Message",{exact:true}).fill("Synthetic pinned-route reply");
  await page.getByRole("button",{name:"Send message",exact:true}).click();
  await expect(page.getByRole("alert").filter({hasText:"Your text and files are kept"})).toBeVisible();
  const cfg=await(await page.request.get("/api/v1/inbox/settings")).json();
  await page.request.put("/api/v1/inbox/settings",{headers,data:{...cfg,enabled:false}});
  await page.getByRole("button",{name:"Send message",exact:true}).click();
  await expect(page.getByLabel("Message",{exact:true})).toHaveValue("");
  expect(keys).toHaveLength(2);expect(keys[1]).toBe(keys[0]);expect(legacyCalls).toBe(0);
 }finally{
  const cfg=await(await page.request.get("/api/v1/inbox/settings")).json();
  await page.request.put("/api/v1/inbox/settings",{headers,data:{...previous,revision:cfg.revision}});
  await page.request.post("/api/v1/pause",{headers,data:{paused:true}});
  if(!wasEnrolled)await page.request.delete("/api/v1/enrollment/demo-chat",{headers});
 }
});


test("archive feed serializes with held older pages and never polls stale head snapshots",async({page})=>{
 const previous=await setup(page);
 let heads=0,changes=0;
 let release!:()=>void;
 const held=new Promise<void>(resolve=>{release=resolve});
 const message=(id:string,text:string)=>({id,direction:"incoming",text,at:"2026-01-01T00:00:00Z"});
 const stale=[message("race-edit","Synthetic stale edit"),message("race-delete","Synthetic deleted row")];
 await page.route("**/api/v1/inbox/conversations/demo-chat/history?*",async route=>{
  const older=new URL(route.request().url()).searchParams.has("cursor");
  if(older)await held;else heads++;
  await route.fulfill({json:{messages:stale,next_cursor:older?"":"older",change_cursor:0}});
 });
 await page.route("**/api/v1/inbox/conversations/demo-chat/changes?*",async route=>{
  changes++;
  await route.fulfill({json:{messages:[{...message("race-edit","Synthetic current edit"),revision:1}],deleted:["race-delete"],cursor:2,more:false}});
 });
 try{
  await page.reload();await open(page);
  await expect(page.getByText("Synthetic stale edit",{exact:true})).toBeVisible();
  const paging=page.waitForRequest(request=>request.url().includes("/demo-chat/history?")&&new URL(request.url()).searchParams.has("cursor"));
  await page.getByRole("button",{name:"Load older messages",exact:true}).click();await paging;
  // A poll during a pending page must defer its change cursor advancement.
  await page.waitForTimeout(3400);
  expect(changes).toBe(0);
  release();
  await expect(page.getByText("Synthetic current edit",{exact:true})).toBeVisible({timeout:10000});
  await expect(page.getByText("Synthetic stale edit",{exact:true})).toHaveCount(0);
  await expect(page.getByText("Synthetic deleted row",{exact:true})).toHaveCount(0);
  await expect.poll(()=>changes,{timeout:10000}).toBeGreaterThan(1);
  expect(heads).toBe(1);
  await expect(page.getByText("Synthetic current edit",{exact:true})).toBeVisible();
 }finally{
  release();
  const cfg=await(await page.request.get("/api/v1/inbox/settings")).json();
  await page.request.put("/api/v1/inbox/settings",{headers,data:{...previous,revision:cfg.revision}});
 }
});


test("archive exports select readable format, download and delete the package",async({page})=>{
 const previous=await setup(page);
 try{
  await emit(page,"export-ui-message","Synthetic readable export message");
  await page.reload();
  await page.getByRole("button",{name:"Settings",exact:true}).click();
  await page.getByRole("button",{name:"Import & export",exact:true}).click();
  await expect(page.getByRole("heading",{name:"Original file archive",exact:true})).toBeVisible();
  await page.getByLabel("Archive export format").selectOption("plaintext");
  await page.getByRole("button",{name:"Start message export",exact:true}).click();
  await expect(page.getByRole("link",{name:"Download message export",exact:true})).toBeVisible({timeout:15000});
  await expect(page.getByRole("link",{name:"Download message export",exact:true})).toHaveAttribute("download","messages-export.tar");
  const file=await page.request.get(await page.getByRole("link",{name:"Download message export",exact:true}).getAttribute("href")||"");
  expect(file.ok()).toBeTruthy();
  expect((await file.body()).includes(Buffer.from("manifest.json"))).toBeTruthy();
  expect((await file.body()).includes(Buffer.from("Synthetic readable export message"))).toBeTruthy();
  await page.getByRole("button",{name:"Delete server export package",exact:true}).click();
  await expect(page.getByRole("link",{name:"Download message export",exact:true})).toHaveCount(0);
  await page.getByLabel("Archive export format").selectOption("encrypted");
  await page.getByRole("button",{name:"Start message export",exact:true}).click();
  await expect(page.getByRole("link",{name:"Download message export",exact:true})).toHaveAttribute("download","messages-archive.sma",{timeout:15000});
  await page.getByRole("button",{name:"Delete server export package",exact:true}).click();
 }finally{
  const cfg=await(await page.request.get("/api/v1/inbox/settings")).json();
  await page.request.put("/api/v1/inbox/settings",{headers,data:{...previous,revision:cfg.revision}});
 }
});
