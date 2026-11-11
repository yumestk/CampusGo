import { useEffect, useRef, useState } from 'react';
import { api, IDENTITIES } from './api';
import type { AuthConfig, Session, User } from './api';
import { useHashRoute } from './router';
import Icon from './components/Icon';
import CampusArt from './components/CampusArt';
import Tasks from './pages/Tasks';
import Detail from './pages/Detail';
import Publish from './pages/Publish';
import Wallet from './pages/Wallet';
import Credit from './pages/Credit';
import NotificationCenter, { NotificationBadge } from './components/NotificationCenter';

export default function App() {
  const route = useHashRoute();
  const [user, setUser] = useState<User | null>(null), [config, setConfig] = useState<AuthConfig | null>(null);
  const [loading, setLoading] = useState(true), [busy, setBusy] = useState(false), [error, setError] = useState('');
  const generation = useRef(0), loginLock = useRef(false);
  const popupCleanup = useRef<() => void>(() => {});
  const oidcTrigger = useRef<HTMLButtonElement>(null), restoreOidcFocus = useRef(false);
  useEffect(() => {
    if (!busy && restoreOidcFocus.current) { restoreOidcFocus.current = false; oidcTrigger.current?.focus(); }
  }, [busy]);
  const restore = async () => {
    const current = ++generation.current; setLoading(true); setError('');
    try {
      const nextConfig = await api.config(); if (current !== generation.current) return; setConfig(nextConfig);
      if (api.getToken()) { const next = await api.me(); if (current === generation.current) setUser(next); }
    } catch (e) { if (current === generation.current) setError(e instanceof Error ? e.message : '加载失败'); }
    finally { if (current === generation.current) setLoading(false); }
  };
  useEffect(() => {
    const logout = () => { ++generation.current; setUser(null); setLoading(false); };
    window.addEventListener('campusgo:logout', logout); void restore();
    return () => { ++generation.current; window.removeEventListener('campusgo:logout', logout); popupCleanup.current(); };
  }, []);
  const login = async (id: string) => {
    if (loginLock.current) return; loginLock.current = true; setBusy(true); setError('');
    try { await api.login(id); setUser(await api.me()); }
    catch (e) { setError(e instanceof Error ? e.message : '登录失败'); }
    finally { loginLock.current = false; setBusy(false); }
  };
  const oidc = () => {
    if (!config?.oidcEnabled || loginLock.current) return;
    const popup = window.open(`${config.oidcCallbackOrigin}/api/auth/oidc/start`, 'campusgo-identity', 'width=560,height=760');
    if (!popup) { setError('请允许打开登录窗口后重试'); return; }
    loginLock.current = true; setBusy(true); setError('');
    const detach = () => { clearInterval(poll); clearTimeout(timeout); window.removeEventListener('message', receive); popupCleanup.current = () => {}; };
    const cleanup = () => { detach(); loginLock.current = false; restoreOidcFocus.current = true; setBusy(false); };
    const receive = async (event: MessageEvent) => {
      if (event.source !== popup || event.origin !== config.oidcCallbackOrigin || event.data?.code !== 'OK') return;
      detach();
      try { api.acceptSession(event.data.data as Session); setUser(await api.me()); }
      catch (e) { setError(e instanceof Error ? e.message : '登录失败'); }
      finally { loginLock.current = false; setBusy(false); }
    };
    const poll = window.setInterval(() => { if (popup.closed) { cleanup(); setError('登录窗口已关闭；未完成时可重新登录'); } }, 500);
    const timeout = window.setTimeout(() => { cleanup(); popup.close(); setError('登录已超时，请重新开始'); }, 5 * 60_000);
    window.addEventListener('message', receive); popupCleanup.current = () => { cleanup(); popup.close(); };
  };
  useEffect(() => {
    if (user) { window.scrollTo(0,0); document.getElementById('main-content')?.focus({preventScroll:true}); }
  },[route,user?.id]);
  const logout = async () => {
    if (busy) return; setBusy(true); setError('');
    try { await api.logout(); } catch (e) { setError(e instanceof Error ? e.message : '退出失败，请重试'); } finally { setBusy(false); }
  };
  if (loading) return <main className="login-page connection-page"><section className="login-card" role="status">正在连接校园服务…</section></main>;
  if (!user) return <main className="login-page">
    <section className="login-story"><a className="brand" href="#/square"><span className="brand-mark"><Icon name="package"/></span><span>CampusGo<small>校园里的顺路好帮手</small></span></a><div className="login-story-body"><span className="eyebrow">A LITTLE HELP, A BETTER DAY</span><h1>校园里的小事，<br/><em>交给顺路的同学。</em></h1><p>取件、带饭、买东西。让你的时间，<br/>留给更重要的生活。</p><div className="login-note"><CampusArt/><strong>小事有人帮，生活多一点余裕。</strong><span>发布委托 · 赏金托管 · 认真履约</span></div></div></section>
    <section className="login-side"><div className="login-card"><span className="soft-label">校园里的顺路好帮手</span><h2>欢迎回来</h2><p className="login-description">登录后查看校园委托与任务进度。</p>
      {config?.oidcEnabled && <button ref={oidcTrigger} className="btn btn-primary" disabled={busy} onClick={oidc}>使用统一身份登录<Icon name="arrow-right" size={16}/></button>}
      {config?.demoLoginEnabled && <><p className="login-description">也可以选择本地演示身份</p><div className="identity-list">{IDENTITIES.map(identity => <button className="identity-btn" key={identity.id} disabled={busy} onClick={() => void login(identity.id)}><span className="identity-icon"><Icon name={identity.icon}/></span><span className="identity-copy"><strong>{identity.name}<small>#{identity.id}</small></strong><span>进入任务广场</span></span><Icon name="arrow-right" size={18}/></button>)}</div></>}
      {error && <div className="banner warn" role="alert">{error}<button className="text-button" onClick={() => void restore()} disabled={busy}>重试连接</button></div>}
      {api.getToken() && error && <p>会话已保留，连接恢复后可重试。</p>}
    </div></section>
  </main>;
  const path = route.split('?')[0], detail = path.startsWith('/detail/') ? path.split('/')[2] : null;
  const activePath = detail ? (new URLSearchParams(route.split('?')[1]).get('from')==='mine'?'/mine':'/square') : path;
  return <div className="app"><a className="skip-link" href="#main-content" onClick={event=>{event.preventDefault();document.getElementById('main-content')?.focus();}}>跳到主要内容</a>
    <aside className="sidebar"><a className="brand" href="#/square"><span className="brand-mark"><Icon name="package"/></span><span>CampusGo<small>校园里的顺路好帮手</small></span></a><div className="campus-selector"><Icon name="pin"/><span>当前校区 · {user.campusId}</span></div><nav>{[['/square','任务广场','grid'],['/mine','我的任务','receipt'],['/wallet','我的钱包','wallet'],['/credit','信用中心','shield'],['/notifications','站内通知','receipt'],['/publish','发布委托','plus']].map(([href,label,icon]) => <a className={`nav-item ${activePath===href ? 'active' : ''}`} aria-current={activePath===href ? 'page' : undefined} href={`#${href}`} key={href}><Icon name={icon}/>{label}</a>)}</nav><div className="sidebar-art" aria-hidden="true"><CampusArt/><p>走吧，<br/>刚好顺路。</p></div><div className="sidebar-bottom"><div className="profile"><span className="avatar">{user.name.slice(0,1)}</span><span>{user.name}<small> · #{user.id}</small></span></div></div></aside>
    <header className="topbar"><div className="topbar-left"><a href="#/square" className="mobile-app-title">CampusGo</a><span className="breadcrumb">校园生活 / <strong>{detail ? '任务详情' : path === '/mine' ? '我的任务' : path === '/publish' ? '发布委托' : path === '/wallet' ? '我的钱包' : path === '/notifications' ? '站内通知' : path === '/credit' ? '信用中心' : '任务广场'}</strong></span></div><div className="topbar-right"><NotificationBadge key={user.id} userId={user.id}/><span>{user.name}</span>{config?.authMode==='jwt'&&<button className="btn btn-ghost" disabled={busy} onClick={async()=>{setBusy(true);setError('');try{await api.refresh();setUser(await api.me());}catch(e){setError(e instanceof Error?e.message:'续期失败，请重试');}finally{setBusy(false);}}}>续期登录</button>}<button className="btn btn-ghost" onClick={() => void logout()} disabled={busy}><Icon name="logout" size={16}/>退出登录</button></div></header>
    <main className="main" id="main-content" tabIndex={-1}><div className="content" key={user.id}>{error && <div className="banner warn" role="alert">{error}</div>}{detail ? <Detail key={detail} id={detail} user={user}/> : path === '/publish' ? <Publish user={user}/> : path === '/notifications' ? <NotificationCenter/> : path === '/credit' ? <Credit userId={user.id}/> : path === '/wallet' ? <Wallet/> : <Tasks key={path} mine={path === '/mine'} userId={user.id}/>}<footer className="workspace-footer"><span>CampusGo</span><span>每一份托付，都认真送达。</span></footer></div></main>
    <nav className="mobile-bottom-nav" aria-label="底部导航">{[['/square','广场','grid'],['/mine','我的任务','receipt'],['/wallet','钱包','wallet'],['/credit','信用','shield'],['/publish','发布','plus']].map(([href,label,icon])=><a className="mobile-tab" aria-current={activePath===href?'page':undefined} href={`#${href}`} key={href}><Icon name={icon}/><strong>{label}</strong></a>)}</nav>
  </div>;
}
