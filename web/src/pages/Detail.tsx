import { useEffect, useRef, useState } from 'react';
import { api, ApiError, money, STATUS_TEXT, TYPE_TEXT, userName } from '../api';
import GrabPanel from './GrabPanel';
import ConfirmPanel from './ConfirmPanel';
import FulfillmentPanel from './FulfillmentPanel';
import type { User, Card, StatusChange } from '../api';
import Icon from '../components/Icon';
import EscrowReceipt from './EscrowReceipt';
import { taskGuidance } from '../taskPresentation';
export default function Detail({ id,user }: { id: string;user:User }) {
  const [card,setCard] = useState<Card | null>(null), [events,setEvents] = useState<StatusChange[]>([]), [error,setError] = useState(''), [timelineError,setTimelineError] = useState(false), [loading,setLoading] = useState(false);
  const version = useRef(0), lock = useRef(false);
  const load = async () => {
    if(lock.current) return; lock.current = true; const current = ++version.current; setLoading(true); setError('');
    const [detail,timeline] = await Promise.allSettled([api.detail(id),api.timeline(id)]);
    if(current !== version.current) return;
    if(detail.status === 'fulfilled') setCard(detail.value); else {
      setError(detail.reason instanceof Error ? detail.reason.message : '任务加载失败');
      if(detail.reason instanceof ApiError && [401,403,404].includes(detail.reason.status)) { setCard(null); setEvents([]); }
    }
    if(timeline.status === 'fulfilled') { setEvents(timeline.value); setTimelineError(false); } else setTimelineError(true);
    setLoading(false); lock.current = false;
  };
  useEffect(() => { lock.current = false; void load(); return () => { ++version.current; }; },[id]);
 const latestLoad=useRef(load);latestLoad.current=load;
 useEffect(()=>{const refresh=()=>void latestLoad.current();window.addEventListener('campusgo:changed',refresh);return()=>window.removeEventListener('campusgo:changed',refresh);},[]);
  const fromMine = new URLSearchParams(location.hash.split('?')[1]).get('from') === 'mine';
  const guidance = card ? taskGuidance(card) : null;
  const responsible = card && guidance?.owner==='接单同学' && card.grabberId ? userName(card.grabberId) : card && guidance?.owner==='发单同学' ? userName(card.publisherId) : '';
  const responsibility = guidance?.owner + (responsible && responsible!==guidance?.owner ? ` · ${responsible}` : '');
  return <div className="task-detail-page"><div className="task-detail-toolbar"><a className="task-back-link" href={fromMine?"#/mine":"#/square"}><Icon name="arrow-left" size={16}/>{fromMine?"返回我的任务":"返回任务广场"}</a><button className="btn btn-ghost" disabled={loading} onClick={() => void load()}>刷新进度</button></div>
    {error && <div className="banner warn" role="alert">{error}{card && '。已保留上次查询的任务，状态可能滞后。'}<button className="text-button" onClick={() => void load()} disabled={loading}>重试</button></div>}
    {!card && <section className="panel empty-state" role="status">{loading ? '正在打开这份任务…' : '暂时无法打开任务'}</section>}
    {card && <><section className="task-overview"><div className="task-overview-top"><span className="eyebrow">TASK FILE</span><span>{TYPE_TEXT[card.type] || card.type}</span><span className={`status status-${card.status}`}>{STATUS_TEXT[card.status] || card.status}</span></div><div className="task-overview-heading"><div><h1>{card.title}</h1><span className="task-order-id">任务编号 {card.id}</span></div><div className="detail-reward"><span>悬赏金额</span><strong>{money(card.rewardCents)}</strong></div></div><div className="task-current"><div><span>当前负责</span><strong>{responsibility}</strong></div><div><span>下一步</span><p>{guidance?.next}</p></div></div></section><div className="task-detail-layout"><div className="task-detail-main"><section className="panel"><h2>委托信息</h2><dl className="task-facts"><div><dt>发单同学</dt><dd>{userName(card.publisherId)}</dd></div><div><dt>接单同学</dt><dd>{card.grabberId ? userName(card.grabberId) : '等待接单'}</dd></div><div><dt>你在这份委托中的身份</dt><dd>{({PUBLISHER:"发单同学",CURRENT_RUNNER:"接单同学",ARBITRATOR:"平台仲裁员"} as Record<string,string>)[card.role] || "同校同学"}</dd></div><div><dt>当前轮次</dt><dd>{card.round + 1}</dd></div></dl></section><section className="panel task-progress"><div className="task-panel-title"><h2>任务进展</h2></div>{timelineError && <p role="alert">进度记录暂时不可用，请稍后刷新。</p>}{events.length === 0 ? <div className="task-timeline-empty">暂无进展记录</div> : <ol className="task-timeline">{events.map((event,index) => <li key={`${event.time}-${index}`}><div className="task-timeline-node"><Icon name="check" size={12}/></div><div className="task-timeline-content"><strong>{STATUS_TEXT[event.to] || event.to}</strong><p>{STATUS_TEXT[event.from] || event.from} → {STATUS_TEXT[event.to] || event.to}</p><time dateTime={event.time}>{new Date(event.time).toLocaleString('zh-CN')}</time></div></li>)}</ol>}</section></div><aside className="task-detail-aside" aria-label="当前操作与托管"><FulfillmentPanel key={`${user.id}:${id}`} card={card} user={user} onResolved={()=>void load()}/><ConfirmPanel card={card} onResolved={() => void load()}/><GrabPanel card={card} user={user} onResolved={() => void load()}/>{["PUBLISHER","ARBITRATOR"].includes(card.role) && card.status !== "DRAFT" && <EscrowReceipt key={`${id}:${card.status}`} id={id}/>}<section className="panel reward-summary"><h2>结算说明</h2><p>完成结算时，平台收取赏金的 5% 作为佣金，按整数分向下取整。</p><dl><div><dt>托管赏金</dt><dd>{money(card.rewardCents)}</dd></div><div><dt>按此赏金结算实收</dt><dd>{money((BigInt(card.rewardCents)-BigInt(card.rewardCents)/20n).toString())}</dd></div></dl><p>此处为结算金额说明，实际资金状态以托管记录与钱包流水为准。</p></section></aside></div></>}
  </div>;
}
