import { useEffect,useRef,useState } from 'react';
import { api,money } from '../api';
import type { Balance,LedgerEntry } from '../api';
import Icon from '../components/Icon';

export default function Wallet(){
 const [balance,setBalance]=useState<Balance|null>(null),[entries,setEntries]=useState<LedgerEntry[]>([]),[cursor,setCursor]=useState(''),[error,setError]=useState(''),[busy,setBusy]=useState(false);
 const [credit,setCredit]=useState<number|null>(null);
 const generation=useRef(0),lock=useRef(false);
 const load=async(more=false)=>{
  if(lock.current)return;lock.current=true;setBusy(true);setError('');const current=++generation.current;
  try{
   const [b,p]=await Promise.all([api.balance(),api.ledger(more?cursor:'')]);
   void api.credit().then(c=>{if(current===generation.current)setCredit(c.score);}).catch(()=>{if(current===generation.current)setCredit(null);});
   if(current!==generation.current)return;setBalance(b);setEntries(old=>more?[...old,...p.items.filter(e=>!old.some(o=>o.id===e.id))]:p.items);setCursor(p.nextCursor);
  }catch(e){if(current===generation.current)setError(e instanceof Error?e.message:'钱包加载失败');}
  finally{if(current===generation.current){lock.current=false;setBusy(false);}}
 };
 useEffect(()=>{lock.current=false;void load();return()=>{++generation.current;};},[]);
 const latestLoad=useRef(load);latestLoad.current=load;
 useEffect(()=>{const refresh=()=>void latestLoad.current();window.addEventListener('campusgo:changed',refresh);return()=>window.removeEventListener('campusgo:changed',refresh);},[]);
 return <div className="funds-page wallet-page account-record-page"><div className="page-head"><div><span className="eyebrow">EVERY CENT COUNTS</span><h1>我的钱包</h1><p>查看可用余额与每一笔资金记录。</p></div><button className="btn btn-ghost" onClick={()=>void load()} disabled={busy}><Icon name="refresh"/>{busy?'正在刷新…':'刷新钱包'}</button></div>
 {error&&<div className="banner warn" role="alert">{error}{balance&&'。已保留上次查询结果，余额可能滞后。'}<button className="text-button" disabled={busy} onClick={()=>void load()}>重试</button></div>}
 <section className="wallet-overview" aria-label="钱包余额与托管说明" aria-busy={busy}><div className="wallet-available"><Icon name="wallet" size={28}/><span>可用余额 · 元</span><strong data-testid="wallet-available">{balance?money(balance.availableCents):'—'}</strong><p className="funds-muted">可用于发布委托的余额</p><a className="btn" href="#/publish">发布一份委托<Icon name="arrow-right"/></a></div><div className="panel wallet-explain"><span className="action-kicker">资金去向</span><h2>钱包余额与任务托管分开记</h2><p>发布后的赏金转入平台托管账户，可在对应任务详情中查看。</p><p>当前信用：{credit===null?'暂不可用':`${credit} 分`}。基准 60 分，按最近 30 天信用事件定期校准。<a href="#/credit">查看信用足迹与榜单</a></p><dl className="wallet-freeze"><div><dt>预授权冻结余额</dt><dd>{balance?money(balance.frozenCents):'—'}</dd></div></dl><p className="funds-muted">预授权冻结不含任务托管。任务托管金额请逐单查看，不能用冻结余额代替。</p></div></section>
 <section className="panel wallet-ledger" aria-label="资金流水" aria-busy={busy}><div className="funds-section-title"><h2>资金流水</h2><span className="funds-muted">最近记录优先 · 金额精确到分</span></div>{entries.length===0?<div className="empty-state" role="status"><p>{busy?'正在加载资金记录…':error?'资金记录暂不可用，请重试。':'还没有资金流水'}</p>{!busy&&!error&&<a className="btn btn-ghost" href="#/mine">查看我的任务</a>}</div>:<ul>{entries.map(e=><li key={e.id}><span aria-hidden="true" className={`ledger-symbol ${e.direction==='CREDIT'?'credit':''}`}><Icon name={e.direction==='CREDIT'?'arrow-down':'arrow-up'}/></span><div className="ledger-copy"><strong>{({OPENING:'演示期初资金',ESCROW:'赏金转入托管',SETTLE:'任务结算收入',REFUND:'托管赏金退款'} as Record<string,string>)[e.refType]||e.refType}</strong><p>{e.refType==='OPENING'?'本地演示账户初始化':<a href={`#/detail/${e.refId}`}>查看对应任务<Icon name="arrow-right" size={12}/></a>}</p>{e.refType!=='OPENING'&&<span className="record-ref">任务 {e.refId}</span>}<time dateTime={e.time}>{new Date(e.time).toLocaleString('zh-CN')}</time></div><div className={`ledger-amount ${e.direction==='CREDIT'?'ledger-income':'ledger-expense'}`}><span className="record-direction">{e.direction==='CREDIT'?'入账':'出账'}</span><strong>{e.direction==='CREDIT'?'+':'−'}{money(e.amountCents)}</strong><small>记账后 {money(e.balanceAfterCents)}</small></div></li>)}</ul>}{cursor&&<button className="btn btn-ghost" disabled={busy} onClick={()=>void load(true)}>{busy?'正在加载…':'加载更早流水'}</button>}</section>
 </div>;
}
