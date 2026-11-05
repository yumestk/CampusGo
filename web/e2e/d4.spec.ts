import {test,expect} from '@playwright/test';
import type {Page} from '@playwright/test';
import {execFileSync} from 'node:child_process';
import {resolve} from 'node:path';
test.skip(process.env.CAMPUSGO_D4_BROWSER!=='1','run make verify-d4');
const id=(n:number)=>String(9007199254741400n+BigInt(n));
const compose=['compose','--env-file','.env','-f','deploy/local/compose.yaml'];
function sql(query:string){return execFileSync('docker',[...compose,'exec','-T','mysql','sh','-c','MYSQL_PWD="$MYSQL_PASSWORD" exec mysql --default-character-set=utf8mb4 -N -B -ucampusgo campusgo_verification'],{cwd:resolve('..'),input:query,encoding:'utf8'}).trim();}
async function login(page:Page,user:'A'|'B'='A'){
 await page.goto('/');await page.getByRole('button',{name:new RegExp(`跑腿同学 ${user}`)}).click();await expect(page.getByRole('button',{name:'退出登录'})).toBeVisible();
}
async function open(page:Page,n:number){await page.goto(`/#/detail/${id(n)}`);await expect(page.getByRole('heading',{name:/D4页面验证:/})).toBeVisible();}
async function switchTo(page:Page,user:'A'|'B'){await page.getByRole('button',{name:'退出登录'}).click();await page.getByRole('button',{name:new RegExp(`跑腿同学 ${user}`)}).click();await expect(page.getByRole('button',{name:'退出登录'})).toBeVisible();}
async function grab(page:Page,n:number){await login(page);await open(page,n);await page.getByRole('button',{name:'立即抢单'}).click();await expect(page.getByRole('button',{name:'确认接单',exact:true})).toBeVisible();}
async function waitStatus(n:number,status:string,round:number){await expect.poll(()=>sql(`SELECT CONCAT(status,':',round) FROM errand WHERE id=${id(n)};`),{timeout:15000}).toBe(`${status}:${round}`);}

