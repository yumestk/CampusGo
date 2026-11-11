import {test,expect} from '@playwright/test';
import type {Page} from '@playwright/test';
import {randomUUID} from 'node:crypto';
import {appendFileSync} from 'node:fs';
import {resolve} from 'node:path';

test.skip(process.env.CAMPUSGO_D8_BROWSER!=='1','requires isolated business fixtures and cleanup guard');
const names:Record<string,RegExp>={'1001':/发单同学/,'2001':/跑腿同学 A/,'2002':/跑腿同学 B/,'9001':/平台仲裁员/};
async function login(page:Page,id='1001'){
 await page.goto('/');await expect(page.getByRole('button',{name:'退出登录'}).or(page.getByRole('button',{name:/发单同学/}))).toBeVisible();
 if(await page.getByRole('button',{name:'退出登录'}).isVisible())await page.getByRole('button',{name:'退出登录'}).click();
 await page.getByRole('button',{name:names[id]}).click();await expect(page.getByRole('button',{name:'退出登录'})).toBeVisible();
}
async function token(page:Page,id:string){const r=await page.request.post('/api/auth/login',{data:{userId:id}});expect(r.status()).toBe(200);return(await r.json()).data.token as string;}
async function post(page:Page,token:string,path:string,data:unknown,key?:string){const r=await page.request.post(path,{headers:{Authorization:`Bearer ${token}`,...(key?{'Idempotency-Key':key}:{})},data});expect(r.status(),await r.text()).toBe(200);return(await r.json()).data;}
async function publish(page:Page,token:string,title:string){const key=randomUUID();appendFileSync(resolve('../work/d5-browser-keys.jsonl'),JSON.stringify({userId:'1001',key})+'\n');return(await post(page,token,'/api/errands',{campusId:'1',type:'OTHER',title,rewardCents:'101',slotTotal:1},key)).errandId as string;}

test('F5 funded task states expose exactly server-authorized actions for four roles',async({page})=>{
 test.setTimeout(180000);
 const tokens:Record<string,string>={};for(const user of Object.keys(names))tokens[user]=await token(page,user);
 const labels:Record<string,string>={CONFIRM:'确认接单',PICK_UP:'确认取货',DELIVER:'确认送达',DISPUTE:'发起争议',CANCEL:'取消并退款',SETTLE:'确认完成并结算',ARBITRATE_RUNNER:'裁决支持跑腿',ARBITRATE_PUBLISHER:'裁决支持发单人'};
 const action=(id:string,a:string,user:string)=>post(page,tokens[user],`/api/errands/${id}/actions`,{expectedRound:0,action:a},randomUUID());
 try{
  for(const state of ['PUBLISHED','LOCKED','ACCEPTED','PICKED_UP','DELIVERED','DISPUTED','SETTLED','CANCELLED','REFUNDED']){
   const id=await publish(page,tokens['1001'],`D5页面验证:F5角色矩阵${state}`);
   if(!['PUBLISHED','CANCELLED'].includes(state)){
    const grab=await post(page,tokens['2001'],`/api/errands/${id}/grab`,{expectedRound:0},randomUUID());expect(grab.state).toBe('SUCCEEDED');
    if(state!=='LOCKED')await post(page,tokens['2001'],`/api/errands/${id}/confirm`,{expectedRound:0});
    if(['PICKED_UP','DELIVERED','SETTLED'].includes(state))await action(id,'PICK_UP','2001');
    if(['DELIVERED','SETTLED'].includes(state))await action(id,'DELIVER','2001');
    if(['DISPUTED','REFUNDED'].includes(state))await action(id,'DISPUTE','2001');
    if(state==='SETTLED')await action(id,'SETTLE','1001');
    if(state==='REFUNDED')await action(id,'ARBITRATE_PUBLISHER','9001');
   }
   if(state==='CANCELLED')await action(id,'CANCEL','1001');
   for(const user of Object.keys(names)){
    await login(page,user);await page.goto(`/#/detail/${id}`);await expect(page.locator(`.status-${state}`)).toBeVisible();
    const response=await page.request.get(`/api/errands/${id}`,{headers:{Authorization:`Bearer ${tokens[user]}`}});expect(response.status()).toBe(200);const card=(await response.json()).data;
    for(const [action,label] of Object.entries(labels))await expect(page.getByRole('button',{name:label,exact:true})).toHaveCount(card.availableActions.includes(action)?1:0);
    await expect(page.getByRole('button',{name:state==='LOCKED'?'尝试候选登记':'立即抢单',exact:true})).toHaveCount(card.availableActions.includes('GRAB')?1:0);
    await expect(page.getByRole('heading',{name:'赏金托管记录',exact:true})).toHaveCount(['PUBLISHER','ARBITRATOR'].includes(card.role)?1:0);
    if(card.availableActions.includes('CONFIRM'))await expect(page.locator('.confirm-panel time')).toBeVisible();
   }
   // Release each in-flight fixture before creating the next, respecting runner limits.
   if(state==='PUBLISHED')await action(id,'CANCEL','1001');
   if(state==='LOCKED')await post(page,tokens['2001'],`/api/errands/${id}/confirm`,{expectedRound:0});
   if(['LOCKED','ACCEPTED'].includes(state))await action(id,'PICK_UP','2001');
   if(['LOCKED','ACCEPTED','PICKED_UP'].includes(state))await action(id,'DELIVER','2001');
   if(['LOCKED','ACCEPTED','PICKED_UP','DELIVERED'].includes(state))await action(id,'SETTLE','1001');
   if(state==='DISPUTED')await action(id,'ARBITRATE_RUNNER','9001');
  }
 }finally{for(const t of Object.values(tokens))await page.request.post('/api/auth/logout',{headers:{Authorization:`Bearer ${t}`}});}
});

