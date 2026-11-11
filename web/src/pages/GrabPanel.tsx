import {useEffect,useRef,useState} from 'react';
import {api} from '../api';
import type {Card,GrabResult,User} from '../api';
import {readGrab,saveGrab,clearGrab} from '../grabOperation';
import type {GrabOperation} from '../grabOperation';

const reasons:Record<string,string>={SLOT_FULL:'这份任务已被其他同学抢到。',STALE_VERSION:'任务轮次或状态已变化，请刷新后查看。',ERRAND_NOT_GRABBABLE:'当前身份或任务状态不允许抢单。',ALREADY_GRABBED:'你已经是这份任务的接单人。',CREDIT_TOO_LOW:'当前信用分未达到接单要求。',TOO_MANY_ONGOING:'在途任务已达上限，请先完成已有任务。',REQUEST_EXPIRED:'本次请求已过期。',GRAB_CONFLICT:'任务已发生变化，请刷新后查看。',ERRAND_NOT_FOUND:'任务不存在。'};
export default function GrabPanel({card,user,onResolved}:{card:Card;user:User;onResolved:()=>void}){
 const [operation,setOperation]=useState<GrabOperation|null>(null),[result,setResult]=useState<GrabResult|null>(null),[error,setError]=useState(''),[busy,setBusy]=useState(false),[blocked,setBlocked]=useState(false);
 const generation=useRef(0),lock=useRef(false),resolved=useRef(onResolved);resolved.current=onResolved;
 useEffect(()=>{
  const current=++generation.current;
  try{const op=readGrab(localStorage,user.id,user.campusId,card.id);if(op){setOperation(op);void api.grabResult(op.key,op.errandId).then(r=>{if(current===generation.current){setResult(r);if(r.state==='SUCCEEDED')resolved.current();}}).catch(e=>{if(current===generation.current)setError(e instanceof Error?e.message:'查询抢单结果失败');});}}
  catch(e){setError(e instanceof Error?e.message:'无法恢复抢单记录');setBlocked(true);}
  return()=>{++generation.current;};
 },[user.id,user.campusId,card.id]);
 const run=async(queryOnly:boolean)=>{
  if(lock.current||blocked)return;lock.current=true;setBusy(true);setError('');const current=++generation.current;
  try{
   let op=operation;
   if(!op){
    const existing=readGrab(localStorage,user.id,user.campusId,card.id);
    if(existing){setOperation(existing);throw Error('已恢复另一窗口的抢单，请先查询结果');}
    if(queryOnly||!card.availableActions.includes('GRAB'))return;
    op={key:crypto.randomUUID(),errandId:card.id,campusId:user.campusId,expectedRound:card.round};
    saveGrab(localStorage,user.id,op);setOperation(op);
   }
   const r=queryOnly?await api.grabResult(op.key,op.errandId):await api.grab(op.key,op.errandId,op.expectedRound);
   if(current===generation.current){setResult(r);if(r.state==='SUCCEEDED'||r.state==='REJECTED'||r.state==='EXPIRED')resolved.current();}
  }catch(e){if(current===generation.current)setError(e instanceof Error?e.message:'抢单结果待确认，请查询结果');}
  finally{if(current===generation.current){lock.current=false;setBusy(false);}}
 };
 const restart=()=>{
  if(!operation||!result||!['REJECTED','EXPIRED','SUCCEEDED'].includes(result.state))return;
  try{clearGrab(localStorage,user.id,operation);++generation.current;setOperation(null);setResult(null);setError('');resolved.current();}
  catch(e){setError(e instanceof Error?e.message:'无法新建抢单');}
 };
 if(!operation&&!blocked&&!card.availableActions.includes('GRAB'))return null;
 const currentReservation=card.status==='LOCKED'&&card.grabberId===user.id&&result?.appliedRound===card.round;
 const failed=result?.state==='REJECTED'||result?.state==='EXPIRED',success=result?.state==='SUCCEEDED';
 return <section className={`panel publish-result grab-panel ${!operation ? "action-panel" : "request-receipt"}`} aria-live="polite">
  <span className="action-kicker">{operation?"本次抢单回执":"下一步 · 申请接单"}</span>
  {!operation?<><h2>顺路的话，帮同学一把</h2><p>{card.status==='LOCKED'?'当前名额已被抢到，可以尝试登记候选；是否获得递补以任务进展为准。':'抢到后进入待确认状态，请留意任务进展。'}</p></>:success?<><h2>{currentReservation?'已抢到，等待确认':'本次抢单曾成功'}</h2><p>{currentReservation?'已为你保留这份委托，请在期限内确认接单。':'当前归属和状态以任务最新进展为准。'}</p></>:failed?<><h2>{result.state==='EXPIRED'?'本次抢单请求已过期':'本次未抢到'}</h2><p>{reasons[result.errorCode]||'任务当前不可抢，请刷新后查看。'}</p></>:<><h2>抢单结果待确认</h2><p>{result?.state==='NOT_OBSERVED'?'暂未查到已提交结果，这不代表抢单失败。':'已保留本次请求。正在处理或连接中断时，请查询结果。'}</p></>}
  {error&&<div className="banner warn" role="alert">{error}</div>}
  <div className="funds-actions">{!operation?<button className="btn btn-primary" disabled={busy||blocked} onClick={()=>void run(false)}>{card.status==='LOCKED'?'尝试候选登记':'立即抢单'}</button>:success?<><a className="btn btn-ghost" href="#/mine">查看我的任务</a>{card.availableActions.includes('GRAB')&&!currentReservation&&<button className="btn btn-primary" disabled={busy} onClick={restart}>准备重新抢单</button>}</>:failed?<button className="btn btn-ghost" disabled={busy||!card.availableActions.includes('GRAB')} onClick={restart}>准备重新抢单</button>:<><button className="btn btn-primary" disabled={busy||blocked} onClick={()=>void run(true)}>查询抢单结果</button><button className="btn btn-ghost" disabled={busy||blocked} onClick={()=>void run(false)}>重试原抢单</button></>}</div>
 </section>;
}
