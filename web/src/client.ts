export interface User { id: string; campusId: string; name: string; role: string }
export interface Session { token: string; userId: string; expiresIn: string }
export interface Card { id: string; campusId: string; publisherId: string; grabberId: string | null; title: string; type: string; status: string; rewardCents: string; slotTaken: number; slotTotal: number; round: number; role: string; availableActions: string[] }
export interface Page { items: Card[]; nextCursor: string }
export interface StatusChange { time: string; from: string; to: string; round: number; operatorId: string }
export interface AuthConfig { demoLoginEnabled: boolean; oidcEnabled: boolean; oidcCallbackOrigin: string }
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
  async function request<T>(path: string, method = 'GET', body?: unknown, authenticated = true): Promise<T> {
    const sentToken = authenticated ? token : null;
    const headers: Record<string, string> = { 'X-Request-Id': crypto.randomUUID() };
    if (sentToken) headers.Authorization = `Bearer ${sentToken}`;
    if (body !== undefined) headers['Content-Type'] = 'application/json';
    let response: Response;
    try { response = await fetcher(path, { method, headers, body: body === undefined ? undefined : JSON.stringify(body), cache: 'no-store' }); }
    catch { throw new ApiError('NETWORK', '网络连接失败，请稍后重试', 0, 1000); }
    if (response.status === 401 && sentToken && token === sentToken) clear();
    let result: { code: string; message: string; data: T };
    try { result = await response.json(); } catch { throw new ApiError('INVALID_RESPONSE', '服务暂时不可用，请稍后重试', response.status); }
    if (typeof result?.code !== 'string') invalid();
    if (!response.ok || result.code !== 'OK') {
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
    async detail(id: string) { return cardValue(await request<Card>(`/api/errands/${encodeURIComponent(id)}`)); },
    timeline: (id: string) => request<StatusChange[]>(`/api/errands/${encodeURIComponent(id)}/timeline`),
  };
}
