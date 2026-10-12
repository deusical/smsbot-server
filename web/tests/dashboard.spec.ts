import {test,expect,type Page} from '@playwright/test';
import {readFileSync} from 'node:fs';
async function login(page:Page){const descriptor=JSON.parse(readFileSync('.test-state/connection.json','utf8'));const token=readFileSync(descriptor.owner,'utf8');await page.goto('/');await page.getByLabel('Owner credential').fill(token);await page.getByRole('button',{name:'Open dashboard'}).click();await expect(page.getByRole('heading',{name:'Home',exact:true})).toBeVisible()}
test('authenticated command journey and credential revocation',async({page})=>{
 await login(page);await page.getByRole('button',{name:'Conversations',exact:true}).click();
 const demo=page.locator('article.conversation').filter({hasText:'Demo conversation'});await demo.getByRole('button',{name:'Enroll',exact:true}).click();await expect(demo.getByRole('button',{name:'Remove'})).toBeVisible();
 await page.getByRole('button',{name:'Enable bot'}).click();await expect(page.getByRole('button',{name:'Pause bot'})).toBeVisible();
 await demo.getByRole('button',{name:/Demo conversation/}).click();await page.getByRole('button',{name:'Simulate !ping'}).click();
 await expect(page.locator('.bubble.outgoing')).toContainText('pong');
 await page.getByRole('button',{name:'Activity',exact:true}).click();await expect(page.locator('summary').first()).toContainText('submitted');
 await page.getByRole('button',{name:'Settings',exact:true}).click();await page.getByRole('button',{name:'Authorize Android client'}).click();await expect(page.getByLabel('New client credential')).toBeVisible();
 const token=await page.getByLabel('New client credential').inputValue();const before=await page.request.get('/api/v1/state',{headers:{Authorization:`Bearer ${token}`}});expect(before.status()).toBe(200);
 await page.getByRole('button',{name:'Revoke',exact:true}).last().click();const after=await page.request.get('/api/v1/state',{headers:{Authorization:`Bearer ${token}`}});expect(after.status()).toBe(401);
 await page.getByRole('button',{name:'Sign out'}).click();await expect(page.getByLabel('Owner credential')).toBeVisible();
 expect(await page.evaluate(()=>localStorage.length)).toBe(0);
});
for(const settings of [{name:'mobile light',width:390,height:844,scheme:'light' as const},{name:'desktop dark',width:1440,height:1000,scheme:'dark' as const}]){
 test(settings.name+' layout',async({page})=>{await page.setViewportSize({width:settings.width,height:settings.height});await page.emulateMedia({colorScheme:settings.scheme});await login(page);for(const name of ['Home','Conversations','Commands','Activity','Settings']){await page.getByRole('button',{name,exact:true}).click();expect(await page.evaluate(()=>document.documentElement.scrollWidth<=window.innerWidth)).toBe(true)};await page.getByRole('button',{name:'Home',exact:true}).click();await page.screenshot({path:`test-results/${settings.name.replace(' ','-')}.png`,fullPage:true})});
}
test('unauthenticated API does not disclose state',async({request})=>{expect((await request.get('/api/v1/activity')).status()).toBe(401)});

test('slow conversation refresh does not overlap and failure is specific',async({page})=>{
 await login(page);
 let fail=true;let active=0;let maximum=0;let started=0;
 await page.route('**/api/v1/conversations',async route=>{
  active++;started++;maximum=Math.max(maximum,active);
  try{
   await new Promise(resolve=>setTimeout(resolve,3500));
   if(fail)await route.fulfill({status:503,contentType:'application/json',body:'{"error":"transport_unavailable"}'});
   else await route.continue();
  }finally{active--}
 });
 await expect(page.getByRole('alert')).toContainText('Could not refresh conversations.',{timeout:12000});
 expect(maximum).toBe(1);expect(started).toBeGreaterThan(0);
 await expect(page.getByRole('alert')).not.toContainText('Connection lost');
 fail=false;
 await expect(page.getByRole('alert')).toHaveCount(0,{timeout:12000});
 expect(maximum).toBe(1);
});

for(const heldEndpoint of ['conversations','state']){
 test(`pause button updates both ways with a pending ${heldEndpoint} poll`,async({page})=>{
  await login(page);
  const toggle=page.locator('header button');
  await expect(toggle).toBeEnabled();
  if(await toggle.textContent()==='Pause bot'){
   await toggle.click();await expect(toggle).toHaveText('Enable bot');await expect(toggle).toBeEnabled();
  }
  let release!:()=>void;let started!:()=>void;
  const held=new Promise<void>(resolve=>{release=resolve});
  const pending=new Promise<void>(resolve=>{started=resolve});
  await page.route(`**/api/v1/${heldEndpoint}`,async route=>{
   const response=await route.fetch();started();await held;await route.fulfill({response});
  },{times:1});
  try{
   await pending;
   await toggle.click();
   await expect(toggle).toHaveText('Pause bot');await expect(toggle).toBeEnabled();
   await expect(page.locator('.hero .pill')).toHaveText('ACTIVE');
   expect((await (await page.request.get('/api/v1/state')).json()).paused).toBe(false);
   if(heldEndpoint==='state'){
    // The captured paused=true response must not overwrite the pause response.
    const resumed=page.waitForResponse('**/api/v1/activity');release();await resumed;
    await expect(toggle).toHaveText('Pause bot');await expect(page.locator('.hero .pill')).toHaveText('ACTIVE');
   }
   await toggle.click();
   await expect(toggle).toHaveText('Enable bot');await expect(toggle).toBeEnabled();
   await expect(page.locator('.hero .pill')).toHaveText('PAUSED');
   expect((await (await page.request.get('/api/v1/state')).json()).paused).toBe(true);
  }finally{release()}
 });
}

test('rejected pause change leaves the displayed state unchanged',async({page})=>{
 await login(page);
 const toggle=page.locator('header button');await expect(toggle).toBeEnabled();
 if(await toggle.textContent()==='Pause bot'){
  await toggle.click();await expect(toggle).toHaveText('Enable bot');await expect(toggle).toBeEnabled();
 }
 await page.route('**/api/v1/pause',route=>route.fulfill({status:503,contentType:'application/json',body:'{"error":"transport_unavailable"}'}));
 await toggle.click();await expect(page.getByRole('alert')).toContainText('transport unavailable');
 await expect(toggle).toHaveText('Enable bot');await expect(page.locator('.hero .pill')).toHaveText('PAUSED');
 expect((await (await page.request.get('/api/v1/state')).json()).paused).toBe(true);
});
