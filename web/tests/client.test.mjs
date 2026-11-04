import { test } from 'node:test';
import assert from 'node:assert/strict';
import { createClient, money, validInteger } from '../src/client.ts';

function client(fetcher) {
  const values = new Map(); let invalidated = 0;
  const storage = { getItem: k => values.get(k) ?? null, setItem: (k,v) => values.set(k,v), removeItem: k => values.delete(k) };
  const api = createClient(storage, fetcher, () => invalidated++);
  api.acceptSession({token: 'a'.repeat(43), userId: '1001', expiresIn: '7200'});
  return { api, values, invalidated: () => invalidated };
}
const response = (status, code, data = null) => new Response(JSON.stringify({code,message:code,data}), {status,headers:{'Retry-After':'2'}});

test('int64 IDs and cents retain precision; numeric wire values are rejected', async () => {
  assert.equal(money('9223372036854775807'), '¥92,233,720,368,547,758.07');
  assert.equal(money('-1'), '-¥0.01');
  for (const value of [9007199254740993, '9223372036854775808', '1e3', '1.5', null]) assert.equal(validInteger(value),false);
  const {api} = client(async () => response(200,'OK',{id:9007199254740993,campusId:'1'}));
  await assert.rejects(api.me(), e => e.code === 'INVALID_RESPONSE');
});
test('503 and network failures retain credentials and expose retry semantics', async () => {
  const {api,invalidated} = client(async () => response(503,'DEPENDENCY_UNAVAILABLE'));
  await assert.rejects(api.me(), e => e.status === 503 && e.retryAfterMillis === 2000);
  assert.equal(api.getToken(),'a'.repeat(43)); assert.equal(invalidated(),0);
  await assert.rejects(api.logout(), e => e.status === 503);
  assert.equal(api.getToken(),'a'.repeat(43));
  const offline = client(async () => { throw new TypeError('offline'); });
  await assert.rejects(offline.api.me(), e => e.code === 'NETWORK'); assert.ok(offline.api.getToken());
});
test('a late 401 from an older credential cannot log out a new session', async () => {
  let resolve; const pending = new Promise(r => { resolve = r; });
  const {api,invalidated} = client(() => pending);
  const request = api.me();
  api.acceptSession({token:'b'.repeat(43),userId:'2001',expiresIn:'7200'});
  resolve(response(401,'UNAUTHORIZED'));
  await assert.rejects(request,e => e.status === 401);
  assert.equal(api.getToken(),'b'.repeat(43)); assert.equal(invalidated(),0);
});
test('current 401 clears session; failed unauthenticated login does not', async () => {
  const {api,invalidated} = client(async () => response(401,'UNAUTHORIZED'));
  await assert.rejects(api.login('3000')); assert.ok(api.getToken());
  await assert.rejects(api.me()); assert.equal(api.getToken(),null); assert.equal(invalidated(),1);
});
test('late successful logout cannot clear a newer login', async () => {
  let resolve; const pending = new Promise(r => {resolve = r;});
  const {api} = client(() => pending); const logout = api.logout();
  api.acceptSession({token:'b'.repeat(43),userId:'2001',expiresIn:'7200'});
  resolve(response(200,'OK')); await logout; assert.equal(api.getToken(),'b'.repeat(43));
});
