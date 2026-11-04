import type { PublishCommand } from './client.ts';
import { validInteger } from './client.ts';

export interface Operation { key: string; command: PublishCommand }
const name = (userId: string) => `campusgo_publish:${userId}`;
type Store = Pick<Storage,'getItem' | 'setItem' | 'removeItem'>;
export function readOperation(storage: Store,userId: string,campusId: string): Operation | null {
  const raw = storage.getItem(name(userId)); if (raw === null) return null;
  let op: Operation;
  try { op = JSON.parse(raw); } catch { throw new Error('本机发布记录损坏，请保留记录并核对发布结果'); }
  const c = op?.command;
  if (!op || !/^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/.test(op.key) || !c || c.campusId !== campusId || !['DELIVERY','BUY','QUEUE','OTHER'].includes(c.type) || typeof c.title !== 'string' || c.title.length === 0 || [...c.title].length > 64 || !validInteger(c.rewardCents) || BigInt(c.rewardCents) <= 0n || c.slotTotal !== 1) throw new Error('本机发布记录不匹配，请保留记录并核对发布结果');
  return op;
}
export function saveOperation(storage: Store,userId: string,op: Operation) {
  const old = readOperation(storage,userId,op.command.campusId);
  if (old && JSON.stringify(old) !== JSON.stringify(op)) throw new Error('已有待确认的发布，请先恢复原请求');
  storage.setItem(name(userId),JSON.stringify(op));
}
export function clearOperation(storage: Store,userId: string,op: Operation) {
  const old = readOperation(storage,userId,op.command.campusId);
  if (old && old.key !== op.key) throw new Error('另一窗口已有新的发布，请刷新页面');
  storage.removeItem(name(userId));
}