test('grab, confirm, reload and authoritative history',async({page})=>{
 await grab(page,1);await expect(page.getByText('前确认，超时将自动递补或重新开放。',{exact:false})).toBeVisible();await page.getByRole('button',{name:'确认接单',exact:true}).click();await expect(page.locator('.status-ACCEPTED')).toBeVisible();await expect(page.getByRole('heading',{name:'本次抢单曾成功'})).toBeVisible();await page.reload();await expect(page.locator('.status-ACCEPTED')).toBeVisible();await expect(page.getByRole('button',{name:'确认接单',exact:true})).toHaveCount(0);
});
test('lost confirmation response recovers from SQL after reload',async({page})=>{
 await grab(page,2);await page.route(`**/api/errands/${id(2)}/confirm`,async route=>{const response=await route.fetch();expect(response.status()).toBe(200);await route.abort('connectionreset');});await page.getByRole('button',{name:'确认接单',exact:true}).click();await page.reload();await expect(page.locator('.status-ACCEPTED')).toBeVisible();expect(sql(`SELECT COUNT(*) FROM errand_status_log WHERE errand_id=${id(2)} AND to_status='ACCEPTED';`)).toBe('1');
});
test('candidate registration, worker promotion and new owner confirmation',async({page})=>{
 await grab(page,3);await switchTo(page,'B');await page.getByRole('button',{name:'尝试候选登记'}).click();await expect(page.getByRole('heading',{name:'本次未抢到'})).toBeVisible();
 sql(`UPDATE errand SET confirm_deadline=TIMESTAMPADD(SECOND,-1,CURRENT_TIMESTAMP(3)) WHERE id=${id(3)};`);await waitStatus(3,'LOCKED',1);await page.getByRole('button',{name:'刷新进度'}).click();await expect(page.getByRole('button',{name:'确认接单',exact:true})).toBeVisible();
 await switchTo(page,'A');await expect(page.getByRole('button',{name:'确认接单',exact:true})).toHaveCount(0);await expect(page.getByRole('heading',{name:'本次抢单曾成功'})).toBeVisible();await switchTo(page,'B');await page.getByRole('button',{name:'确认接单',exact:true}).click();await expect(page.locator('.status-ACCEPTED')).toBeVisible();
});
test('empty queue repools and historical success can explicitly start a new round',async({page})=>{
 await grab(page,4);const original=await page.evaluate(task=>JSON.parse(localStorage.getItem(`campusgo_grab:2001:${task}`)!),id(4));sql(`UPDATE errand SET confirm_deadline=TIMESTAMPADD(SECOND,-1,CURRENT_TIMESTAMP(3)) WHERE id=${id(4)};`);await waitStatus(4,'PUBLISHED',1);await page.getByRole('button',{name:'刷新进度'}).click();await expect(page.getByRole('heading',{name:'本次抢单曾成功'})).toBeVisible();await page.getByRole('button',{name:'准备重新抢单'}).click();await page.getByRole('button',{name:'立即抢单'}).click();await expect(page.getByRole('button',{name:'确认接单',exact:true})).toBeVisible();const current=await page.evaluate(task=>JSON.parse(localStorage.getItem(`campusgo_grab:2001:${task}`)!),id(4));expect(current.expectedRound).toBe(1);expect(current.key).not.toBe(original.key);
});
test('late old-round confirmation cannot confirm a new reservation',async({page})=>{
 await grab(page,5);sql(`UPDATE errand SET confirm_deadline=TIMESTAMPADD(SECOND,-1,CURRENT_TIMESTAMP(3)) WHERE id=${id(5)};`);await waitStatus(5,'PUBLISHED',1);
 const response=await page.evaluate(async task=>{const token=localStorage.getItem('campusgo_session');const r=await fetch(`/api/errands/${task}/confirm`,{method:'POST',headers:{Authorization:`Bearer ${token}`,'Content-Type':'application/json'},body:JSON.stringify({expectedRound:0})});return {status:r.status,body:await r.json()};},id(5));expect(response.status).toBe(409);expect(response.body.code).toBe('STALE_VERSION');await page.getByRole('button',{name:'刷新进度'}).click();await expect(page.getByRole('button',{name:'确认接单',exact:true})).toHaveCount(0);
});
test('mobile confirmation layout',async({page})=>{
 await page.setViewportSize({width:390,height:844});await grab(page,6);expect(await page.evaluate(()=>document.documentElement.scrollWidth)).toBeLessThanOrEqual(390);if(process.env.CAMPUSGO_VISUAL==='1')await page.screenshot({path:resolve('../work/d4-confirm-mobile.png'),fullPage:true});await page.getByRole('button',{name:'确认接单',exact:true}).click();await expect(page.locator('.status-ACCEPTED')).toBeVisible();
});
test('MQ outage leaves SQL due scan available and worker alive',async({page})=>{
 test.setTimeout(90000);await grab(page,7);
 try{execFileSync('docker',[...compose,'stop','rmqbroker'],{cwd:resolve('..'),stdio:'pipe'});sql(`UPDATE errand SET confirm_deadline=TIMESTAMPADD(SECOND,-1,CURRENT_TIMESTAMP(3)) WHERE id=${id(7)};`);await waitStatus(7,'PUBLISHED',1);await page.getByRole('button',{name:'刷新进度'}).click();await expect(page.locator('.status-PUBLISHED')).toBeVisible();}
 finally{execFileSync('docker',[...compose,'up','-d','--wait','rmqbroker'],{cwd:resolve('..'),stdio:'pipe'});}
});
test('confirmation quota rejected with visible explanation',async({page})=>{
 await login(page,'B');await open(page,8);await page.getByRole('button',{name:'立即抢单'}).click();await expect(page.getByRole('button',{name:'确认接单',exact:true})).toBeVisible();
 // Fill the remaining quota after reservation; this scenario also runs alone.
 const ongoing=Number(sql("SELECT COUNT(*) FROM errand WHERE grabber_id=2002 AND status IN ('ACCEPTED','PICKED_UP');"));
 sql(`INSERT INTO errand(id,campus_id,publisher_id,grabber_id,type,title,reward_amount,status,slot_taken,version) VALUES ${[10,11,12,13,14].slice(0,5-ongoing).map(n=>`(${id(n)},1,1001,2002,'DELIVERY','D4页面验证:额度',500,'ACCEPTED',1,1)`).join(',')};`);
 await page.getByRole('button',{name:'确认接单',exact:true}).click();await expect(page.getByRole('alert')).toContainText('在途任务已达 5 单上限');expect(sql(`SELECT COUNT(*) FROM errand WHERE grabber_id=2002 AND status IN ('ACCEPTED','PICKED_UP');`)).toBe('5');await expect(page.locator('.status-LOCKED')).toBeVisible();
});
