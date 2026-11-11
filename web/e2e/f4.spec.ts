import {test,expect} from '@playwright/test';
import type {Page} from '@playwright/test';
import {randomUUID} from 'node:crypto';
import {appendFileSync} from 'node:fs';
import {resolve} from 'node:path';

test.skip(process.env.CAMPUSGO_D8_BROWSER!=='1','use isolated business verification and fixture cleanup');
async function login(page:Page){
 await page.goto('/');
 await expect(page.getByRole('button',{name:'退出登录'}).or(page.getByRole('button',{name:/发单同学/}))).toBeVisible();
 if(await page.getByRole('button',{name:'退出登录'}).isVisible())await page.getByRole('button',{name:'退出登录'}).click();
 await page.getByRole('button',{name:/发单同学/}).click();
 await expect(page.getByRole('button',{name:'退出登录'})).toBeVisible();
}
test('F4 real wallet and notice pagination retain unique task links',async({page})=>{
 test.setTimeout(90000);
 const auth=await page.request.post('/api/auth/login',{data:{userId:'1001'}});expect(auth.status()).toBe(200);
 const token=(await auth.json()).data.token;
 try{
  const headers={Authorization:`Bearer ${token}`};
  for(let i=0;i<22;i++){
   const key=randomUUID();appendFileSync(resolve('../work/d5-browser-keys.jsonl'),JSON.stringify({userId:'1001',key})+'\n');
   const response=await page.request.post('/api/errands',{headers:{...headers,'Idempotency-Key':key},data:{campusId:'1',type:'OTHER',title:`D5页面验证:F4分页${i}`,rewardCents:'101',slotTotal:1}});
   expect(response.status()).toBe(200);const id=(await response.json()).data.errandId;
   const cancel=await page.request.post(`/api/errands/${id}/actions`,{headers:{...headers,'Idempotency-Key':randomUUID()},data:{expectedRound:0,action:'CANCEL'}});expect(cancel.status()).toBe(200);
  }
 }finally{await page.request.post('/api/auth/logout',{headers:{Authorization:`Bearer ${token}`}});}
 await login(page);await page.goto('/#/wallet');
 await expect(page.getByRole('button',{name:'加载更早流水'})).toBeVisible();
 const first=await page.locator('.wallet-ledger li').count();
 await page.getByRole('button',{name:'加载更早流水'}).click();
 await expect.poll(()=>page.locator('.wallet-ledger li').count()).toBeGreaterThan(first);
 const ids=await page.locator('.wallet-ledger li .record-ref').allTextContents();expect(ids.length).toBeGreaterThan(20);
 await page.locator('.wallet-ledger a[href^="#/detail/"]').first().click();await expect(page.getByRole('heading',{name:/D5页面验证:/})).toBeVisible();
 await page.goto('/#/notifications');
 await expect(async()=>{await page.getByRole('button',{name:'刷新通知',exact:true}).click();await expect(page.getByRole('button',{name:'加载更早通知'})).toBeVisible();}).toPass({timeout:60000,intervals:[1000]});
 const before=await page.locator('.notice-item').count();await page.getByRole('button',{name:'加载更早通知'}).click();
 await expect.poll(()=>page.locator('.notice-item').count()).toBeGreaterThan(before);
 const refs=await page.locator('.notice-item .record-ref').allTextContents();expect(new Set(refs).size).toBe(refs.length);
 await page.locator('.notice-item a').first().click();await expect(page.getByRole('heading',{name:/D5页面验证:/})).toBeVisible();
});
test('F4 failed refresh preserves real records and retry restores availability',async({page})=>{
 await login(page);
 for(const [route,heading,refresh,endpoint,record] of [
  ['wallet','我的钱包','刷新钱包','**/api/wallet/**','.wallet-ledger li'],
  ['credit','信用中心','刷新信用','**/api/credit/**','.account-credit-score'],
  ['notifications','站内通知','刷新通知','**/api/notifications?*','.notice-item'],
 ]){
  await page.goto(`/#/${route}`);await expect(page.getByRole('heading',{name:heading,exact:true})).toBeVisible();await expect(page.locator(record).first()).toBeVisible();
  await page.route(endpoint,r=>r.fulfill({status:503,contentType:'application/json',body:JSON.stringify({code:'DEPENDENCY_UNAVAILABLE',message:'验收读取失败',data:null})}));
  await page.getByRole('button',{name:refresh,exact:true}).click();await expect(page.getByRole('alert')).toContainText('已保留');await expect(page.locator(record).first()).toBeVisible();
  await page.unroute(endpoint);await page.getByRole('button',{name:refresh,exact:true}).click();await expect(page.getByRole('alert')).toHaveCount(0);
 }
});
