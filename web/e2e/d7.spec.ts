import {test,expect} from '@playwright/test';
import type {Page} from '@playwright/test';
import {appendFileSync} from 'node:fs';
import {randomUUID} from 'node:crypto';
import {execFileSync} from 'node:child_process';
import {resolve} from 'node:path';
test.skip(process.env.CAMPUSGO_D7_BROWSER!=='1','run make verify-d7');
test.setTimeout(120_000);
const compose=['compose','--env-file','.env','-f','deploy/local/compose.yaml'];
function sql(q:string){return execFileSync('docker',[...compose,'exec','-T','mysql','sh','-c','MYSQL_PWD="$MYSQL_PASSWORD" exec mysql --default-character-set=utf8mb4 -N -B -ucampusgo campusgo_verification'],{cwd:resolve('..'),input:q,encoding:'utf8'}).trim();}
const tokens:string[]=[];
async function token(page:Page,userId:string){const r=await page.request.post('/api/auth/login',{data:{userId}});expect(r.status()).toBe(200);const t=(await r.json()).data.token as string;tokens.push(t);return t;}
test.afterEach(async({page})=>{for(const token of tokens.splice(0))await page.request.post('/api/auth/logout',{headers:{Authorization:`Bearer ${token}`}});});
async function post(page:Page,path:string,body:unknown,token:string,key?:string){
 const send=()=>page.request.post(path,{headers:{Authorization:`Bearer ${token}`,...(key?{'Idempotency-Key':key}:{})},data:body});
 let r=await send();const deadline=Date.now()+45_000;
 while(key&&r.status()===503&&Date.now()<deadline){await page.waitForTimeout(500);r=await send();} // Preserve original request across producer startup/unknown outcomes.
 expect(r.status(),await r.text()).toBe(200);return(await r.json()).data;
}
async function task(page:Page,name:string,status='PUBLISHED'){
 const owner=await token(page,'1001'),runner=await token(page,'2001'),key=randomUUID();appendFileSync(resolve('../work/d5-browser-keys.jsonl'),JSON.stringify({userId:'1001',key})+'\n');
 const r=await post(page,'/api/errands',{campusId:'1',type:'DELIVERY',title:`D5页面验证:${name}`,rewardCents:'1001',slotTotal:1},owner,key),id=r.errandId as string;
 if(status!=='PUBLISHED'){await post(page,`/api/errands/${id}/grab`,{expectedRound:0},runner,randomUUID());await post(page,`/api/errands/${id}/confirm`,{expectedRound:0},runner);}
 if(['PICKED_UP','DELIVERED'].includes(status))await post(page,`/api/errands/${id}/actions`,{action:'PICK_UP',expectedRound:0},runner);
 if(status==='DELIVERED')await post(page,`/api/errands/${id}/actions`,{action:'DELIVER',expectedRound:0},runner);
 return id;
}
const names:Record<string,RegExp>={'1001':/发单同学/,'2001':/跑腿同学 A/,'2002':/跑腿同学 B/,'9001':/平台仲裁员/};
async function login(page:Page,user='1001'){await page.goto('/');await expect(page.getByRole('button',{name:'退出登录'}).or(page.getByRole('button',{name:/发单同学/}))).toBeVisible();if(await page.getByRole('button',{name:'退出登录'}).isVisible())await page.getByRole('button',{name:'退出登录'}).click();await page.getByRole('button',{name:names[user]}).click();await expect(page.getByRole('button',{name:'退出登录'})).toBeVisible();}
async function open(page:Page,id:string){await page.goto(`/#/detail/${id}`);await expect(page.getByRole('heading',{name:/D5页面验证:/})).toBeVisible();}
async function action(page:Page,label:string){const panel=page.getByRole('region',{name:'履约与资金操作'});await panel.getByRole('button',{name:label,exact:true}).click();await panel.getByRole('button',{name:'确认操作',exact:true}).click();}
async function status(page:Page,s:string){await expect(page.locator(`.status-${s}`)).toBeVisible();}


test('real gateway and independent MQ consumers deliver to two API nodes, persist and mark read',async({page})=>{
 const id=await task(page,'D7双节点结算','DELIVERED');await login(page,'2001');await page.goto('/#/notifications');
 await expect(page.getByRole('link',{name:/通知，/})).toHaveAttribute('title','实时已连接');
 await page.evaluate(async()=>{
  const w=window as unknown as {d7ws:WebSocket;d7events:string[]};w.d7events=[];
  w.d7ws=new WebSocket('ws://127.0.0.1:18086/api/ws');
  await new Promise<void>((resolve,reject)=>{w.d7ws.onopen=()=>w.d7ws.send(JSON.stringify({token:localStorage.getItem('campusgo_session')||JSON.parse(localStorage.getItem('campusgo_jwt_pair')||'null')?.session.token}));w.d7ws.onerror=()=>reject(Error('node2 unavailable'));w.d7ws.onmessage=e=>{const value=JSON.parse(e.data);w.d7events.push(value.type);if(value.type==='ready')resolve();};});
 });
 const owner=await token(page,'1001');await post(page,`/api/errands/${id}/actions`,{action:'SETTLE',expectedRound:0},owner,randomUUID());
 await expect.poll(()=>page.evaluate(()=>(window as unknown as {d7events:string[]}).d7events.includes('notification.new')),{timeout:90000}).toBe(true);
 const notice=page.locator('.notice-item').filter({has:page.locator(`a[href="#/detail/${id}"]`)});await expect(notice.getByText('任务已结算，你实收 951 分，平台佣金 50 分',{exact:true})).toBeVisible({timeout:90000});
 expect(sql(`SELECT COUNT(*) FROM notification WHERE errand_id=${id};`)).toBe('2');
 await notice.getByRole('button',{name:'标为已读'}).click();await expect(notice.getByText('已读 · 任务结算',{exact:true})).toBeVisible();
 await expect(page.getByTestId('unread-count')).toHaveText('0');await page.reload();await expect(notice.getByText('已读 · 任务结算',{exact:true})).toBeVisible();
 expect(sql(`SELECT COUNT(*) FROM notification WHERE errand_id=${id} AND user_id=2001 AND read_flag=1;`)).toBe('1');
});

