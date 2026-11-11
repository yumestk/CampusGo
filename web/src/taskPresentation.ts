import type { Card } from './api';

// Guidance is presentation only. Permissions always come from availableActions.
export function taskGuidance(card: Card): { owner: string; next: string } {
  const states: Record<string, { owner: string; next: string }> = {
    DRAFT: { owner: '发单同学', next: '这份委托尚未发布。' },
    PUBLISHED: { owner: '等待同学接单', next: '任务已开放，抢到后需要确认接单。' },
    LOCKED: { owner: '接单同学', next: '请在确认期限内接单，超时将递补或重新开放。' },
    ACCEPTED: { owner: '接单同学', next: '接单已确认，下一步取货。' },
    PICKED_UP: { owner: '接单同学', next: '正在配送，送达后请确认送达。' },
    DELIVERED: { owner: '发单同学', next: '请核对送达情况，再确认完成并结算；有问题可按权限发起争议。' },
    DISPUTED: { owner: '平台仲裁员', next: '等待平台处理争议，资金结果以托管记录为准。' },
    SETTLED: { owner: '已完成', next: '任务已结算，可查看钱包流水和信用记录。' },
    REFUNDED: { owner: '已结束', next: '赏金已退款，可查看任务托管记录。' },
    CANCELLED: { owner: '已结束', next: '任务已取消，涉及赏金的结果请查看托管记录。' },
    CLOSED: { owner: '已结束', next: '任务已关闭，请查看进度和资金记录。' },
  };
  return states[card.status] ?? { owner: '请查看任务进展', next: '可执行的操作以页面当前入口为准。' };
}

export function taskGroup(card: Card): string {
  if (card.status === 'DISPUTED') return 'DISPUTED';
  if (['SETTLED', 'REFUNDED', 'CANCELLED', 'CLOSED'].includes(card.status)) return 'DONE';
  if (card.availableActions.some(action => ['CONFIRM', 'PICK_UP', 'DELIVER', 'SETTLE', 'ARBITRATE_RUNNER', 'ARBITRATE_PUBLISHER'].includes(action))) return 'TODO';
  return 'ONGOING';
}
