import {test,expect} from '@playwright/test';
import type {Page} from '@playwright/test';
import {appendFileSync} from 'node:fs';
import {resolve} from 'node:path';

test.skip(process.env.CAMPUSGO_D8_BROWSER!=='1','run make verify-d8-business');
test.setTimeout(90_000);
const names:Record<string,RegExp>={'1001':/发单同学/,'2001':/跑腿同学 A/,'2002':/跑腿同学 B/};
async function login(page:Page,id:string){
 await page.goto('/');
 await expect(page.getByRole('button',{name:'退出登录'}).or(page.getByRole('button',{name:/发单同学/}))).toBeVisible();
 if(await page.getByRole('button',{name:'退出登录'}).isVisible())await page.getByRole('button',{name:'退出登录'}).click();
 await page.getByRole('button',{name:names[id]}).click();
 await expect(page.getByRole('button',{name:'退出登录'})).toBeVisible();
}
async function action(page:Page,label:string){
 const panel=page.getByRole('region',{name:'履约与资金操作'});
 await panel.getByRole('button',{name:label,exact:true}).click();
 await panel.getByRole('button',{name:'确认操作',exact:true}).click();
}

test('D8 business all-UI publish, grab, confirm, deliver, settle, credit and durable notice',async({page})=>{
 page.on('request',r=>{
  if(r.method()==='POST'&&new URL(r.url()).pathname==='/api/errands'){
   const key=r.headers()['idempotency-key'];if(key)appendFileSync(resolve('../work/d5-browser-keys.jsonl'),JSON.stringify({userId:'1001',key})+'\n');
  }
 });
 await login(page,'1001');await page.goto('/#/publish');
 await page.getByLabel('任务标题').fill('D5页面验证:D8全页面业务闭环');await page.getByLabel('悬赏金额（元）').fill('10.01');
 await page.getByRole('button',{name:'托管赏金并发布'}).click();
 await expect(page.getByRole('heading',{name:'发布成功，赏金已托管'})).toBeVisible();
 await page.getByRole('link',{name:'查看这份委托'}).click();const detail=page.url();const id=detail.split('/detail/')[1];
 await expect(page.getByText('¥10.01 · 托管中')).toBeVisible();await expect(page.getByRole('button',{name:'立即抢单'})).toHaveCount(0);
 await login(page,'2001');await page.goto(detail);await page.getByRole('button',{name:'立即抢单'}).click();
 await expect(page.getByRole('heading',{name:'已抢到，等待确认'})).toBeVisible();await page.getByRole('button',{name:'确认接单',exact:true}).click();
 await expect(page.locator('.status-ACCEPTED')).toBeVisible();await action(page,'确认取货');await expect(page.locator('.status-PICKED_UP')).toBeVisible();
 await action(page,'确认送达');await expect(page.locator('.status-DELIVERED')).toBeVisible();
 await login(page,'1001');await page.goto(detail);await action(page,'确认完成并结算');await expect(page.locator('.status-SETTLED')).toBeVisible();
 await login(page,'2001');await page.goto('/#/credit');await expect(page.getByRole('heading',{name:'靠谱，值得被看见'})).toBeVisible();
 const event=page.getByRole('region',{name:'信用足迹'}).locator('.account-credit-event').filter({has:page.locator(`a[href="#/detail/${id}"]`)});
 await expect(event.getByText('完成结算',{exact:true})).toBeVisible();await expect(event.getByText('+2',{exact:true})).toBeVisible();
 await page.getByRole('button',{name:'扣分记录',exact:true}).click();await expect(event).toHaveCount(0);
 await page.getByRole('button',{name:'加分记录',exact:true}).click();await expect(event).toBeVisible();
 await expect(page.getByRole('region',{name:'校园信用榜'})).toBeVisible();
 await page.goto('/#/notifications');const notice=page.locator('.notice-item').filter({has:page.locator(`a[href="#/detail/${id}"]`)});
 await expect(notice.getByText('任务已结算，你实收 951 分，平台佣金 50 分',{exact:true})).toBeVisible({timeout:60000});
 await notice.getByRole('button',{name:'标为已读'}).click();await expect(notice.getByText('已读 · 任务结算',{exact:true})).toBeVisible();
 await page.goto('/#/wallet');const income=page.locator('.wallet-ledger li').filter({has:page.locator(`a[href="#/detail/${id}"]`)});await expect(income.getByText('任务结算收入',{exact:true})).toBeVisible();await expect(income.getByText('+¥9.51',{exact:true})).toBeVisible();
 await login(page,'2002');await page.goto('/#/credit');await expect(page.locator(`.account-credit-event a[href="#/detail/${id}"]`)).toHaveCount(0);
});

test('D8 business credit navigation, filters and mobile layout',async({page})=>{
 await page.setViewportSize({width:390,height:844});await login(page,'2001');
 await page.getByRole('navigation',{name:'底部导航'}).getByRole('link',{name:'信用',exact:true}).click();
 await expect(page.getByRole('heading',{name:'靠谱，值得被看见'})).toBeVisible();
 await expect(page.getByTestId('credit-score')).not.toContainText('—');
 await page.getByRole('button',{name:'刷新信用',exact:true}).click();await expect(page.getByRole('button',{name:'刷新信用',exact:true})).toBeEnabled();
 await expect(page.getByRole('region',{name:'校园信用榜'})).toBeVisible();
 expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
});
