import { useDeferredValue, useEffect, useRef, useState } from 'react';
import { api, money, STATUS_TEXT, TYPE_TEXT, TYPE_ICON, userName } from '../api';
import type { Card } from '../api';
import Icon from '../components/Icon';
import CampusArt from '../components/CampusArt';
import { taskGroup, taskGuidance } from '../taskPresentation';

type View = { role: string; query: string; category: string; group: string; scroll: number };
function readView(key: string): View {
  try {
    const value = JSON.parse(sessionStorage.getItem(key) || '{}');
    return { role: value.role === 'GRABBED_BY_ME' ? value.role : 'PUBLISHED_BY_ME', query: typeof value.query === 'string' ? value.query : '', category: Object.keys(TYPE_TEXT).includes(value.category) ? value.category : 'ALL', group: ['TODO','ONGOING','DONE','DISPUTED'].includes(value.group) ? value.group : 'ALL', scroll: Number.isFinite(value.scroll) ? value.scroll : 0 };
  } catch { return { role: 'PUBLISHED_BY_ME', query: '', category: 'ALL', group: 'ALL', scroll: 0 }; }
}
export default function Tasks({ mine, userId }: { mine: boolean; userId: string }) {
  const storageKey = `campusgo:view:${userId}:${mine ? 'mine' : 'square'}`;
  const [initial] = useState(() => readView(storageKey));
  const [items,setItems] = useState<Card[]>([]), [cursor,setCursor] = useState(''), [role,setRole] = useState(initial.role);
  const [loading,setLoading] = useState(false), [error,setError] = useState(''), [loaded,setLoaded] = useState(false);
  const [query,setQuery] = useState(initial.query), [category,setCategory] = useState(initial.category), [group,setGroup] = useState(initial.group);
  const search = useDeferredValue(query.trim().toLocaleLowerCase());
  const version = useRef(0), inFlight = useRef(false), restored = useRef(false);
  const view = useRef(initial); view.current = { role, query, category, group, scroll: initial.scroll };
  const saveView = () => { try { sessionStorage.setItem(storageKey, JSON.stringify({ ...view.current, scroll: window.scrollY })); } catch { /* Preferences never block business operations. */ } };
  useEffect(() => () => { saveView(); },[storageKey]);
  useEffect(() => { saveView(); },[role,query,category,group]);
  const load = async (more = false) => {
    if (inFlight.current) return; inFlight.current = true;
    const current = ++version.current; setLoading(true); setError('');
    try { const page = mine ? await api.mine(role,more ? cursor : '') : await api.list(more ? cursor : ''); if(current === version.current) { setItems(previous => more ? [...previous,...page.items.filter(item => !previous.some(p => p.id === item.id))] : page.items); setCursor(page.nextCursor); setLoaded(true); } }
    catch(e) { if(current === version.current) setError(e instanceof Error ? e.message : '任务加载失败'); }
    finally { if(current === version.current) { setLoading(false); inFlight.current = false; } }
  };
  useEffect(() => { inFlight.current = false; setItems([]); setLoaded(false); setCursor(''); void load(); return () => { ++version.current; }; },[mine,role]);
  useEffect(() => { if(loaded && !restored.current) { restored.current = true; window.scrollTo(0,initial.scroll); } },[loaded,initial.scroll]);
  const latestLoad=useRef(load);latestLoad.current=load;
  useEffect(()=>{const refresh=()=>void latestLoad.current();window.addEventListener('campusgo:changed',refresh);return()=>window.removeEventListener('campusgo:changed',refresh);},[]);
  const filtered = items.filter(item => `${item.title} ${item.id}`.toLocaleLowerCase().includes(search) && (mine ? group === 'ALL' || taskGroup(item) === group : category === 'ALL' || item.type === category));
  const filters = mine ? [['ALL','全部'],['TODO','待处理'],['ONGOING','进行中'],['DONE','已结束'],['DISPUTED','争议中']] : [['ALL','全部类型'],...Object.entries(TYPE_TEXT)];
  const clearFilters = () => { setQuery(''); setCategory('ALL'); setGroup('ALL'); };
  return <div className={`square-page ${mine ? 'mine-page' : 'market-page'}`}>
    <div className="page-head"><div><span className="eyebrow">{mine ? 'MY CAMPUS ERRANDS' : 'CAMPUS DAYS'}</span><h1>{mine ? '我的任务' : '顺路，帮个忙。'}</h1><p>{mine ? '每一份托付，进展都在这里。' : '取一份快递，带一份热饭。校园里的小事，一起完成。'}</p></div><a className="btn btn-primary" href="#/publish"><Icon name="plus" size={18}/>发布委托</a>{!mine&&<div className="market-heading-art" aria-hidden="true"><CampusArt/><span>Campus<br/><i>days.</i></span></div>}</div>
    {mine && <div className="role-tabs" aria-label="任务关系">{[['PUBLISHED_BY_ME','我发布的'],['GRABBED_BY_ME','我接单的']].map(([value,label])=><button key={value} aria-pressed={role===value} className={role===value?'active':''} onClick={()=>{setRole(value);setGroup('ALL');}}>{label}</button>)}</div>}
    <div className="market-layout"><section className="task-market" aria-label="任务列表">
      <div className="section-title"><h2>{mine ? '任务记录' : '发现任务'}</h2><div className="market-tools"><label className="search-field"><Icon name="search" size={18}/><input aria-label="搜索任务" placeholder="搜索已加载的任务" value={query} onChange={e=>setQuery(e.target.value)}/></label><button className="icon-button" aria-label="刷新任务" title="刷新任务" onClick={()=>void load()} disabled={loading}><Icon name="refresh" size={18}/></button></div></div>
      <div className="filter-tabs" aria-label={mine?'按任务进度筛选':'按任务类型筛选'}>{filters.map(([value,label])=><button key={value} aria-pressed={(mine?group:category)===value} className={(mine?group:category)===value?'active':''} onClick={()=>mine?setGroup(value):setCategory(value)}>{!mine&&<Icon name={value==='ALL'?'grid':TYPE_ICON[value]||'package'} size={16}/>}<span>{label}</span></button>)}</div>
      {mine&&<p className="group-guidance">{({ALL:'查看当前关系下的全部任务。',TODO:'优先处理需要你确认、履约或裁决的任务。',ONGOING:'查看仍在进行或等待其他同学处理的任务。',DONE:'查看已经结束的任务与对应资金结果。',DISPUTED:'争议进展与资金结果，以任务详情为准。'} as Record<string,string>)[group]}</p>}
      <p className="list-caption" role="status">{loaded ? `已加载 ${items.length} 份 · 当前显示 ${filtered.length} 份` : '正在读取任务'} · 搜索与筛选仅针对已加载任务{cursor?'，可继续加载更多。':'。'}</p>
      {error && <div className="banner warn" role="alert">{error}{loaded && '。已保留上次加载结果。'}<button className="text-button" onClick={()=>void load()} disabled={loading}>重试</button></div>}
      {loading&&!loaded&&<div className="loading-state" role="status">正在加载校园委托…</div>}
      <div className="card-grid">{filtered.map(card=><article className={`errand-card type-${card.type}`} key={card.id}><div className="card-top"><span className="category-label"><Icon name={TYPE_ICON[card.type]||'package'} size={17}/>{TYPE_TEXT[card.type]||card.type}</span><span className={`status status-${card.status}`}>{STATUS_TEXT[card.status]||card.status}</span></div><a className="card-title" href={`#/detail/${card.id}?from=${mine?'mine':'square'}`}>{card.title}</a><div className="card-meta"><span>{userName(card.publisherId)}</span><span className="card-number">#{card.id}</span></div>{mine&&<p className="card-next"><Icon name="clock" size={15}/>{taskGroup(card)==='TODO'?`待你处理 · ${taskGuidance(card).next}`:`当前负责：${taskGuidance(card).owner}`}</p>}<div className="card-bottom"><div className="card-reward"><span className="reward-label">悬赏</span><strong className="reward">{money(card.rewardCents)}</strong></div><a className="card-detail" aria-label={`查看任务：${card.title}`} href={`#/detail/${card.id}?from=${mine?'mine':'square'}`}>查看任务<Icon name="arrow-right" size={16}/></a></div></article>)}</div>
      {loaded&&!error&&filtered.length===0&&<div className="empty-state"><Icon name="package" size={32}/><h3>{items.length?'没有匹配的任务':mine?'这里还没有任务':'暂时没有开放的委托'}</h3><p>{items.length?'调整筛选条件，或继续加载更多任务。':mine?(role==='GRABBED_BY_ME'?'去广场寻找适合你的委托，接单后的进展会显示在这里。':'发布一份委托后，可以在这里查看进展。'):'需要帮忙的小事，可以从你的一份委托开始。'}</p>{items.length?<button className="btn" onClick={clearFilters}>清除筛选</button>:<a className="btn" href={mine?'#/square':'#/publish'}>{mine?'去任务广场':'发布第一份委托'}</a>}</div>}
      {cursor&&<div className="list-more"><button className="btn" disabled={loading} onClick={()=>void load(true)}>{loading?'加载中…':'加载更多任务'}</button></div>}
    </section>{!mine&&<aside className="market-rules" aria-label="接单与托管规则"><section className="market-rule-primary"><div className="rule-heading"><Icon name="clock"/><h2>抢到后，再确认</h2></div><h3>顺路的委托，<br/>也值得认真对待。</h3><p>抢到名额后，请在任务详情给出的期限内确认接单，再开始履约。</p><a className="btn btn-primary" href="#/mine">查看我的任务<Icon name="arrow-right" size={18}/></a></section><section className="market-rule-trust"><Icon name="shield" size={28}/><h2>先托管，再结算</h2><p>发布后的赏金转入平台托管。完成结算后，收入会记入钱包。</p><p>每份任务的资金进展，请查看详情中的托管记录。</p><a href="#/wallet">查看资金记录<Icon name="arrow-right" size={16}/></a></section></aside>}</div>
  </div>;
}
