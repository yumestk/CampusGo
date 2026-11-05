import { useEffect, useRef, useState } from 'react';
import { api, ApiError, money, STATUS_TEXT, TYPE_TEXT, userName } from '../api';
import GrabPanel from './GrabPanel';
import type { User, Card, StatusChange } from '../api';
import Icon from '../components/Icon';
import EscrowReceipt from './EscrowReceipt';
import './taskPages.css';
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
  return <div className="task-detail-page"><div className="task-detail-toolbar"><a className="task-back-link" href="#/square"><Icon name="arrow-left" size={16}/>返回任务广场</a><button className="btn btn-ghost" disabled={loading} onClick={() => void load()}>刷新进度</button></div>
    {error && <div className="banner warn" role="alert">{error}<button className="text-button" onClick={() => void load()} disabled={loading}>重试</button></div>}
    {!card && <section className="panel empty-state" role="status">{loading ? '正在打开这份任务…' : '暂时无法打开任务'}</section>}
    {card && <><section className="panel task-overview"><div className="task-overview-top"><span>{TYPE_TEXT[card.type] || card.type}</span><span className={`status status-${card.status}`}>{STATUS_TEXT[card.status] || card.status}</span></div><h1>{card.title}</h1><span className="task-order-id">任务编号 {card.id}</span><dl className="task-facts"><div><dt>发单同学</dt><dd>{userName(card.publisherId)}</dd></div><div><dt>接单同学</dt><dd>{card.grabberId ? userName(card.grabberId) : '等待接单'}</dd></div><div><dt>悬赏金额</dt><dd>{money(card.rewardCents)}</dd></div><div><dt>当前轮次</dt><dd>{card.round + 1}</dd></div></dl></section><GrabPanel card={card} user={user} onResolved={() => void load()}/>{["PUBLISHER","ARBITRATOR"].includes(card.role) && card.status !== "DRAFT" && <EscrowReceipt key={id} id={id}/>}<section className="panel task-progress"><div className="task-panel-title"><h2>任务进展</h2></div>{timelineError && <p role="alert">进度记录暂时不可用，请稍后刷新。</p>}{events.length === 0 ? <div className="task-timeline-empty">暂无进展记录</div> : <ol className="task-timeline">{events.map((event,index) => <li key={`${event.time}-${index}`}><div className="task-timeline-node"><Icon name="check" size={12}/></div><div className="task-timeline-content"><strong>{STATUS_TEXT[event.to] || event.to}</strong><p>{STATUS_TEXT[event.from] || event.from} → {STATUS_TEXT[event.to] || event.to}</p><time dateTime={event.time}>{new Date(event.time).toLocaleString('zh-CN')}</time></div></li>)}</ol>}</section></>}
  </div>;
}
