import {useRef,useState} from 'react';
import {api,ApiError,money} from '../api';
import type {Card,User} from '../api';
const labels:Record<string,string>={PICK_UP:'确认取货',DELIVER:'确认送达',DISPUTE:'发起争议',CANCEL_DRAFT:'取消草稿',CANCEL:'取消并退款',SETTLE:'确认完成并结算',ARBITRATE_RUNNER:'裁决支持跑腿',ARBITRATE_PUBLISHER:'裁决支持发单人'};
const funds=new Set(['CANCEL','SETTLE','ARBITRATE_RUNNER','ARBITRATE_PUBLISHER']);
type Pending={key:string;action:string;round:number};
export default function FulfillmentPanel({card,user,onResolved}:{card:Card;user:User;onResolved:()=>void}){
 const storageKey=`campusgo:fund:${user.id}:${card.id}`;
 const [pending,setPending]=useState<Pending|null>(()=>{try{const p=JSON.parse(sessionStorage.getItem(storageKey)||'null') as Pending|null;return p&&/^[0-9a-f-]{36}$/.test(p.key)&&funds.has(p.action)&&Number.isInteger(p.round)&&p.round>=0?p:null;}catch{return null;}});
 const [busy,setBusy]=useState(false),[message,setMessage]=useState(''),[selected,setSelected]=useState('');const lock=useRef(false);
 const priority=(action:string)=>['PICK_UP','DELIVER','SETTLE'].includes(action)?0:['DISPUTE','CANCEL','CANCEL_DRAFT'].includes(action)?2:1;
 const actions=card.availableActions.filter(a=>labels[a]).sort((a,b)=>priority(a)-priority(b));
 const save=(p:Pending|null)=>{if(p)sessionStorage.setItem(storageKey,JSON.stringify(p));else sessionStorage.removeItem(storageKey);setPending(p);};
 const observe=async(p:Pending,rejected?:ApiError)=>{const r=await api.fundResult(p.key);if(r.state==='COMMITTED'){save(null);setSelected('');setMessage('资金操作已完成，可以查看钱包和任务进展。');onResolved();}else if(r.state==='ABORTED'){save(null);setSelected('');setMessage(({FUND_EXPIRED:'请求已过期，资金未执行。',BALANCE_LIMIT:'账户金额达到上限，本次操作未执行。',INVALID_STATUS:'任务状态已变化，本次操作未执行。',STALE_VERSION:'任务轮次已变化，本次操作未执行。',FORBIDDEN:'没有执行权限，本次操作未执行。'} as Record<string,string>)[r.errorCode]||'本次资金操作未执行，请刷新任务后再操作。');onResolved();}else if(r.state==='NOT_OBSERVED'&&rejected&&[400,403,404,409].includes(rejected.status)){save(null);setSelected('');setMessage(rejected.message);onResolved();}else setMessage('结果尚未确认，请查询进度或使用原请求重试。');};
 const run=async(action:string,retry?:Pending)=>{if(lock.current)return;lock.current=true;setBusy(true);setMessage('');let p=retry;
  try{if(funds.has(action)){p=p||{key:crypto.randomUUID(),action,round:card.round};save(p);}await api.action(card.id,p?.round??card.round,action,p?.key);if(p)await observe(p);else{setSelected('');setMessage('操作已完成。');onResolved();}}
  catch(e){setMessage(e instanceof Error?e.message:'操作结果待确认，请刷新进度。');if(p){try{await observe(p,e instanceof ApiError?e:undefined);}catch{/* Keep the original request until its durable result can be queried. */}}else if(e instanceof ApiError&&e.code==='ACTION_RESULT_UNKNOWN')onResolved();}
  finally{lock.current=false;setBusy(false);}
 };
 const query=async()=>{if(!pending||lock.current)return;lock.current=true;setBusy(true);try{await observe(pending);}catch(e){setMessage(e instanceof Error?e.message:'查询失败，请稍后重试');}finally{lock.current=false;setBusy(false);}};
 if(!actions.length&&!pending&&!message)return null;
 const commission=BigInt(card.rewardCents)/20n,runner=BigInt(card.rewardCents)-commission;
 return <section className="panel publish-result action-panel" aria-label="履约与资金操作"><span className="action-kicker">{pending?"原操作待确认":"按当前权限操作"}</span><h2>履约与资金</h2>{card.deliveredAt&&<p>送达时间：{new Date(card.deliveredAt).toLocaleString('zh-CN')}。送达满 24 小时后，未进入争议的任务将自动结算。</p>}{message&&<p role="status">{message}</p>}
 {pending?<><p>正在确认“{labels[pending.action]}”的处理结果。</p><button className="btn btn-ghost" disabled={busy} onClick={()=>void query()}>查询资金结果</button><button className="btn btn-primary" disabled={busy} onClick={()=>void run(pending.action,pending)}>使用原请求重试</button></>:<>
 <div className="fulfillment-actions">{actions.map(a=><button key={a} className={`btn ${!selected&&["PICK_UP","DELIVER","SETTLE"].includes(a)?"btn-primary":["CANCEL","CANCEL_DRAFT","DISPUTE"].includes(a)?"btn-danger":"btn-ghost"}`} aria-pressed={selected===a} disabled={busy} onClick={()=>setSelected(a)}>{labels[a]}</button>)}</div>
 {selected&&actions.includes(selected)&&<div className="action-confirmation"><h3>操作核对 · {labels[selected]}</h3><p>{selected==='SETTLE'||selected==='ARBITRATE_RUNNER'?`托管赏金 ${money(card.rewardCents)}，跑腿获得 ${money(runner.toString())}，平台佣金 ${money(commission.toString())}。结算信用 +2。`:selected==='CANCEL'||selected==='ARBITRATE_PUBLISHER'?`托管赏金 ${money(card.rewardCents)} 全额退还发单人，不收佣金。${selected==='ARBITRATE_PUBLISHER'?'跑腿争议败诉，信用 -8。':''}`:selected==='DISPUTE'?'发起争议后，资金继续托管，等待平台仲裁。':`确认执行“${labels[selected]}”？`}</p><button className="btn btn-primary" disabled={busy} onClick={()=>void run(selected)}>{busy?'正在处理…':'确认操作'}</button><button className="btn btn-ghost" disabled={busy} onClick={()=>setSelected('')}>暂不操作</button></div>}
 </>}
 </section>;
}
