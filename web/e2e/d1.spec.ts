import { test, expect } from '@playwright/test';

const boundary = 'D1只读验证:精度边界';
async function demo(page: import('@playwright/test').Page) {
  await page.goto('/'); await page.getByRole('button', {name:/发单同学/}).click();
  await expect(page.getByRole('button',{name:'退出登录'})).toBeVisible();
  await expect(page.getByRole('link',{name:boundary,exact:true})).toBeVisible();
}
test('demo login, exact IDs and cents, detail/timeline, mine, restore and logout', async ({page}) => {
  await demo(page);
  await expect(page.getByRole('link',{name:boundary,exact:true})).toBeVisible();
  await expect(page.getByText('¥92,233,720,368,547,758.07',{exact:true})).toBeVisible();
  await page.getByRole('link',{name:boundary,exact:true}).click();
  await expect(page).toHaveURL(/9007199254740993\?from=square$/);
  await expect(page.getByText('任务编号 9007199254740993')).toBeVisible();
  await expect(page.getByText('草稿 → 待抢单')).toBeVisible();
  await page.getByRole('link',{name:'我的任务',exact:true}).first().click();
  await expect(page.getByRole('link',{name:'D1只读验证:私人草稿',exact:true})).toBeVisible();
  await page.reload(); await expect(page.getByRole('heading',{name:'我的任务'})).toBeVisible();
  await page.getByRole('button',{name:'退出登录'}).click();
  await expect(page.getByRole('heading',{name:'欢迎回来'})).toBeVisible();
  expect(await page.evaluate(() => localStorage.getItem('campusgo_session'))).toBeNull();
});
test('503 keeps session and previous results; explicit retry recovers', async ({page}) => {
  await demo(page); const token = await page.evaluate(() => localStorage.getItem('campusgo_session'));
  await page.route('**/api/errands?**', route => route.fulfill({status:503,contentType:'application/json',headers:{'Retry-After':'1'},body:JSON.stringify({code:'DEPENDENCY_UNAVAILABLE',message:'依赖暂不可用，请稍后重试',data:null})}));
  await page.getByRole('button',{name:'刷新任务'}).click();
  await expect(page.getByRole('alert')).toContainText('依赖暂不可用');
  await expect(page.getByRole('link',{name:boundary,exact:true})).toBeVisible();
  expect(await page.evaluate(() => localStorage.getItem('campusgo_session'))).toBe(token);
  await page.unroute('**/api/errands?**'); await page.getByRole('button',{name:'重试',exact:true}).click();
  await expect(page.getByRole('alert')).toHaveCount(0);
});
test('real Dex authorization code + PKCE popup login maps to an internal user', async ({page}) => {
  test.skip(!process.env.CAMPUSGO_LOCAL_IDP_PASSWORD, 'run make verify-d1');
  await page.goto('/');
  const popupPromise = page.waitForEvent('popup');
  await page.getByRole('button',{name:/使用统一身份登录/}).click(); const popup = await popupPromise;
  await popup.locator('input[name="login"]').fill('publisher@campusgo.local');
  await popup.locator('input[name="password"]').fill(process.env.CAMPUSGO_LOCAL_IDP_PASSWORD!);
  await popup.getByRole('button',{name:'Login',exact:true}).click();
  await expect(page.getByRole('heading',{name:'顺路，帮个忙。'})).toBeVisible();
  await expect(page.getByRole('link',{name:boundary,exact:true})).toBeVisible();
  const token = await page.evaluate(() => localStorage.getItem('campusgo_session'));
  expect(token).toMatch(/^[A-Za-z0-9_-]{43}$/);
  await page.getByRole('button',{name:'退出登录'}).click();
});
test('real Dex user without issuer/subject binding is forbidden', async ({page}) => {
  test.skip(!process.env.CAMPUSGO_LOCAL_IDP_PASSWORD, 'run make verify-d1');
  await page.goto('/'); const popupPromise = page.waitForEvent('popup');
  await page.getByRole('button',{name:/使用统一身份登录/}).click(); const popup = await popupPromise;
  await popup.locator('input[name="login"]').fill('unbound@campusgo.local');
  await popup.locator('input[name="password"]').fill(process.env.CAMPUSGO_LOCAL_IDP_PASSWORD!);
  const callback = popup.waitForResponse(r => r.url().includes('/api/auth/oidc/callback'));
  await popup.getByRole('button',{name:'Login',exact:true}).click();
  const result = await callback; expect(result.status()).toBe(403); expect((await result.json()).code).toBe('IDENTITY_NOT_BOUND');
  expect(await page.evaluate(() => localStorage.getItem('campusgo_session'))).toBeNull(); await popup.close();
});
test('mobile pages fit the viewport', async ({page}) => {
  await page.setViewportSize({width:390,height:844}); await demo(page);
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(390);
  await page.getByRole('link',{name:boundary,exact:true}).click();
  await expect(page.getByRole('heading',{name:boundary,exact:true})).toBeVisible();
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(390);
});
