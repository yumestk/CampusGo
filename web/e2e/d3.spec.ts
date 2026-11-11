import {test,expect} from '@playwright/test';
import type {Page} from '@playwright/test';
import {resolve} from 'node:path';
test.skip(process.env.CAMPUSGO_D3_BROWSER!=='1','run make verify-d3 against isolated verification fixtures');
const id=(n:number)=>`900719925474130${n}`;
async function demo(page:Page,n:number){
 await page.goto('/');await page.getByRole('button',{name:/跑腿同学 A/}).click();await expect(page.getByRole('button',{name:'退出登录'})).toBeVisible();await page.goto(`/#/detail/${id(n)}`);await expect(page.getByRole('heading',{name:/D3页面验证:/})).toBeVisible();await expect(page.getByRole('button',{name:'立即抢单'})).toBeVisible();
}
async function operation(page:Page,n:number,user='2001'){return page.evaluate(({n,user})=>JSON.parse(localStorage.getItem(`campusgo_grab:${user}:900719925474130${n}`)!),{n,user});}
test('grab reaches LOCKED with timeline and runner history; publisher cannot grab own task',async({page})=>{
 await demo(page,1);await page.getByRole('button',{name:'立即抢单'}).click();await expect(page.getByRole('heading',{name:'已抢到，等待确认'})).toBeVisible();await expect(page.getByText('待抢单 → 待确认')).toBeVisible();
 await page.getByRole('link',{name:'查看我的任务'}).click();await page.getByRole('button',{name:'我接单的'}).click();await expect(page.getByRole('link',{name:'D3页面验证:正常抢单',exact:true})).toBeVisible();
 await page.getByRole('button',{name:'退出登录'}).click();await page.getByRole('button',{name:/发单同学/}).click();await expect(page.getByRole('button',{name:'退出登录'})).toBeVisible();await page.goto(`/#/detail/${id(8)}`);await expect(page.getByRole('heading',{name:'D3页面验证:新轮次'})).toBeVisible();await expect(page.getByRole('button',{name:'立即抢单'})).toHaveCount(0);
});
test('committed response lost; reload queries original key without another write',async({page})=>{
 await demo(page,2);let writes=0,key='';await page.route(`**/api/errands/${id(2)}/grab`,async route=>{writes++;key=route.request().headers()['idempotency-key'];const response=await route.fetch();expect(response.status()).toBe(200);await route.abort('connectionreset');});
 await page.getByRole('button',{name:'立即抢单'}).click();await expect(page.getByRole('alert')).toContainText('网络连接失败');expect((await operation(page,2)).key).toBe(key);
 await page.unroute(`**/api/errands/${id(2)}/grab`);await page.reload();await expect(page.getByRole('heading',{name:'已抢到，等待确认'})).toBeVisible();expect((await operation(page,2)).key).toBe(key);expect(writes).toBe(1);
});
test('pre-write 503 retains key and original round through NOT_OBSERVED and retry',async({page})=>{
 await demo(page,3);let key='';await page.route(`**/api/errands/${id(3)}/grab`,async route=>{key=route.request().headers()['idempotency-key'];await route.fulfill({status:503,contentType:'application/json',body:JSON.stringify({code:'DEPENDENCY_UNAVAILABLE',message:'依赖暂不可用',data:null})});});
 await page.getByRole('button',{name:'立即抢单'}).click();await expect(page.getByRole('alert')).toContainText('依赖暂不可用');await page.getByRole('button',{name:'查询抢单结果'}).click();await expect(page.getByText(/暂未查到已提交结果/)).toBeVisible();
 const original=await operation(page,3);await page.unroute(`**/api/errands/${id(3)}/grab`);const next=page.waitForRequest(r=>r.method()==='POST'&&r.url().endsWith(`/api/errands/${id(3)}/grab`));await page.getByRole('button',{name:'重试原抢单'}).click();const request=await next;expect(request.headers()['idempotency-key']).toBe(key);expect(request.postDataJSON()).toEqual({expectedRound:original.expectedRound});await expect(page.getByRole('heading',{name:'已抢到，等待确认'})).toBeVisible();
});
test('late initial query cannot overwrite a successful retry',async({page})=>{
 const op={key:crypto.randomUUID(),errandId:id(4),campusId:'1',expectedRound:0};await page.addInitScript(op=>localStorage.setItem(`campusgo_grab:2001:${op.errandId}`,JSON.stringify(op)),op);
 let release!:()=>void;const pending=new Promise<void>(resolve=>{release=resolve;});let count=0;
 await page.route(`**/api/errands/${id(4)}/grab-requests/${op.key}`,async route=>{count++;await pending;await route.fulfill({status:202,contentType:'application/json',body:JSON.stringify({code:'OK',data:{requestKey:op.key,errandId:op.errandId,state:'NOT_OBSERVED',errorCode:'',appliedRound:0,retryAfterMillis:1000}})});});
 await page.goto('/');await page.getByRole('button',{name:/跑腿同学 A/}).click();await expect(page.getByRole('button',{name:'退出登录'})).toBeVisible();await page.goto(`/#/detail/${id(4)}`);await expect.poll(()=>count).toBeGreaterThan(0);
 await page.getByRole('button',{name:'重试原抢单'}).click();await expect(page.getByRole('heading',{name:'已抢到，等待确认'})).toBeVisible();const response=page.waitForResponse(r=>r.url().includes(op.key));release();await response;await expect(page.getByRole('heading',{name:'已抢到，等待确认'})).toBeVisible();
});
test('pending request remains scoped to the original runner',async({page})=>{
 await demo(page,5);await page.route(`**/api/errands/${id(5)}/grab`,route=>route.abort('connectionreset'));await page.getByRole('button',{name:'立即抢单'}).click();await expect(page.getByRole('alert')).toBeVisible();const original=await operation(page,5);
 await page.getByRole('button',{name:'退出登录'}).click();await page.getByRole('button',{name:/跑腿同学 B/}).click();await expect(page.getByRole('button',{name:'立即抢单'})).toBeVisible();expect(await operation(page,5,'2002')).toBeNull();expect((await operation(page,5)).key).toBe(original.key);
 await page.getByRole('button',{name:'退出登录'}).click();await page.getByRole('button',{name:/跑腿同学 A/}).click();await expect(page.getByRole('heading',{name:'抢单结果待确认'})).toBeVisible();expect((await operation(page,5)).key).toBe(original.key);
});
test('mobile grab layout and successful recovery',async({page})=>{
 await page.setViewportSize({width:390,height:844});await demo(page,6);expect(await page.evaluate(()=>document.documentElement.scrollWidth)).toBeLessThanOrEqual(390);
 await page.getByRole('button',{name:'立即抢单'}).click();await expect(page.getByRole('heading',{name:'已抢到，等待确认'})).toBeVisible();expect(await page.evaluate(()=>document.documentElement.scrollWidth)).toBeLessThanOrEqual(390);
 if(process.env.CAMPUSGO_VISUAL==='1')await page.screenshot({path:resolve('../work/d3-grab-mobile.png'),fullPage:true});
});
test('PROCESSING renders query controls and later resolves the durable result',async({page})=>{
 await demo(page,7);await page.route(`**/api/errands/${id(7)}/grab`,async route=>{const response=await route.fetch();expect(response.status()).toBe(200);const data=(await response.json()).data;await route.fulfill({status:202,contentType:'application/json',body:JSON.stringify({code:'OK',data:{...data,state:'PROCESSING',retryAfterMillis:1000}})});});
 await page.getByRole('button',{name:'立即抢单'}).click();await expect(page.getByRole('heading',{name:'抢单结果待确认'})).toBeVisible();await page.getByRole('button',{name:'查询抢单结果'}).click();await expect(page.getByRole('heading',{name:'已抢到，等待确认'})).toBeVisible();
});
test('durable 409 rejection survives reload; a new request needs an explicit user action',async({page})=>{
 const op={key:crypto.randomUUID(),errandId:id(8),campusId:'1',expectedRound:1};await page.addInitScript(op=>{if(!sessionStorage.getItem('d3-initialized')){localStorage.setItem(`campusgo_grab:2001:${op.errandId}`,JSON.stringify(op));sessionStorage.setItem('d3-initialized','1');}},op);
 await page.goto('/');await page.getByRole('button',{name:/跑腿同学 A/}).click();await expect(page.getByRole('button',{name:'退出登录'})).toBeVisible();await page.goto(`/#/detail/${id(8)}`);await expect(page.getByRole('button',{name:'重试原抢单'})).toBeVisible();
 await page.getByRole('button',{name:'重试原抢单'}).click();await expect(page.getByRole('heading',{name:'本次未抢到'})).toBeVisible();expect((await operation(page,8)).key).toBe(op.key);
 await page.reload();await expect(page.getByRole('heading',{name:'本次未抢到'})).toBeVisible();expect((await operation(page,8)).key).toBe(op.key);
 await page.getByRole('button',{name:'准备重新抢单'}).click();await page.getByRole('button',{name:'立即抢单'}).click();await expect(page.getByRole('heading',{name:'已抢到，等待确认'})).toBeVisible();expect((await operation(page,8)).key).not.toBe(op.key);expect((await operation(page,8)).expectedRound).toBe(0);
});