test('F5 square pagination failures preserve loaded cards and full 64-character titles',async({page})=>{
 test.setTimeout(90000);const t=await token(page,'1001');const marker=`F5${randomUUID().slice(0,8)}`;const title=(`D5页面验证:${marker}`).padEnd(64,'长');
 try{for(let i=0;i<22;i++)await publish(page,t,i===21?title:`D5页面验证:${marker}分页${i}`);}finally{await page.request.post('/api/auth/logout',{headers:{Authorization:`Bearer ${t}`}});}
 await login(page);await page.goto('/#/square');await expect(page.getByRole('button',{name:'加载更多任务'})).toBeVisible();const before=await page.locator('.errand-card').count();
 await page.route('**/api/errands?**',r=>r.fulfill({status:503,contentType:'application/json',body:JSON.stringify({code:'DEPENDENCY_UNAVAILABLE',message:'验收分页失败',data:null})}));
 await page.getByRole('button',{name:'加载更多任务'}).click();await expect(page.getByRole('alert')).toContainText('已保留');await expect(page.locator('.errand-card')).toHaveCount(before);
 await page.unroute('**/api/errands?**');await page.getByRole('button',{name:'加载更多任务'}).click();await expect.poll(()=>page.locator('.errand-card').count()).toBeGreaterThan(before);
 await expect(page.getByRole('link',{name:title,exact:true})).toHaveText(title);
 const ids=await page.locator('.card-number').allTextContents();expect(new Set(ids).size).toBe(ids.length);
 await page.getByRole('textbox',{name:'搜索任务'}).fill(marker);await page.getByRole('link',{name:title,exact:true}).click();await expect(page.getByRole('heading',{name:title,exact:true})).toBeVisible();await page.getByRole('link',{name:'返回任务广场'}).click();await expect(page.getByRole('textbox',{name:'搜索任务'})).toHaveValue(marker);
});

test('F5 first-load failures have retry paths and never display false zero balances',async({page})=>{
 await login(page);
 for(const [route,endpoint,refresh] of [['wallet','**/api/wallet**','刷新钱包'],['credit','**/api/credit/**','刷新信用'],['notifications','**/api/notifications?*','刷新通知'],['square','**/api/errands?**','刷新任务']]){
  await page.route(endpoint,r=>r.fulfill({status:503,contentType:'application/json',body:JSON.stringify({code:'DEPENDENCY_UNAVAILABLE',message:'验收首次读取失败',data:null})}));
  await page.goto(`/#/${route}`);await expect(page.getByRole('alert')).toBeVisible();
  if(route==='wallet')await expect(page.getByTestId('wallet-available')).toHaveText('—');
  if(route==='credit')await expect(page.getByTestId('credit-score')).toHaveText('—分');
  await page.unroute(endpoint);await page.getByRole('button',{name:refresh,exact:true}).click();await expect(page.getByRole('alert')).toHaveCount(0);
 }
});

test('F5 text enlargement, reduced motion and keyboard focus remain usable',async({page})=>{
 await login(page);await page.setViewportSize({width:375,height:844});await page.emulateMedia({reducedMotion:'reduce'});
 for(const route of ['square','mine','publish','wallet','credit','notifications']){
  await page.goto(`/#/${route}`);await page.addStyleTag({content:'html { font-size: 200% !important; }'});
  await expect.poll(()=>page.evaluate(()=>document.documentElement.scrollWidth)).toBeLessThanOrEqual(375);
  const nav=page.getByRole('navigation',{name:'底部导航'});await expect(nav.getByRole('link')).toHaveCount(5);
  const header=page.locator('.topbar');const logout=header.getByRole('button',{name:'退出登录'});await expect(logout).toBeVisible();
  const box=await logout.boundingBox();expect(box!.x).toBeGreaterThanOrEqual(0);expect(box!.x+box!.width).toBeLessThanOrEqual(375);
  expect(await page.locator('.btn').first().evaluate(e=>getComputedStyle(e).transitionDuration)).toBe('0s');
 }
 await page.goto('/#/square');await expect(async()=>{await expect(page.getByRole('button',{name:'刷新任务'})).toBeEnabled();await page.getByRole('textbox',{name:'搜索任务'}).press('Tab');await expect(page.getByRole('button',{name:'刷新任务'})).toBeFocused({timeout:1000});}).toPass({timeout:10000,intervals:[200]});expect(await page.getByRole('button',{name:'刷新任务'}).evaluate(e=>getComputedStyle(e).outlineWidth)).toBe('3px');
});


test('F5 invalid publish fields expose associated errors and focus without writing',async({page})=>{
 await login(page);await page.goto('/#/publish');let writes=0;page.on('request',r=>{if(r.method()==='POST'&&new URL(r.url()).pathname==='/api/errands')writes++;});
 await page.getByLabel('任务标题').fill('D5页面验证:F5校验');await page.getByLabel('悬赏金额（元）').fill('1.001');await page.getByRole('button',{name:'托管赏金并发布'}).click();
 await expect(page.getByLabel('悬赏金额（元）')).toHaveAttribute('aria-invalid','true');await expect(page.getByLabel('悬赏金额（元）')).toBeFocused();await expect(page.locator('#reward-error')).toContainText('最多两位小数');
 await page.getByLabel('悬赏金额（元）').fill('1.00');await page.getByLabel('任务标题').fill('长'.repeat(65));await page.getByRole('button',{name:'托管赏金并发布'}).click();
 await expect(page.getByLabel('任务标题')).toHaveAttribute('aria-invalid','true');await expect(page.getByLabel('任务标题')).toBeFocused();expect(writes).toBe(0);
});
