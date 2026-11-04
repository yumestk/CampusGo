import { useEffect,useState } from 'react';
import { api,ApiError,money } from '../api';
import type { Escrow } from '../api';
import Icon from '../components/Icon';
import './funds.css';
export default function EscrowReceipt({id}:{id:string}){
 const [receipt,setReceipt]=useState<Escrow|null>(null),[error,setError]=useState(''),[revision,setRevision]=useState(0);
 useEffect(()=>{let active=true;setError('');void api.escrow(id).then(r=>{if(active)setReceipt(r);}).catch(e=>{if(active){if(e instanceof ApiError&&[401,403,404].includes(e.status))setReceipt(null);setError(e instanceof Error?e.message:'托管记录暂不可用');}});return()=>{active=false;};},[id,revision]);
 return <section className="panel escrow-receipt"><Icon name="shield" size={30}/><div><h2>赏金托管记录</h2>{receipt&&<p>{money(receipt.amountCents)} · {({HELD:'托管中',RELEASED:'已结算',REFUNDED:'已退款'} as Record<string,string>)[receipt.status]||receipt.status}</p>}{error&&<p role="alert">{error}</p>}{!receipt&&!error&&<p>正在查询托管记录…</p>}</div><button className="btn btn-ghost" onClick={()=>setRevision(v=>v+1)}>刷新托管记录</button></section>;
}
