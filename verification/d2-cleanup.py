#!/usr/bin/env python3
"""Undo only recorded D2 browser operations in the isolated verification database."""
import json
import pathlib
import re
import subprocess

manifest = pathlib.Path('work/d2-publish-keys.jsonl')
if not manifest.exists():
    raise SystemExit(0)
operations = set()
for line in manifest.read_text().splitlines():
    row = json.loads(line)
    if row['userId'] not in ['1001', '2001', '2002'] or not re.fullmatch(r'[0-9a-f]{8}(-[0-9a-f]{4}){3}-[0-9a-f]{12}', row['key']):
        raise SystemExit('Invalid D2 cleanup manifest; no cleanup performed')
    operations.add((row['userId'], row['key']))
if not operations:
    raise SystemExit(0)
values = ','.join(f"({user},'{key}')" for user, key in operations)
sql = f"""
CREATE TEMPORARY TABLE d2_owned_request (publisher_id BIGINT,request_key CHAR(36) CHARACTER SET ascii COLLATE ascii_bin,PRIMARY KEY(publisher_id,request_key));
INSERT INTO d2_owned_request VALUES {values};
CREATE TEMPORARY TABLE d2_owned_errand AS SELECT r.errand_id id FROM publish_request r JOIN d2_owned_request o USING(publisher_id,request_key) JOIN errand e ON e.id=r.errand_id WHERE e.publisher_id=r.publisher_id AND e.title LIKE 'D2页面验证:%';
-- Fail before any mutation if another writer's funds are present or a recorded request does not belong to the test.
CREATE TEMPORARY TABLE d2_guard (n BIGINT CHECK(n=0));
INSERT INTO d2_guard SELECT COUNT(*) FROM wallet_ledger l LEFT JOIN d2_owned_errand e ON e.id=l.ref_id WHERE l.ref_type='ESCROW' AND e.id IS NULL;
INSERT INTO d2_guard SELECT COUNT(*) FROM publish_request r JOIN d2_owned_request o USING(publisher_id,request_key) LEFT JOIN d2_owned_errand e ON e.id=r.errand_id WHERE r.state='SUCCEEDED' AND e.id IS NULL;
START TRANSACTION;
DELETE r FROM publish_request r JOIN d2_owned_request o USING(publisher_id,request_key);
DELETE e FROM escrow_order e JOIN d2_owned_errand o ON o.id=e.errand_id;
DELETE l FROM errand_status_log l JOIN d2_owned_errand o ON o.id=l.errand_id;
DELETE e FROM errand e JOIN d2_owned_errand o ON o.id=e.id;
DELETE l FROM wallet_ledger l JOIN d2_owned_errand o ON o.id=l.ref_id WHERE l.ref_type='ESCROW';
UPDATE wallet_account a LEFT JOIN (SELECT account_id,SUM(CASE WHEN direction='CREDIT' THEN CAST(amount AS DECIMAL(65,0)) ELSE -CAST(amount AS DECIMAL(65,0)) END) balance,MAX(account_version) version FROM wallet_ledger GROUP BY account_id) l ON l.account_id=a.id SET a.available=COALESCE(l.balance,0),a.version=COALESCE(l.version,0) WHERE a.id IN (1,1001,2001,2002);
COMMIT;
"""
subprocess.run(['docker','compose','--env-file','.env','-f','deploy/local/compose.yaml','exec','-T','mysql','sh','-c',
    'MYSQL_PWD="$MYSQL_PASSWORD" exec mysql --default-character-set=utf8mb4 -ucampusgo campusgo_verification'], input=sql, text=True, check=True)
manifest.unlink()
print(f'Cleaned {len(operations)} recorded D2 browser operations from verification database.')
