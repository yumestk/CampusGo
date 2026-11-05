#!/usr/bin/env python3
"""Remove only manifest-owned D5 data from the isolated verification database."""
import json,pathlib,re,subprocess
path=pathlib.Path('work/d5-fixtures.jsonl')
if not path.exists(): raise SystemExit(0)
rows=[json.loads(line) for line in path.read_text().splitlines()]
if any(r['kind'] not in ('user','errand') or not re.fullmatch(r'[1-9][0-9]{0,18}',r['id']) for r in rows): raise SystemExit('Invalid D5 manifest')
users=','.join({r['id'] for r in rows if r['kind']=='user'}) or '0'
# Include publisher-owned tasks in case the process died between Publish and manifest append.
sql=f"""
CREATE TEMPORARY TABLE d5_guard(n BIGINT CHECK(n=0));
INSERT INTO d5_guard SELECT COUNT(*) FROM app_user WHERE id IN ({users}) AND display_name<>'D5验证用户';
INSERT INTO d5_guard SELECT COUNT(*) FROM errand WHERE publisher_id IN ({users}) AND title<>'D5验证任务';
CREATE TEMPORARY TABLE d5_tasks AS SELECT id FROM errand WHERE publisher_id IN ({users});
START TRANSACTION;
DELETE FROM fund_event_receipt WHERE request_id IN (SELECT request_id FROM fund_request WHERE errand_id IN (SELECT id FROM d5_tasks));
DELETE FROM fund_request WHERE errand_id IN (SELECT id FROM d5_tasks);
DELETE FROM credit_event WHERE errand_id IN (SELECT id FROM d5_tasks) OR user_id IN ({users});
DELETE FROM grab_repair_task WHERE errand_id IN (SELECT id FROM d5_tasks);
DELETE FROM local_message WHERE errand_id IN (SELECT id FROM d5_tasks);
DELETE FROM grab_record WHERE errand_id IN (SELECT id FROM d5_tasks);
DELETE FROM grab_request WHERE errand_id IN (SELECT id FROM d5_tasks);
DELETE FROM publish_request WHERE publisher_id IN ({users});
DELETE FROM escrow_order WHERE errand_id IN (SELECT id FROM d5_tasks);
DELETE FROM errand_status_log WHERE errand_id IN (SELECT id FROM d5_tasks);
DELETE FROM wallet_ledger WHERE ref_id IN (SELECT id FROM d5_tasks) AND ref_type IN ('ESCROW','SETTLE','REFUND');
DELETE FROM wallet_ledger WHERE user_id IN ({users}) AND ref_type='OPENING';
DELETE FROM errand WHERE id IN (SELECT id FROM d5_tasks);
DELETE FROM wallet_account WHERE owner_id IN ({users}) AND owner_type='USER';
DELETE FROM credit_score WHERE user_id IN ({users});
DELETE FROM app_user WHERE id IN ({users});
INSERT INTO d5_guard SELECT COUNT(*) FROM wallet_ledger WHERE account_id IN (1,2);
UPDATE wallet_account SET available=0,version=0 WHERE id IN (1,2);
COMMIT;
"""
subprocess.run(['docker','compose','--env-file','.env','-f','deploy/local/compose.yaml','exec','-T','mysql','sh','-c','MYSQL_PWD="$MYSQL_PASSWORD" exec mysql --default-character-set=utf8mb4 -ucampusgo campusgo_verification'],input=sql,text=True,check=True)
path.unlink()
print('Cleaned manifest-owned D5 fixtures.')