test('offline notification is recovered by polling with failed query retry and retained session',async({page})=>{
 const id=await task(page,'D7离线退款');await page.routeWebSocket('**/api/ws',ws=>ws.close());await login(page);await page.goto('/#/notifications');
 const owner=await token(page,'1001');await post(page,`/api/errands/${id}/actions`,{action:'CANCEL',expectedRound:0},owner,randomUUID());
 await expect(page.locator('.notice-item').filter({has:page.locator(`a[href="#/detail/${id}"]`)}).getByText('任务已退款，1001 分已退回你的账户',{exact:true})).toBeVisible({timeout:90000});
 await page.route('**/api/notifications?*',route=>route.fulfill({status:503,contentType:'application/json',body:JSON.stringify({code:'DEPENDENCY_UNAVAILABLE',message:'通知查询暂不可用',data:null})}));
 await page.getByRole('button',{name:'刷新通知'}).click();await expect(page.getByRole('alert')).toContainText('通知查询暂不可用');expect(await page.evaluate(()=>Boolean(localStorage.getItem('campusgo_session')))).toBe(true);
 await page.unroute('**/api/notifications?*');await page.getByRole('button',{name:'刷新通知'}).click();await expect(page.getByRole('alert')).toHaveCount(0);
});

test('notification pagination, ownership and responsive page work',async({page})=>{
 const id=await task(page,'D7通知分页');
 for(let i=0;i<23;i++)sql(`INSERT INTO notification(msg_key,user_id,errand_id,type,content) VALUES('${randomUUID()}',1001,${id},'CANCEL','D7分页通知 ${i}');`);
 await login(page);await page.goto('/#/notifications');await expect(page.locator('.notice-item')).toHaveCount(20);
 while(await page.getByRole('button',{name:'加载更早通知'}).isVisible()){await page.getByRole('button',{name:'加载更早通知'}).click();await expect(page.getByRole('button',{name:'刷新通知'})).toBeEnabled();}await expect(page.locator('.notice-item')).toHaveCount(Number(sql('SELECT COUNT(*) FROM notification WHERE user_id=1001;')));
 await page.setViewportSize({width:390,height:844});expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
 await login(page,'2002');await page.goto('/#/notifications');await expect(page.getByText(/D7分页通知/)).toHaveCount(0);
});

test('websocket reconnect restores live channel and refreshes facts',async({page})=>{
 let connections=0;let disconnect:()=>Promise<void>=async()=>{};
 await page.routeWebSocket('**/api/ws',ws=>{connections++;const server=ws.connectToServer();disconnect=async()=>{await ws.close({code:1012,reason:'D7 transport interruption'});await server.close();};});
 await login(page);const badge=page.getByRole('link',{name:/通知，/});await expect(badge).toHaveAttribute('title','实时已连接');
 await disconnect();await expect.poll(()=>connections,{timeout:15000}).toBeGreaterThan(1);
 await expect(badge).toHaveAttribute('title','实时已连接',{timeout:15000});
});

test('expired JWT connection refreshes credentials and reconnects without losing identity',async({page})=>{
 test.skip(process.env.CAMPUSGO_D7_MODE!=='jwt','JWT-only lifecycle check');
 let connections=0;page.on('websocket',()=>{connections++;});
 await login(page);const badge=page.getByRole('link',{name:/通知，/});await expect(badge).toHaveAttribute('title','实时已连接');
 const original=await page.evaluate(()=>JSON.parse(localStorage.getItem('campusgo_jwt_pair')!).session.token);
 await expect.poll(async()=>await page.evaluate(old=>JSON.parse(localStorage.getItem('campusgo_jwt_pair')||'null')?.session.token!==old,original),{timeout:45000,intervals:[500]}).toBe(true);
 await expect.poll(()=>connections,{timeout:15000}).toBeGreaterThan(1);await expect(badge).toHaveAttribute('title','实时已连接',{timeout:15000});await expect(page.getByRole('button',{name:'退出登录'})).toBeVisible();
 await page.setViewportSize({width:390,height:844});expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
});
