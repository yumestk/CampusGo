import {test} from 'node:test';
import assert from 'node:assert/strict';
import {taskGroup,taskGuidance} from '../src/taskPresentation.ts';
const card=(status,availableActions=[])=>({status,availableActions});
test('task groups use authorized next steps, never cancellation or grab eligibility as a todo',()=>{
 for(const action of ['CONFIRM','PICK_UP','DELIVER','SETTLE','ARBITRATE_RUNNER','ARBITRATE_PUBLISHER'])assert.equal(taskGroup(card('ACCEPTED',[action])),'TODO');
 for(const action of ['GRAB','CANCEL','DISPUTE'])assert.equal(taskGroup(card('PUBLISHED',[action])),'ONGOING');
 for(const state of ['SETTLED','REFUNDED','CANCELLED','CLOSED'])assert.equal(taskGroup(card(state,['SETTLE'])),'DONE');
 assert.equal(taskGroup(card('DISPUTED',['ARBITRATE_RUNNER'])),'DISPUTED');
});
test('task guidance explains responsibility and does not equate closed or cancelled status with a confirmed refund',()=>{
 assert.equal(taskGuidance(card('LOCKED')).owner,'接单同学');
 assert.equal(taskGuidance(card('DELIVERED')).owner,'发单同学');
 assert.match(taskGuidance(card('DELIVERED')).next,/确认完成并结算/);
 for(const state of ['CANCELLED','CLOSED'])assert.doesNotMatch(taskGuidance(card(state)).next,/已退款/);
 assert.match(taskGuidance(card('FUTURE_STATE')).next,/当前入口/);
});
