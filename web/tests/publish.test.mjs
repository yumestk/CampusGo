import {test} from 'node:test';
import assert from 'node:assert/strict';
import {createClient,yuanToCents} from '../src/client.ts';
// Node's strip-types loader needs explicit extension for this dependency.
import {readOperation,saveOperation,clearOperation} from '../src/publishOperation.ts';

const command={campusId:'1',type:'DELIVERY',title:'帮取快递',rewardCents:'1234',slotTotal:1};
const op={key:'be0c515f-52db-4260-9150-44f83fcb4f87',command};
function store(){const m=new Map();return {getItem:k=>m.get(k)??null,setItem:(k,v)=>m.set(k,v),removeItem:k=>m.delete(k)};}
test('yuan conversion is exact across int64 bounds and rejects truncation',()=>{
 for(const [yuan,cents] of [['0.01','1'],['12.3','1230'],['92233720368547758.07','9223372036854775807']])assert.equal(yuanToCents(yuan),cents);
 for(const value of ['0','-1','1.001','1e3','NaN','01.00','92233720368547758.08',' 1','1.'])assert.throws(()=>yuanToCents(value));
});
test('pending operation survives reload, is identity-scoped and cannot be silently replaced',()=>{
 const s=store();saveOperation(s,'1001',op);assert.deepEqual(readOperation(s,'1001','1'),op);assert.equal(readOperation(s,'2001','1'),null);
 assert.throws(()=>readOperation(s,'1001','2'));assert.throws(()=>saveOperation(s,'1001',{...op,key:crypto.randomUUID()}));
 assert.throws(()=>clearOperation(s,'1001',{...op,key:crypto.randomUUID()}));assert.deepEqual(readOperation(s,'1001','1'),op);
 clearOperation(s,'1001',op);assert.equal(readOperation(s,'1001','1'),null);
 s.setItem('campusgo_publish:1001','garbled');assert.throws(()=>readOperation(s,'1001','1'));
});
test('network failure and retry keep the exact operation key and command',async()=>{
 const calls=[];const s=store();saveOperation(s,'1001',op);
 const api=createClient(s,async(path,options)=>{calls.push({path,options});if(calls.length===1)throw Error('lost response');return new Response(JSON.stringify({code:'OK',data:{requestKey:op.key,state:'SUCCEEDED',errandId:'9007199254740993',status:'PUBLISHED',frozenCents:'1234',errorCode:''}}));},()=>{});
 api.acceptSession({token:'a'.repeat(43),userId:'1001',expiresIn:'7200'});
 await assert.rejects(api.publish(op.key,op.command));const restored=readOperation(s,'1001','1');const r=await api.publish(restored.key,restored.command);
 assert.equal(r.errandId,'9007199254740993');for(const {options} of calls){assert.equal(options.headers['Idempotency-Key'],op.key);assert.equal(options.body,JSON.stringify(command));}
});
