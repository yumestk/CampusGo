import {validInteger} from './client.ts';
export interface GrabOperation {key:string;errandId:string;campusId:string;expectedRound:number}
type Store=Pick<Storage,'getItem'|'setItem'|'removeItem'>;
const name=(userId:string,id:string)=>`campusgo_grab:${userId}:${id}`;
export function readGrab(storage:Store,userId:string,campusId:string,id:string):GrabOperation|null {
 const raw=storage.getItem(name(userId,id));if(raw===null)return null;
 let op:GrabOperation;
 try{op=JSON.parse(raw);}catch{throw Error('本机抢单记录损坏，请保留记录并核对结果');}
 if(!op||!/^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/.test(op.key)||op.key==='00000000-0000-0000-0000-000000000000'||op.errandId!==id||!validInteger(op.errandId)||BigInt(op.errandId)<=0n||op.campusId!==campusId||!Number.isInteger(op.expectedRound)||op.expectedRound<0||op.expectedRound>2147483647)throw Error('本机抢单记录不匹配，请保留记录并核对结果');
 return op;
}
export function saveGrab(storage:Store,userId:string,op:GrabOperation){
 const old=readGrab(storage,userId,op.campusId,op.errandId);
 if(old&&JSON.stringify(old)!==JSON.stringify(op))throw Error('另一窗口已有抢单请求，请先恢复原请求');
 storage.setItem(name(userId,op.errandId),JSON.stringify(op));
}
export function clearGrab(storage:Store,userId:string,op:GrabOperation){
 const old=readGrab(storage,userId,op.campusId,op.errandId);
 if(old&&old.key!==op.key)throw Error('另一窗口已有新的抢单，请刷新页面');
 storage.removeItem(name(userId,op.errandId));
}
