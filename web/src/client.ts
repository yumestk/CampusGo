export interface User { id: string; campusId: string; name: string; role: string }
export interface Session { token: string; userId: string; expiresIn: string }
export interface Card { id: string; campusId: string; publisherId: string; grabberId: string | null; title: string; type: string; status: string; rewardCents: string; slotTaken: number; slotTotal: number; round: number; confirmDeadline?: string | null; role: string; availableActions: string[] }
export interface Page { items: Card[]; nextCursor: string }
export interface StatusChange { time: string; from: string; to: string; round: number; operatorId: string }
export interface AuthConfig { demoLoginEnabled: boolean; oidcEnabled: boolean; oidcCallbackOrigin: string }
export interface Balance { availableCents: string; frozenCents: string; version: string }
export interface LedgerEntry { id: string; time: string; direction: string; amountCents: string; balanceAfterCents: string; accountVersion: string; refType: string; refId: string; bizNo: string }
export interface LedgerPage { items: LedgerEntry[]; nextCursor: string }
export interface Escrow { errandId: string; amountCents: string; status: string }
export interface PublishCommand { campusId: string; type: string; title: string; rewardCents: string; slotTotal: number }
export interface PublishResult { requestKey: string; state: 'SUCCEEDED' | 'REJECTED' | 'NOT_OBSERVED'; errandId: string | null; status: string; frozenCents: string; errorCode: string }
export interface GrabResult {requestKey:string;errandId:string;state:"PROCESSING"|"SUCCEEDED"|"REJECTED"|"EXPIRED"|"NOT_OBSERVED";errorCode:string;appliedRound:number;retryAfterMillis:number}
export class ApiError extends Error {
  code: string; status: number; retryAfterMillis: number;
  constructor(code: string, message: string, status = 0, retryAfterMillis = 0) { super(message); this.code = code; this.status = status; this.retryAfterMillis = retryAfterMillis; }
}
const key = 'campusgo_session';
export function validInteger(value: unknown): value is string {
  return typeof value === 'string' && /^-?(0|[1-9][0-9]*)$/.test(value) && BigInt(value) >= -9223372036854775808n && BigInt(value) <= 9223372036854775807n;
}
function invalid(): never { throw new ApiError('INVALID_RESPONSE', '服务返回的数据格式异常，请重试'); }
function sessionValue(value: Session): Session {
  if (!value || !/^[A-Za-z0-9_-]{43}$/.test(value.token) || !validInteger(value.userId) || BigInt(value.userId) <= 0n || !validInteger(value.expiresIn) || BigInt(value.expiresIn) <= 0n) invalid();
  return value;
}
function cardValue(value: Card): Card {
  if (!value || !validInteger(value.id) || !validInteger(value.publisherId) || !validInteger(value.rewardCents) || (value.grabberId !== null && !validInteger(value.grabberId)) || !Array.isArray(value.availableActions)) invalid();
  return value;
}
export function money(cents: string): string {
  if (!validInteger(cents)) invalid();
  const amount = BigInt(cents), positive = amount < 0n ? -amount : amount;
  return `${amount < 0n ? '-' : ''}¥${(positive / 100n).toLocaleString('zh-CN')}.${(positive % 100n).toString().padStart(2, '0')}`;
}
export function createClient(storage: Pick<Storage, 'getItem' | 'setItem' | 'removeItem'>, fetcher: typeof fetch, unauthorized: () => void) {
  let token = storage.getItem(key);
  if (token && !/^[A-Za-z0-9_-]{43}$/.test(token)) { token = null; storage.removeItem(key); }
  function clear() { token = null; storage.removeItem(key); unauthorized(); }
  function acceptSession(value: Session) { const valid = sessionValue(value); storage.setItem(key, valid.token); token = valid.token; }
  async function request<T>(path: string, method = 'GET', body?: unknown, authenticated = true, idempotencyKey?: string, acceptGrabTerminal = false): Promise<T> {
    const sentToken = authenticated ? token : null;
    const headers: Record<string, string> = { 'X-Request-Id': crypto.randomUUID() };
    if (sentToken) headers.Authorization = `Bearer ${sentToken}`;
    if (body !== undefined) headers['Content-Type'] = 'application/json';
    if (idempotencyKey) headers['Idempotency-Key'] = idempotencyKey;
    let response: Response;
    try { response = await fetcher(path, { method, headers, body: body === undefined ? undefined : JSON.stringify(body), cache: 'no-store' }); }
    catch { throw new ApiError('NETWORK', '网络连接失败，请稍后重试', 0, 1000); }
    if (response.status === 401 && sentToken && token === sentToken) clear();
    let result: { code: string; message: string; data: T };
    try { result = await response.json(); } catch { throw new ApiError('INVALID_RESPONSE', '服务暂时不可用，请稍后重试', response.status); }
    if (typeof result?.code !== 'string') invalid();
    if (!response.ok || result.code !== 'OK') {
      if(acceptGrabTerminal && response.status===409){const r=result.data as GrabResult;if(r&&['REJECTED','EXPIRED'].includes(r.state)&&r.errorCode===result.code)return result.data;}
      const seconds = Number(response.headers.get('Retry-After'));
      const retry = response.status === 503 || response.status === 429 ? (Number.isFinite(seconds) && seconds > 0 && seconds <= 60 ? seconds * 1000 : 1000) : 0;
      throw new ApiError(result.code, result.message || '请求失败，请重试', response.status, retry);
    }
    return result.data;
  }
  async function list(path: string) {
    const value = await request<Page>(path);
    if (!value || !Array.isArray(value.items) || typeof value.nextCursor !== 'string') invalid();
    return { items: value.items.map(cardValue), nextCursor: value.nextCursor };
  }
  return {
    request, acceptSession, getToken: () => token,
    config: () => request<AuthConfig>('/api/auth/config', 'GET', undefined, false),
    async me() { const user = await request<User>('/api/users/me'); if (!user || !validInteger(user.id) || !validInteger(user.campusId)) invalid(); return user; },
    async login(userId: string) { const value = sessionValue(await request<Session>('/api/auth/login', 'POST', { userId }, false)); acceptSession(value); return value; },
    async logout() { const sentToken = token; await request('/api/auth/logout', 'POST'); if (token === sentToken) clear(); },
    list: (cursor = '', status = 'PUBLISHED') => list(`/api/errands?cursor=${encodeURIComponent(cursor)}&status=${encodeURIComponent(status)}`),
    mine: (role: string, cursor = '') => list(`/api/errands/mine?cursor=${encodeURIComponent(cursor)}&role=${encodeURIComponent(role)}`),
    async confirm(id:string,expectedRound:number){return cardValue(await request<Card>(`/api/errands/${encodeURIComponent(id)}/confirm`,'POST',{expectedRound}));},
    async detail(id: string) { return cardValue(await request<Card>(`/api/errands/${encodeURIComponent(id)}`)); },
    timeline: (id: string) => request<StatusChange[]>(`/api/errands/${encodeURIComponent(id)}/timeline`),
    async balance() { const b = await request<Balance>('/api/wallet'); if (!b || ![b.availableCents,b.frozenCents,b.version].every(validInteger)) invalid(); return b; },
    async ledger(cursor = '') { const p = await request<LedgerPage>(`/api/wallet/ledger?cursor=${encodeURIComponent(cursor)}`); if (!p || !Array.isArray(p.items) || typeof p.nextCursor !== 'string') invalid(); for (const e of p.items) if (![e.id,e.amountCents,e.balanceAfterCents,e.accountVersion,e.refId].every(validInteger)) invalid(); return p; },
    async escrow(id: string) { const r = await request<Escrow>(`/api/errands/${encodeURIComponent(id)}/escrow`); if (!r || !validInteger(r.errandId) || !validInteger(r.amountCents)) invalid(); return r; },
    async grab(key:string,id:string,expectedRound:number) {return grabValue(await request<GrabResult>(`/api/errands/${encodeURIComponent(id)}/grab`,"POST",{expectedRound},true,key,true),key,id);},
    async grabResult(key:string,id:string) {return grabValue(await request<GrabResult>(`/api/errands/${encodeURIComponent(id)}/grab-requests/${encodeURIComponent(key)}`,"GET",undefined,true,undefined,true),key,id);},
    async publish(key: string, command: PublishCommand) { return publishValue(await request<PublishResult>('/api/errands','POST',command,true,key),key); },
    async publishResult(key: string) { return publishValue(await request<PublishResult>(`/api/publish-requests/${encodeURIComponent(key)}`),key); },
  };
}

