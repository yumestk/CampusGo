import { useEffect, useRef, useState } from 'react';
import { api, money, STATUS_TEXT, TYPE_TEXT, TYPE_ICON, userName } from '../api';
import type { Card } from '../api';
import Icon from '../components/Icon';
import CampusArt from '../components/CampusArt';
import './mobileSquare.css';
import './accountPages.css';
export default function Tasks({ mine }: { mine: boolean }) {
  const [items,setItems] = useState<Card[]>([]), [cursor,setCursor] = useState(''), [role,setRole] = useState('PUBLISHED_BY_ME');
  const [loading,setLoading] = useState(false), [error,setError] = useState(''), [loaded,setLoaded] = useState(false), [query,setQuery] = useState('');
  const version = useRef(0), inFlight = useRef(false);
  const load = async (more = false) => {
    if (inFlight.current) return; inFlight.current = true;
    const current = ++version.current; setLoading(true); setError('');
    try { const page = mine ? await api.mine(role,more ? cursor : '') : await api.list(more ? cursor : ''); if(current === version.current) { setItems(previous => more ? [...previous,...page.items.filter(item => !previous.some(p => p.id === item.id))] : page.items); setCursor(page.nextCursor); setLoaded(true); } }
    catch(e) { if(current === version.current) setError(e instanceof Error ? e.message : '任务加载失败'); }
    finally { if(current === version.current) { setLoading(false); inFlight.current = false; } }
  };
  useEffect(() => { inFlight.current = false; setItems([]); setLoaded(false); setCursor(''); void load(); return () => { ++version.current; }; },[mine,role]);
 const latestLoad=useRef(load);latestLoad.current=load;
 useEffect(()=>{const refresh=()=>void latestLoad.current();window.addEventListener('campusgo:changed',refresh);return()=>window.removeEventListener('campusgo:changed',refresh);},[]);
  const filtered = items.filter(item => `${item.title} ${item.id}`.includes(query.trim()));
  return <div className="square-page"><div className="page-head"><div><span className="eyebrow">CAMPUS LIFE, MADE EASIER</span><h1>{mine ? '我的任务' : '任务广场'}<span className="heading-dot">.</span></h1><p>{mine ? '查看属于你的委托与接单记录。' : '发现校园里的小委托，让每一段顺路都有价值。'}</p></div><button className="btn btn-ghost" onClick={() => void load()} disabled={loading}><Icon name="refresh" size={16}/>刷新任务</button></div>
    {!mine && <section className="square-hero"><div className="hero-copy"><span className="soft-label">校园互助，刚好顺路</span><h2>刚好顺路，<br/><em>刚好帮你。</em></h2><p>一份热饭、一个包裹、一件小事。<br/>在校园，让互相帮助变得简单。</p></div><CampusArt/></section>}
    {mine && <div className="category-tabs"><button className={role === 'PUBLISHED_BY_ME' ? 'active' : ''} onClick={() => setRole('PUBLISHED_BY_ME')}>我发布的</button><button className={role === 'GRABBED_BY_ME' ? 'active' : ''} onClick={() => setRole('GRABBED_BY_ME')}>我接单的</button></div>}
    <section className="task-market" aria-label="任务列表"><div className="section-title"><h2>{mine ? '任务记录' : '发现任务'} <span>{items.length}</span></h2><label className="search-field"><Icon name="search" size={16}/><input aria-label="搜索任务" placeholder="搜索已加载的任务" value={query} onChange={e => setQuery(e.target.value)}/></label></div>
      {error && <div className="banner warn" role="alert">{error}{loaded && '。已保留上次加载结果。'}<button className="text-button" onClick={() => void load()} disabled={loading}>重试</button></div>}
      {loading && !loaded && <p role="status">正在加载任务…</p>}
      <div className="card-grid">{filtered.map(card => <article className="errand-card" key={card.id}><div className="card-top"><span className="category-label"><Icon name={TYPE_ICON[card.type] || 'package'} size={15}/>{TYPE_TEXT[card.type] || card.type}</span><span className={`status status-${card.status}`}>{STATUS_TEXT[card.status] || card.status}</span></div><a className="card-title" href={`#/detail/${card.id}`}>{card.title}</a><div className="card-meta"><span>{userName(card.publisherId)}</span><span className="card-number" title={card.id}>#{card.id}</span></div><div className="card-bottom"><div className="card-reward"><span className="reward-label">悬赏</span><strong className="reward">{money(card.rewardCents)}</strong></div><a className="card-detail" href={`#/detail/${card.id}`}>查看详情<Icon name="arrow-right" size={14}/></a></div></article>)}</div>
      {loaded && !error && filtered.length === 0 && <div className="empty-state"><Icon name="package" size={30}/><h3>{query ? '没有找到匹配的任务' : '暂时没有任务'}</h3><p>稍后刷新，查看新的校园委托。</p></div>}
      {cursor && <button className="btn" disabled={loading} onClick={() => void load(true)}>{loading ? '加载中…' : '加载更多'}</button>}
    </section></div>;
}
