import {useCallback,useEffect,useRef,useState} from 'react';
import {api,userName} from '../api';
import type {CreditProfile,CreditRank} from '../api';
import Icon from '../components/Icon';
import './accountPages.css';

export default function Credit({userId}:{userId:string}) {
 const [self,setSelf]=useState<CreditProfile|null>(null),[ranking,setRanking]=useState<CreditRank[]|null>(null);
 const [filter,setFilter]=useState('ALL'),[loading,setLoading]=useState(true),[error,setError]=useState('');
 const generation=useRef(0);
 const load=useCallback(async()=>{
  const current=++generation.current;setLoading(true);setError('');
  const [profile,rank]=await Promise.allSettled([api.credit(),api.creditRanking()]);
  if(current!==generation.current)return;
  const errors:string[]=[];
  if(profile.status==='fulfilled')setSelf(profile.value);else errors.push('信用记录加载失败，请重试');
  if(rank.status==='fulfilled')setRanking(rank.value);else errors.push('排行榜加载失败，请重试');
  setError(errors.join('；'));setLoading(false);
 },[]);
 useEffect(()=>{void load();const refresh=()=>void load();window.addEventListener('campusgo:changed',refresh);return()=>{++generation.current;window.removeEventListener('campusgo:changed',refresh);};},[load]);
 const events=self?.events.filter(e=>filter==='ALL'||(filter==='POSITIVE'?e.delta>0:e.delta<0))??[];
 return <div className="account-page account-credit-page">
  <div className="page-head"><div><span className="eyebrow">CAMPUS CREDIT</span><h1>靠谱，值得被看见</h1><p>每一次认真履约，都在积累校园里的信任。</p></div><button className="btn btn-ghost" disabled={loading} onClick={()=>void load()}><Icon name="refresh"/>刷新信用</button></div>
  {error&&<div className="banner warn" role="alert">{error}</div>}
  <section className="account-credit-overview" aria-label="我的信用概览">
   <div className="account-credit-score"><div className="account-credit-score-title"><Icon name="shield"/>我的信用分</div><div className="account-score-number" data-testid="credit-score">{self?.score??'—'}<span>分</span></div><span className="account-score-window">最近 {self?.windowDays??30} 天的履约记录</span><p>信用来自每一次约定的兑现</p></div>
   <div className="account-credit-rules"><h2>信用如何积累？</h2><p>初始 60 分，范围 0—100 分，按最近 30 天事件定期校准。</p><div className="account-rule-grid">{[['+2','完成结算'],['−5','抢中后超时未确认'],['−8','争议败诉']].map(([delta,label])=><div key={label}><span className={`account-rule-symbol ${delta.startsWith('+')?'account-positive':'account-negative'}`}>{delta}</span><strong>{label}</strong></div>)}</div><p>信用记录展示最近 50 条；校区榜单最多延迟 30 秒更新。</p></div>
  </section>
  <div className="account-credit-layout">
   <section className="account-surface" aria-label="信用足迹"><div className="account-section-head"><h2>信用足迹</h2><span className="account-count">最近 30 天</span></div><div className="account-filter-bar"><div className="tabs" aria-label="按信用变化筛选">{[['ALL','全部记录'],['POSITIVE','加分记录'],['NEGATIVE','扣分记录']].map(([value,label])=><button key={value} className={`tab ${filter===value?'active':''}`} aria-pressed={filter===value} onClick={()=>setFilter(value)}>{label}</button>)}</div></div>
    {loading&&!self?<p className="account-state" role="status">正在加载信用记录…</p>:!self?<p className="account-state">信用记录暂不可用，请刷新重试。</p>:events.length===0?<p className="account-state">暂无这类信用记录</p>:<div className="account-credit-events">{events.map(e=><div className="account-credit-event" key={e.id}><span className="account-event-dot"><Icon name={e.delta>0?'check':'clock'}/></span><div><strong>{e.description}</strong><span><time dateTime={e.time}>{new Date(e.time).toLocaleString('zh-CN')}</time><a href={`#/detail/${e.refId}`}>查看对应任务</a></span></div><b className={e.delta>0?'account-positive':'account-negative'}>{e.delta>0?'+':''}{e.delta}</b></div>)}</div>}
   </section>
   <section className="account-surface" aria-label="校园信用榜"><div className="account-section-head"><div><h2>校园信用榜</h2><p>当前校区 · 前 20 名</p></div></div>{ranking===null?<p className="account-state">{loading?'正在加载榜单…':'榜单暂不可用，请刷新重试。'}</p>:ranking.length===0?<p className="account-state">等待第一位上榜同学</p>:<div className="account-rank-list">{ranking.map((e,i)=><div className={`account-rank-row ${e.userId===userId?'account-rank-self':''}`} key={e.userId}><span className="account-rank-number">{i+1}</span><span className="account-rank-user">{userName(e.userId)}{e.userId===userId&&<small>我</small>}</span><strong>{e.score}<small> 分</small></strong></div>)}</div>}</section>
  </div>
 </div>;
}
