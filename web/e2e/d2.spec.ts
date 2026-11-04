import {test,expect} from '@playwright/test';
import type {Page} from '@playwright/test';
import {appendFileSync,mkdirSync} from 'node:fs';
import {resolve} from 'node:path';

test.skip(process.env.CAMPUSGO_D2_BROWSER !== '1','run make verify-d2 against the isolated verification database');

async function demo(page:Page,userId='1001'){
 mkdirSync(resolve('../work'),{recursive:true});
 page.on('request',request=>{if(request.method()==='POST'&&new URL(request.url()).pathname==='/api/errands'){
  const key=request.headers()['idempotency-key'];if(key)appendFileSync(resolve('../work/d2-publish-keys.jsonl'),JSON.stringify({userId,key})+'\n');
 }});
 await page.goto('/');await page.getByRole('button',{name:userId==='1001'?/发单同学/:/跑腿同学 A/}).click();
 await expect(page.getByRole('button',{name:'退出登录'})).toBeVisible();await page.goto('/#/publish');
 await expect(page.getByRole('heading',{name:'发布委托.'})).toBeVisible();
}
async function fill(page:Page,title:string,yuan='12.34'){
 await page.getByLabel('任务标题').fill(title);await page.getByLabel('悬赏金额（元）').fill(yuan);
}
async function operation(page:Page,userId='1001'){return page.evaluate(id=>JSON.parse(localStorage.getItem(`campusgo_publish:${id}`)!),userId);}
async function walletBalance(page:Page){return page.evaluate(async()=>{
 const response=await fetch('/api/wallet',{headers:{Authorization:`Bearer ${localStorage.getItem('campusgo_session')}`}});return(await response.json()).data.availableCents as string;
});}

test('publish, escrow receipt, timeline and wallet agree',async({page})=>{
 await demo(page);const before=await walletBalance(page);await fill(page,'D2页面验证:发布与钱包');
 if(process.env.CAMPUSGO_VISUAL==='1')await page.screenshot({path:resolve('../work/d2-publish-desktop.png'),fullPage:true});
 await page.getByRole('button',{name:'托管赏金并发布'}).click();await expect(page.getByRole('heading',{name:'发布成功，赏金已托管'})).toBeVisible();
 const op=await operation(page);expect(op.command.rewardCents).toBe('1234');expect(await walletBalance(page)).toBe((BigInt(before)-1234n).toString());
 await page.getByRole('link',{name:'查看这份委托'}).click();await expect(page.getByRole('heading',{name:'D2页面验证:发布与钱包'})).toBeVisible();
 await expect(page.getByText('¥12.34 · 托管中')).toBeVisible();await expect(page.getByText('草稿 → 待抢单')).toBeVisible();
 await page.goto('/#/wallet');await expect(page.getByTestId('wallet-available')).toHaveText(`¥${((BigInt(before)-1234n)/100n).toLocaleString('zh-CN')}.${((BigInt(before)-1234n)%100n).toString().padStart(2,'0')}`);
 await expect(page.getByText('赏金转入托管',{exact:true})).toBeVisible();await expect(page.getByText('演示期初资金',{exact:true})).toBeVisible();
});

test('server commits but browser loses response; reload resolves the same operation',async({page})=>{
 await demo(page);const before=await walletBalance(page);await fill(page,'D2页面验证:发布回执丢失','1.01');let writes=0;let original='';
 await page.route('**/api/errands',async route=>{if(route.request().method()!=='POST'){await route.continue();return;}
  writes++;original=route.request().headers()['idempotency-key'];const committed=await route.fetch();expect(committed.status()).toBe(200);await route.abort('connectionreset');
 });
 await page.getByRole('button',{name:'托管赏金并发布'}).click();await expect(page.getByRole('heading',{name:'发布结果待确认'})).toBeVisible();await expect(page.getByRole('alert')).toContainText('网络连接失败');
 expect((await operation(page)).key).toBe(original);await page.unroute('**/api/errands');await page.reload();
 await expect(page.getByRole('heading',{name:'发布成功，赏金已托管'})).toBeVisible();expect((await operation(page)).key).toBe(original);expect(writes).toBe(1);expect(await walletBalance(page)).toBe((BigInt(before)-101n).toString());
});