function publishValue(r: PublishResult, key: string) {
  if (!r || r.requestKey !== key || !['SUCCEEDED','REJECTED','NOT_OBSERVED'].includes(r.state) || !validInteger(r.frozenCents) || (r.state === 'SUCCEEDED' && (!validInteger(r.errandId) || BigInt(r.errandId) <= 0n || BigInt(r.frozenCents) <= 0n || r.status !== 'PUBLISHED'))) invalid();
  return r;
}

// Conversion rejects extra decimals; it never rounds or passes through floating point.
export function yuanToCents(value: string): string {
  if (!/^(0|[1-9][0-9]*)(\.[0-9]{1,2})?$/.test(value)) throw new Error('金额需为正数，最多两位小数');
  const [whole,fraction = ''] = value.split('.');
  const cents = BigInt(whole) * 100n + BigInt(fraction.padEnd(2,'0'));
  if (cents <= 0n || cents > 9223372036854775807n) throw new Error('金额超出允许范围');
  return cents.toString();
}

function grabValue(r:GrabResult,key:string,id:string){
 if(!r||r.requestKey!==key||r.errandId!==id||!["PROCESSING","SUCCEEDED","REJECTED","EXPIRED","NOT_OBSERVED"].includes(r.state)||!Number.isInteger(r.appliedRound)||r.appliedRound<0||r.appliedRound>2147483647||!Number.isInteger(r.retryAfterMillis)||r.retryAfterMillis<0||r.retryAfterMillis>60000||typeof r.errorCode!=="string"||(["REJECTED","EXPIRED"].includes(r.state)&&!r.errorCode)||(["PROCESSING","SUCCEEDED","NOT_OBSERVED"].includes(r.state)&&r.errorCode!==""))invalid();
 return r;
}
