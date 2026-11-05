import { useEffect, useRef, useState } from 'react';
import { api } from '../api';
export type Notice = {id:string;errandId:string;type:string;content:string;read:boolean;time:string};
export type NoticePage = {items:Notice[];nextCursor:string};
// A hint invalidates views; it never becomes a financial or notification fact.
export function useRealtime(userId:string){
 const [state,setState]=useState('连接中');
 useEffect(()=>{
  let stopped=false,ws:WebSocket|undefined,retry:ReturnType<typeof setTimeout>,attempt=0;
  const invalidate=()=>window.dispatchEvent(new Event('campusgo:changed'));
  const connect=async()=>{
   try{
    await api.me();if(stopped)return;
    const url=new URL('/api/ws',location.href);url.protocol=location.protocol==='https:'?'wss:':'ws:';
    ws=new WebSocket(url);const current=ws;
    current.onopen=()=>{if(!stopped)current.send(JSON.stringify({token:api.getToken()}));};
    current.onmessage=e=>{if(stopped)return;try{const event=JSON.parse(e.data);if(event.type==='ready'){attempt=0;setState('实时已连接');invalidate();}else if(['notification.new','state.changed'].includes(event.type)){invalidate();}}catch{/* Polling recovers malformed or lost hints. */}};
    current.onerror=()=>current.close();
    current.onclose=()=>{if(!stopped){setState('实时重连中，定时查询可用');schedule();}};
   }catch{if(!stopped){setState('实时暂不可用，定时查询中');schedule();}}
  };
  const schedule=()=>{retry=setTimeout(()=>void connect(),Math.min(30_000,1000*2**Math.min(attempt++,5))+Math.random()*300);};
  void connect();const poll=setInterval(invalidate,15_000);
  const visible=()=>{if(document.visibilityState==='visible')invalidate();};document.addEventListener('visibilitychange',visible);
  return()=>{stopped=true;clearTimeout(retry);clearInterval(poll);document.removeEventListener('visibilitychange',visible);ws?.close();};
 },[userId]);
 return state;
}
export function NotificationBadge({userId}:{userId:string}){
 const [count,setCount]=useState('0');const state=useRealtime(userId);
 useEffect(()=>{let active=true,busy=false;const load=async()=>{if(busy)return;busy=true;try{const n=await api.request<{count:string}>('/api/notifications/unread');if(active)setCount(n.count);}catch{if(active)setCount('—');}finally{busy=false;}};void load();window.addEventListener('campusgo:changed',load);return()=>{active=false;window.removeEventListener('campusgo:changed',load);};},[userId]);
 return <a href="#/notifications" className="btn btn-ghost" title={state} aria-label={`通知，${count} 条未读`}>通知 <span data-testid="unread-count">{count}</span></a>;
}
export default function NotificationCenter(){
 const [items,setItems]=useState<Notice[]>([]),[cursor,setCursor]=useState(''),[busy,setBusy]=useState(false),[error,setError]=useState('');
 const alive=useRef(true),lock=useRef(false),expanded=useRef(false);
 const load=async(more=false)=>{if(lock.current)return;lock.current=true;setBusy(true);try{const p=await api.request<NoticePage>(`/api/notifications?cursor=${encodeURIComponent(more?cursor:'')}`);if(alive.current){if(more)expanded.current=true;setItems(old=>more?[...old,...p.items.filter(n=>!old.some(o=>o.id===n.id))]:expanded.current?[...p.items,...old.filter(n=>!p.items.some(o=>o.id===n.id))]:p.items);if(more||!expanded.current)setCursor(p.nextCursor);setError('');}}catch(e){if(alive.current)setError(e instanceof Error?e.message:'通知暂不可用');}finally{lock.current=false;if(alive.current)setBusy(false);}};
 useEffect(()=>{alive.current=true;void load();const refresh=()=>void load();window.addEventListener('campusgo:changed',refresh);return()=>{alive.current=false;window.removeEventListener('campusgo:changed',refresh);};},[]);
 const read=async(n:Notice)=>{if(lock.current)return;lock.current=true;setBusy(true);try{await api.request(`/api/notifications/${n.id}/read`,'POST');if(alive.current){setItems(old=>old.map(o=>o.id===n.id?{...o,read:true}:o));window.dispatchEvent(new Event('campusgo:changed'));}}catch(e){if(alive.current)setError(e instanceof Error?e.message:'标记失败，请重试');}finally{lock.current=false;if(alive.current)setBusy(false);}};
 return <div className="funds-page"><div className="page-head"><div><h1>站内通知<span>.</span></h1><p>结算与退款结果保存在这里，离线期间的消息也可查询。</p></div><button className="btn btn-ghost" disabled={busy} onClick={()=>{expanded.current=false;void load();}}>刷新通知</button></div>{error&&<div className="banner warn" role="alert">{error}，可刷新重试。</div>}<section className="panel notice-list" aria-label="通知列表">{items.length===0?<p className="empty-state">{busy?'正在加载通知…':'暂无通知'}</p>:items.map(n=><article key={n.id} className="notice-item"><div><strong>{n.read?'已读':'未读'} · {n.type.startsWith('ARBITRATE')?'争议裁决':n.type==='CANCEL'?'任务退款':'任务结算'}</strong><p>{n.content}</p><time dateTime={n.time}>{new Date(n.time).toLocaleString('zh-CN')}</time></div><div><a className="btn btn-ghost" href={`#/detail/${n.errandId}`}>查看任务</a>{!n.read&&<button className="btn btn-ghost" disabled={busy} onClick={()=>void read(n)}>标为已读</button>}</div></article>)}{cursor&&<button className="btn" disabled={busy} onClick={()=>void load(true)}>加载更早通知</button>}</section><p>实时提示为尽力送达；页面每 15 秒补查，资金结果以钱包与任务记录为准。</p></div>;
}
