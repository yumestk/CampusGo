#!/usr/bin/env python3
"""Remove only manifest-owned D4 fixtures from the isolated verification DB."""
import json
import pathlib
import re
import subprocess

manifest=pathlib.Path('work/d4-fixtures.jsonl')
if not manifest.exists():
    raise SystemExit(0)
rows=[json.loads(line) for line in manifest.read_text().splitlines()]
for row in rows:
    if row['kind'] not in ['user','errand'] or not re.fullmatch(r'[1-9][0-9]{0,18}',row['id']) or not re.fullmatch(r'd4-[0-9a-f-]{36}',row['namespace']):
        raise SystemExit('Invalid D4 fixture manifest; no cleanup performed')
users={r['id'] for r in rows if r['kind']=='user'}
tasks={r['id'] for r in rows if r['kind']=='errand'}
user_ids=','.join(users) or '0'
task_ids=','.join(tasks) or '0'
sql=f"""
CREATE TEMPORARY TABLE d4_guard (n BIGINT CHECK(n=0));
INSERT INTO d4_guard SELECT COUNT(*) FROM app_user WHERE id IN ({user_ids}) AND display_name<>'D4验证用户';
INSERT INTO d4_guard SELECT COUNT(*) FROM errand WHERE id IN ({task_ids}) AND (title<>'D4验证任务' OR publisher_id NOT IN ({user_ids}));
START TRANSACTION;
DELETE FROM credit_event WHERE errand_id IN ({task_ids});
DELETE FROM grab_repair_task WHERE errand_id IN ({task_ids});
DELETE FROM local_message WHERE errand_id IN ({task_ids});
DELETE FROM grab_record WHERE errand_id IN ({task_ids});
DELETE FROM grab_request WHERE errand_id IN ({task_ids});
DELETE FROM errand_status_log WHERE errand_id IN ({task_ids});
DELETE FROM errand WHERE id IN ({task_ids});
DELETE FROM credit_score WHERE user_id IN ({user_ids});
DELETE FROM app_user WHERE id IN ({user_ids});
COMMIT;
"""
subprocess.run(['docker','compose','--env-file','.env','-f','deploy/local/compose.yaml','exec','-T','mysql','sh','-c','MYSQL_PWD="$MYSQL_PASSWORD" exec mysql --default-character-set=utf8mb4 -ucampusgo campusgo_verification'],input=sql,text=True,check=True)
for namespace in {r['namespace'] for r in rows}:
    command=['docker','compose','--env-file','.env','-f','deploy/local/compose.yaml','exec','-T','redis','sh','-c','export REDISCLI_AUTH="$CAMPUSGO_REDIS_PASSWORD"; exec redis-cli --raw "$@"','d4-cleanup']
    keys=subprocess.check_output(command+['--scan','--pattern',f'campusgo:{namespace}:*'],text=True).splitlines()
    for key in keys:
        if not key.startswith(f'campusgo:{namespace}:') or not re.fullmatch(r'[A-Za-z0-9:{}_-]+',key):
            raise SystemExit('Unexpected Redis key in D4 namespace')
    for start in range(0,len(keys),100):
        subprocess.run(command+['DEL']+keys[start:start+100],check=True,stdout=subprocess.DEVNULL)
manifest.unlink()
print(f'Cleaned manifest-owned D4 fixtures: {len(tasks)} tasks, {len(users)} users.')