test('pre-write 503 and NOT_OBSERVED retain original parameters and key on retry',async({page})=>{
 await demo(page);await fill(page,'D2页面验证:原请求重试','2.00');let original='';
 await page.route('**/api/errands',async route=>{if(route.request().method()!=='POST'){await route.continue();return;}original=route.request().headers()['idempotency-key'];await route.fulfill({status:503,contentType:'application/json',headers:{'Retry-After':'1'},body:JSON.stringify({code:'DEPENDENCY_UNAVAILABLE',message:'依赖暂不可用',data:null})});});
 await page.getByRole('button',{name:'托管赏金并发布'}).click();await expect(page.getByRole('alert')).toContainText('依赖暂不可用');
 await page.getByRole('button',{name:'查询发布结果'}).click();await expect(page.getByText(/暂未查到已提交结果/)).toBeVisible();await expect(page.getByLabel('任务标题')).toBeDisabled();
 await page.unroute('**/api/errands');const next=page.waitForRequest(r=>r.method()==='POST'&&new URL(r.url()).pathname==='/api/errands');await page.getByRole('button',{name:'重试原请求'}).click();expect((await next).headers()['idempotency-key']).toBe(original);
 await expect(page.getByRole('heading',{name:'发布成功，赏金已托管'})).toBeVisible();
});

test('insufficient balance is final and no task or debit is created',async({page})=>{
 await demo(page,'2001');await fill(page,'D2页面验证:余额不足','0.01');await page.getByRole('button',{name:'托管赏金并发布'}).click();
 await expect(page.getByRole('heading',{name:'发布未成功'})).toBeVisible();await expect(page.getByText('本次没有创建任务，也没有扣款。可用余额不足。')).toBeVisible();expect(await walletBalance(page)).toBe('0');
 await page.reload();await expect(page.getByRole('heading',{name:'发布未成功'})).toBeVisible();await page.getByRole('button',{name:'修改后重新发布'}).click();expect(await operation(page,'2001')).toBeNull();await expect(page.getByLabel('任务标题')).toBeEnabled();
});

test('a pending publication stays with its original identity',async({page})=>{
 await demo(page);await fill(page,'D2页面验证:身份隔离','3.00');
 await page.route('**/api/errands',route=>route.request().method()==='POST'?route.abort('connectionreset'):route.continue());
 await page.getByRole('button',{name:'托管赏金并发布'}).click();await expect(page.getByRole('alert')).toBeVisible();const original=await operation(page);
 await page.getByRole('button',{name:'退出登录'}).click();await page.getByRole('button',{name:/跑腿同学 A/}).click();await expect(page.getByRole('button',{name:'退出登录'})).toBeVisible();
 await expect(page.getByLabel('任务标题')).toHaveValue('');expect(await operation(page,'2001')).toBeNull();expect((await operation(page)).key).toBe(original.key);
 await page.getByRole('button',{name:'退出登录'}).click();await page.getByRole('button',{name:/发单同学/}).click();await expect(page.getByLabel('任务标题')).toHaveValue(original.command.title);await expect(page.getByRole('heading',{name:'发布结果待确认'})).toBeVisible();
});

test('strict money input and mobile layout',async({page})=>{
 await page.setViewportSize({width:390,height:844});await demo(page);let writes=0;page.on('request',r=>{if(r.method()==='POST'&&new URL(r.url()).pathname==='/api/errands')writes++;});
 await fill(page,'D2页面验证:金额校验','1.001');await page.getByRole('button',{name:'托管赏金并发布'}).click();await expect(page.getByRole('alert')).toContainText('最多两位小数');expect(writes).toBe(0);
 expect(await page.evaluate(()=>document.documentElement.scrollWidth)).toBeLessThanOrEqual(390);await page.goto('/#/wallet');await expect(page.getByRole('heading',{name:'我的钱包.'})).toBeVisible();expect(await page.evaluate(()=>document.documentElement.scrollWidth)).toBeLessThanOrEqual(390);
 if(process.env.CAMPUSGO_VISUAL==='1'){await expect(page.getByTestId('wallet-available')).not.toHaveText('—');await page.screenshot({path:resolve('../work/d2-wallet-mobile.png'),fullPage:true});}
});

test('late initial result query cannot overwrite a successful retry',async({page})=>{
 const key=crypto.randomUUID();const op={key,command:{campusId:'1',type:'DELIVERY',title:'D2页面验证:迟到查询',rewardCents:'100',slotTotal:1}};
 await page.addInitScript(value=>localStorage.setItem('campusgo_publish:1001',JSON.stringify(value)),op);
 let release!:()=>void;const pending=new Promise<void>(resolve=>{release=resolve;});let intercepted=0;
 await page.route(`**/api/publish-requests/${key}`,async route=>{intercepted++;await pending;await route.fulfill({contentType:'application/json',body:JSON.stringify({code:'OK',data:{requestKey:key,state:'NOT_OBSERVED',errandId:null,status:'',frozenCents:'0',errorCode:''}})});});
 await demo(page);await expect.poll(()=>intercepted).toBeGreaterThan(0);await page.getByRole('button',{name:'重试原请求'}).click();await expect(page.getByRole('heading',{name:'发布成功，赏金已托管'})).toBeVisible();
 const response=page.waitForResponse(r=>r.url().includes(`/api/publish-requests/${key}`));release();await response;
 await expect(page.getByRole('heading',{name:'发布成功，赏金已托管'})).toBeVisible();
});
