import {test} from 'node:test';
import assert from 'node:assert/strict';
import {readGrab,saveGrab,clearGrab} from '../src/grabOperation.ts';
import {createClient,ApiError} from '../src/client.ts';
const storage=()=>{const map=new Map();return {getItem:k=>map.get(k)??null,setItem:(k,v)=>map.set(k,v),removeItem:k=>map.delete(k)};};
const key='a8b3c4d5-1234-4567-8901-aabbccddeeff',id='9007199254740993';
const op={key,errandId:id,campusId:'1',expectedRound:0};
test('grab operation survives reload with original round and is scoped to user/task',()=>{
 const s=storage();saveGrab(s,'2001',op);assert.deepEqual(readGrab(s,'2001','1',id),op);assert.equal(readGrab(s,'2002','1',id),null);
 assert.throws(()=>saveGrab(s,'2001',{...op,key:'b8b3c4d5-1234-4567-8901-aabbccddeeff'}));assert.throws(()=>readGrab(s,'2001','2',id));
 clearGrab(s,'2001',op);assert.equal(readGrab(s,'2001','1',id),null);
});
test('client preserves key and integer round on retry, validates response identity',async()=>{
 const s=storage();let calls=0;
 const client=createClient(s,async(url,options)=>{calls++;assert.equal(options.headers['Idempotency-Key'],key);assert.equal(options.body,'{"expectedRound":0}');return new Response(JSON.stringify({code:'OK',data:{requestKey:key,errandId:id,state:'PROCESSING',errorCode:'',appliedRound:0,retryAfterMillis:1000}}),{status:202});},()=>{});
 assert.equal((await client.grab(key,id,0)).state,'PROCESSING');await client.grab(key,id,0);assert.equal(calls,2);
 const bad=createClient(s,async()=>new Response(JSON.stringify({code:'OK',data:{requestKey:key,errandId:'1',state:'SUCCEEDED',errorCode:'',appliedRound:0,retryAfterMillis:0}})),()=>{});
 await assert.rejects(()=>bad.grabResult(key,id),e=>e instanceof ApiError&&e.code==='INVALID_RESPONSE');
});
test('durable HTTP 409 grab rejection is a validated terminal result, key conflicts remain errors',async()=>{
 const data={requestKey:key,errandId:id,state:'REJECTED',errorCode:'STALE_VERSION',appliedRound:1,retryAfterMillis:0};
 const client=createClient(storage(),async()=>new Response(JSON.stringify({code:'STALE_VERSION',data}),{status:409}),()=>{});
 assert.equal((await client.grab(key,id,1)).state,'REJECTED');
 const conflict=createClient(storage(),async()=>new Response(JSON.stringify({code:'IDEMPOTENCY_CONFLICT',data:null}),{status:409}),()=>{});
 await assert.rejects(()=>conflict.grab(key,id,1),e=>e.code==='IDEMPOTENCY_CONFLICT');
});
