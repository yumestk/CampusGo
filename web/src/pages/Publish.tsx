import { useEffect, useRef, useState } from 'react';
import { api, ApiError, money, TYPE_TEXT, yuanToCents } from '../api';
import type { PublishResult, User } from '../api';
import { clearOperation, readOperation, saveOperation } from '../publishOperation';
import type { Operation } from '../publishOperation';
import Icon from '../components/Icon';

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
  const titleIssue = error && !operation && (!title.trim() || [...title.trim()].length > 64) ? '请填写 1–64 字的任务标题' : '';
  let rewardIssue = '';
  if (error && !operation) { try { yuanToCents(yuan); } catch (e) { rewardIssue = e instanceof Error ? e.message : '请检查悬赏金额'; } }
  useEffect(() => { if (error) document.getElementById(titleIssue ? 'publish-title' : rewardIssue ? 'publish-reward' : 'publish-error')?.focus(); }, [error]);
  return <div className="funds-page publish-page"><div className="page-head"><div><span className="eyebrow">POST A TASK</span><h1>发布委托</h1><p>说说需要什么帮助，交给顺路的同学。</p></div><a className="btn btn-ghost" href="#/wallet"><Icon name="wallet"/>我的钱包</a></div>
    <div className="publish-layout"><section className="panel publish-form"><span className="action-kicker">01 / 填写委托</span><h2>这次需要什么帮助？</h2><p className="publish-available">可用余额 <strong>{balance===null?'暂未获取':money(balance)}</strong> · 发布成功后赏金转入平台托管</p><form onSubmit={e => {e.preventDefault();void run(false);}}>
      {error && <div id="publish-error" tabIndex={-1} className="banner warn" role="alert">{error}</div>}
      <fieldset disabled={!!operation || busy || blocked}><label>任务类型<select value={type} onChange={e => setType(e.target.value)}>{Object.entries(TYPE_TEXT).map(([value,label]) => <option key={value} value={value}>{label}</option>)}</select></label><label>任务标题<input id="publish-title" value={title} onChange={e => setTitle(e.target.value)} maxLength={128} aria-describedby={`title-help${titleIssue ? ' title-error' : ''}`} aria-invalid={!!titleIssue} placeholder="例如：帮取一份快递，送到宿舍楼下" required/></label>{titleIssue && <p className="field-error" id="title-error">{titleIssue}</p>}<p className="field-help" id="title-help">1–64 字。写清需要的帮助，必要时注明地点和时间。</p><label>悬赏金额（元）<input id="publish-reward" aria-invalid={!!rewardIssue} aria-describedby={`reward-help${rewardIssue ? ' reward-error' : ''}`} value={yuan} onChange={e => setYuan(e.target.value)} inputMode="decimal" placeholder="例如 5.00" required/></label>{rewardIssue && <p className="field-error" id="reward-error">{rewardIssue}</p>}<p className="field-help" id="reward-help">以元填写，最多两位小数；发布成功后按此金额托管。</p><p className="funds-muted">当前校区 · {user.campusId}　接单名额 · 1 人</p></fieldset>
      {!operation && <button className="btn btn-primary" type="submit" disabled={busy || blocked}><Icon name="plus"/>托管赏金并发布</button>}
    </form>
    {operation && <section className="publish-result request-receipt" aria-live="polite"><span className="action-kicker">02 / 发布回执</span>
      {result?.state === 'SUCCEEDED' ? <><h3>发布成功，赏金已托管</h3><p>{money(result.frozenCents)} 已转入平台托管账户。</p><a className="btn btn-primary" href={`#/detail/${result.errandId}`}>查看这份委托<Icon name="arrow-right"/></a></> : result?.state === 'REJECTED' ? <><h3>发布未成功</h3><p>本次没有创建任务，也没有扣款。{result.errorCode === 'INSUFFICIENT_BALANCE' ? '可用余额不足。' : result.errorCode === 'BALANCE_LIMIT' ? '账户已达上限。' : '钱包账户不存在。'}</p></> : <><h3>发布结果待确认</h3><p>已保留本次发布。{result?.state === 'NOT_OBSERVED' ? '暂未查到已提交结果；这不代表发布失败。' : '连接中断时，请先查询结果或重试原请求。'}</p></>}
      <div className="funds-actions">{terminal ? <button className="btn btn-ghost" disabled={busy} onClick={restart}>{result.state === 'SUCCEEDED' ? '再发一单' : '修改后重新发布'}</button> : <><button className="btn btn-primary" disabled={busy} onClick={() => void run(true)}>查询发布结果</button><button className="btn btn-ghost" disabled={busy} onClick={() => void run(false)}>重试原请求</button></>}</div>
    </section>}
    </section><aside className="panel publish-note"><span className="action-kicker">资金如何流转</span><Icon name="shield" size={36}/><h2>赏金先托管</h2><p>发布成功时，赏金从钱包转入平台托管账户；钱包流水与任务进度都可以查询。</p><div className="publish-balance"><span>可用余额</span><strong>{balance === null ? '暂未获取' : money(balance)}</strong></div><ol className="publish-money-flow"><li><strong>发布成功</strong><span>钱包余额 → 赏金托管</span></li><li><strong>完成结算</strong><span>接单同学实收，平台收取 5% 佣金（按分向下取整）</span></li><li><strong>取消或争议</strong><span>按任务状态与权限处理，结果以托管记录为准</span></li></ol><p className="funds-muted">发布后可在任务详情查看托管记录。</p></aside></div>
  </div>;
}
