import {useRef,useState} from 'react';
import {api,ApiError} from '../api';
import type {Card} from '../api';
export default function ConfirmPanel({card,onResolved}:{card:Card;onResolved:()=>void}){
 const [busy,setBusy]=useState(false),[error,setError]=useState('');const lock=useRef(false);
 if(!card.availableActions.includes('CONFIRM'))return null;
 const confirm=async()=>{if(lock.current)return;lock.current=true;setBusy(true);setError('');
  try{await api.confirm(card.id,card.round);onResolved();}
  catch(e){setError(e instanceof ApiError&&e.code==='CONFIRM_RESULT_UNKNOWN'?'确认结果暂不确定，请刷新进度后再操作。':e instanceof Error?e.message:'确认失败，请刷新后重试');onResolved();}
  finally{lock.current=false;setBusy(false);}
 };
 return <section className="panel publish-result"><h2>请确认接下这份委托</h2>{card.confirmDeadline&&<p>请在 <time dateTime={card.confirmDeadline}>{new Date(card.confirmDeadline).toLocaleString('zh-CN')}</time> 前确认，超时将自动递补或重新开放。</p>}{error&&<p role="alert">{error}</p>}<button className="btn btn-primary" disabled={busy} onClick={()=>void confirm()}>{busy?'正在确认…':'确认接单'}</button></section>;
}
