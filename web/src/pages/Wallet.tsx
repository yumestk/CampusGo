import { useEffect,useRef,useState } from 'react';
import { api,money } from '../api';
import type { Balance,LedgerEntry } from '../api';
import Icon from '../components/Icon';
import './funds.css';

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
 return <div className="funds-page"><div className="page-head"><div><span className="eyebrow">EVERY CENT COUNTS</span><h1>我的钱包<span>.</span></h1><p>查看可用余额与每一笔资金记录。</p></div><button className="btn btn-ghost" onClick={()=>void load()} disabled={busy}><Icon name="refresh"/>刷新钱包</button></div>
 {error&&<div className="banner warn" role="alert">{error}<button className="text-button" disabled={busy} onClick={()=>void load()}>重试</button></div>}
 <section className="wallet-overview"><div className="wallet-available"><Icon name="wallet" size={28}/><span>可用余额</span><strong data-testid="wallet-available">{balance?money(balance.availableCents):'—'}</strong><a className="btn" href="#/publish">发布一份委托<Icon name="arrow-right"/></a></div><div className="panel wallet-explain"><h2>每笔赏金，都有去向</h2><p>发布后的赏金转入平台托管账户，可在对应任务详情中查看。</p><p>当前信用：{credit??'暂不可用'} 分。基准 60 分，按最近 30 天信用事件定期校准。</p><p className="funds-muted">预授权冻结余额：{balance?money(balance.frozenCents):'—'}</p></div></section>
 <section className="panel wallet-ledger"><div className="funds-section-title"><h2>资金流水</h2><span className="funds-muted">最近记录优先</span></div>{entries.length===0?<p className="empty-state">{busy?'正在加载资金记录…':'还没有资金流水'}</p>:<ul>{entries.map(e=><li key={e.id}><span className={`ledger-symbol ${e.direction==='CREDIT'?'credit':''}`}><Icon name={e.direction==='CREDIT'?'arrow-down':'arrow-up'}/></span><div className="ledger-copy"><strong>{({OPENING:'演示期初资金',ESCROW:'赏金转入托管',SETTLE:'任务结算收入',REFUND:'托管赏金退款'} as Record<string,string>)[e.refType]||e.refType}</strong><p>{e.refType==='OPENING'?'本地演示账户初始化':<a href={`#/detail/${e.refId}`}>查看对应任务<Icon name="arrow-right" size={12}/></a>}</p><time dateTime={e.time}>{new Date(e.time).toLocaleString('zh-CN')}</time></div><div className="ledger-amount"><strong>{e.direction==='CREDIT'?'+':'−'}{money(e.amountCents)}</strong><small>记账后 {money(e.balanceAfterCents)}</small></div></li>)}</ul>}{cursor&&<button className="btn btn-ghost" disabled={busy} onClick={()=>void load(true)}>加载更早流水</button>}</section>
 </div>;
}
