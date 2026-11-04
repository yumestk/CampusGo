import { createClient } from './client';
export * from './client';
export const api = createClient(localStorage, window.fetch.bind(window), () => window.dispatchEvent(new Event('campusgo:logout')));
export const STATUS_TEXT: Record<string, string> = { DRAFT: '草稿', PUBLISHED: '待抢单', LOCKED: '待确认', ACCEPTED: '待取货', PICKED_UP: '配送中', DELIVERED: '待确认完成', SETTLED: '已结算', CLOSED: '已关闭', CANCELLED: '已取消', DISPUTED: '争议中', REFUNDED: '已退款' };
export const TYPE_TEXT: Record<string, string> = { DELIVERY: '跑腿配送', BUY: '帮我买', QUEUE: '代排队', OTHER: '其他帮忙' };
export const TYPE_ICON: Record<string, string> = { DELIVERY: 'package', BUY: 'bag', QUEUE: 'clock', OTHER: 'grid' };
export const IDENTITIES = [{ id: '1001', name: '发单同学', icon: 'package' }, { id: '2001', name: '跑腿同学 A', icon: 'bag' }, { id: '2002', name: '跑腿同学 B', icon: 'users' }, { id: '9001', name: '平台仲裁员', icon: 'shield' }];
export function userName(id: string) { return IDENTITIES.find(user => user.id === id)?.name ?? `同学 #${id}`; }
