import { useEffect, useRef, useState } from 'react';
import { api, ApiError, money, TYPE_TEXT, yuanToCents } from '../api';
import type { PublishResult, User } from '../api';
import { clearOperation, readOperation, saveOperation } from '../publishOperation';
import type { Operation } from '../publishOperation';
import Icon from '../components/Icon';
import './funds.css';

export default function Publish({user}: {user: User}) {
  const [title,setTitle] = useState(''),[type,setType] = useState('DELIVERY'),[yuan,setYuan] = useState('');
  const [operation,setOperation] = useState<Operation | null>(null),[result,setResult] = useState<PublishResult | null>(null);
  const [balance,setBalance] = useState<string | null>(null),[error,setError] = useState(''),[busy,setBusy] = useState(false),[blocked,setBlocked] = useState(false);
  const lock = useRef(false), generation = useRef(0);
  const accept = (op: Operation) => { setOperation(op); setTitle(op.command.title); setType(op.command.type); const amount = BigInt(op.command.rewardCents); setYuan(`${amount / 100n}.${(amount % 100n).toString().padStart(2,'0')}`); };
  useEffect(() => {
    const current = ++generation.current;
    try {
      const op = readOperation(localStorage,user.id,user.campusId);
      if (op) { accept(op); void api.publishResult(op.key).then(r => {if (current === generation.current) setResult(r);}).catch(e => {if (current === generation.current) setError(e instanceof Error ? e.message : '结果查询失败');}); }
    } catch (e) { setError(e instanceof Error ? e.message : '无法恢复发布记录'); setBlocked(true); }
    void api.balance().then(b => {if (current === generation.current) setBalance(b.availableCents);}).catch(() => {});
    return () => {++generation.current;};
  },[user.id,user.campusId]);
  const run = async (queryOnly: boolean) => {
    if (lock.current || blocked) return; lock.current = true; setBusy(true); setError(''); const current = ++generation.current;
    let op = operation;
    try {
      if (!op) {
        // Persist before the first network write. Storage failure must not create an unrecoverable request.
        const existing = readOperation(localStorage,user.id,user.campusId);
        if (existing) { accept(existing); throw new Error('已恢复原发布，请先查询结果或重试原请求'); }
        const trimmed = title.trim(); if (!trimmed || [...trimmed].length > 64) throw new Error('请填写 1–64 字的任务标题');
        op = {key:crypto.randomUUID(),command:{campusId:user.campusId,type,title:trimmed,rewardCents:yuanToCents(yuan),slotTotal:1}};
        saveOperation(localStorage,user.id,op); accept(op);
      }
      const r = queryOnly ? await api.publishResult(op.key) : await api.publish(op.key,op.command);
      if (current === generation.current) setResult(r);
      if (r.state !== 'NOT_OBSERVED') void api.balance().then(b => {if (current === generation.current) setBalance(b.availableCents);}).catch(() => {});
    } catch (e) {
      if (current !== generation.current) return;
      setError(e instanceof Error ? e.message : '请求失败，请查询结果');
      if (op && e instanceof ApiError && ['INSUFFICIENT_BALANCE','BALANCE_LIMIT','ACCOUNT_NOT_FOUND'].includes(e.code)) setResult({requestKey:op.key,state:'REJECTED',errandId:null,status:'',frozenCents:'0',errorCode:e.code});
    } finally { if (current === generation.current) { lock.current = false; setBusy(false); } }
  };
  const restart = () => {
    if (!operation || !result || result.state === 'NOT_OBSERVED') return;
    try { clearOperation(localStorage,user.id,operation); ++generation.current; const current = generation.current; setOperation(null); setResult(null); setError(''); void api.balance().then(b => {if(current === generation.current)setBalance(b.availableCents);}).catch(() => {}); }
    catch (e) {setError(e instanceof Error ? e.message : '无法创建新发布');}
  };
  const terminal = result?.state === 'SUCCEEDED' || result?.state === 'REJECTED';
  return <div className="funds-page"><div className="page-head"><div><span className="eyebrow">A LITTLE HELP</span><h1>发布委托<span>.</span></h1><p>说说需要什么帮助，交给顺路的同学。</p></div><a className="btn btn-ghost" href="#/wallet"><Icon name="wallet"/>我的钱包</a></div>
    <div className="publish-layout"><section className="panel publish-form"><h2>这次需要什么帮助？</h2><form onSubmit={e => {e.preventDefault();void run(false);}}>
      <fieldset disabled={!!operation || busy || blocked}><label>任务类型<select value={type} onChange={e => setType(e.target.value)}>{Object.entries(TYPE_TEXT).map(([value,label]) => <option key={value} value={value}>{label}</option>)}</select></label><label>任务标题<input value={title} onChange={e => setTitle(e.target.value)} maxLength={128} placeholder="例如：帮取一份快递，送到宿舍楼下" required/></label><label>悬赏金额（元）<input value={yuan} onChange={e => setYuan(e.target.value)} inputMode="decimal" placeholder="例如 5.00" required/></label><p className="funds-muted">当前校区 · {user.campusId}　接单名额 · 1 人</p></fieldset>
      {error && <div className="banner warn" role="alert">{error}</div>}
      {!operation && <button className="btn btn-primary" type="submit" disabled={busy || blocked}><Icon name="plus"/>托管赏金并发布</button>}
    </form>
    {operation && <section className="publish-result" aria-live="polite">
      {result?.state === 'SUCCEEDED' ? <><h3>发布成功，赏金已托管</h3><p>{money(result.frozenCents)} 已转入平台托管账户。</p><a className="btn btn-primary" href={`#/detail/${result.errandId}`}>查看这份委托<Icon name="arrow-right"/></a></> : result?.state === 'REJECTED' ? <><h3>发布未成功</h3><p>本次没有创建任务，也没有扣款。{result.errorCode === 'INSUFFICIENT_BALANCE' ? '可用余额不足。' : result.errorCode === 'BALANCE_LIMIT' ? '账户已达上限。' : '钱包账户不存在。'}</p></> : <><h3>发布结果待确认</h3><p>已保留本次发布。{result?.state === 'NOT_OBSERVED' ? '暂未查到已提交结果；这不代表发布失败。' : '连接中断时，请先查询结果或重试原请求。'}</p></>}
      <div className="funds-actions">{terminal ? <button className="btn btn-ghost" disabled={busy} onClick={restart}>{result.state === 'SUCCEEDED' ? '再发一单' : '修改后重新发布'}</button> : <><button className="btn btn-primary" disabled={busy} onClick={() => void run(true)}>查询发布结果</button><button className="btn btn-ghost" disabled={busy} onClick={() => void run(false)}>重试原请求</button></>}</div>
    </section>}
    </section><aside className="panel publish-note"><span className="soft-label">放心托付</span><Icon name="shield" size={36}/><h2>赏金先托管</h2><p>发布成功时，赏金从钱包转入平台托管账户；钱包流水与任务进度都可以查询。</p><div className="publish-balance"><span>可用余额</span><strong>{balance === null ? '暂未获取' : money(balance)}</strong></div><p className="funds-muted">发布后可在任务详情查看托管记录。</p></aside></div>
  </div>;
}
