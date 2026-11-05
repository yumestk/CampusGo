#!/usr/bin/env python3
"""Guard and restore only recorded D5 browser fixtures in the verification DB."""
import json,pathlib,re,subprocess,sys
snapshot=pathlib.Path('work/d5-browser-snapshot.json');manifest=pathlib.Path('work/d5-browser-keys.jsonl')
mysql=['docker','compose','--env-file','.env','-f','deploy/local/compose.yaml','exec','-T','mysql','sh','-c','MYSQL_PWD="$MYSQL_PASSWORD" exec mysql --default-character-set=utf8mb4 -N -B -ucampusgo campusgo_verification']
def query(sql):return subprocess.check_output(mysql,input=sql,text=True).strip()
if len(sys.argv)>1 and sys.argv[1]=='capture':
 if snapshot.exists() or manifest.exists():raise SystemExit('Clean prior D5 browser fixtures first')
 if query("SELECT COUNT(*) FROM wallet_ledger WHERE ref_type<>'OPENING';")!='0':raise SystemExit('Requires isolated DB with no transfers')
 data={'wallet':{},'credit':{}}
 for account in [1,2,1001,2001,2002,9001]:
  data['wallet'][str(account)]=[int(v) for v in query(f'SELECT available,version FROM wallet_account WHERE id={account};').split()]
 for user in [1001,2001,2002,9001]:
  row=query(f'SELECT score,version FROM credit_score WHERE user_id={user};');data['credit'][str(user)]=[int(v) for v in row.split()] if row else None
 snapshot.write_text(json.dumps(data));raise SystemExit(0)
if not snapshot.exists():
 if manifest.exists():raise SystemExit('Missing snapshot; refusing cleanup')
 raise SystemExit(0)
data=json.loads(snapshot.read_text());operations=set()
if manifest.exists():
 for line in manifest.read_text().splitlines():
  row=json.loads(line)
  if row['userId']!='1001' or not re.fullmatch(r'[0-9a-f]{8}(-[0-9a-f]{4}){3}-[0-9a-f]{12}',row['key']):raise SystemExit('Invalid D5 browser manifest')
  operations.add((row['userId'],row['key']))
values=','.join(f"({user},'{key}')" for user,key in operations)
commands=["CREATE TEMPORARY TABLE d5_browser_keys(publisher_id BIGINT,request_key CHAR(36) CHARACTER SET ascii COLLATE ascii_bin);"]
if values:commands.append('INSERT INTO d5_browser_keys VALUES '+values+';')
commands+=['CREATE TEMPORARY TABLE d5_browser_tasks AS SELECT r.errand_id id FROM publish_request r JOIN d5_browser_keys k USING(publisher_id,request_key) JOIN errand t ON t.id=r.errand_id WHERE t.publisher_id=1001 AND t.title LIKE \'D5页面验证:%\';','CREATE TEMPORARY TABLE d5_browser_guard(n BIGINT CHECK(n=0));',"INSERT INTO d5_browser_guard SELECT COUNT(*) FROM wallet_ledger l WHERE l.ref_type<>'OPENING' AND l.ref_id NOT IN (SELECT id FROM d5_browser_tasks);",'START TRANSACTION;']
for account,row in data['wallet'].items():
 if not account.isdigit() or len(row)!=2 or any(not isinstance(v,int) or v<0 for v in row):raise SystemExit('Invalid wallet snapshot')
 balance,version=row
 commands.append(f"INSERT INTO d5_browser_guard SELECT IF(a.available={balance}+COALESCE(l.net,0) AND a.version={version}+COALESCE(l.n,0),0,1) FROM wallet_account a LEFT JOIN (SELECT SUM(CASE WHEN direction='CREDIT' THEN CAST(amount AS DECIMAL(65,0)) ELSE -CAST(amount AS DECIMAL(65,0)) END) net,COUNT(*) n FROM wallet_ledger WHERE account_id={account} AND ref_id IN (SELECT id FROM d5_browser_tasks) AND ref_type<>'OPENING') l ON TRUE WHERE a.id={account} FOR UPDATE;")
 commands.append(f'UPDATE wallet_account SET available={balance},version={version} WHERE id={account};')
for user,row in data['credit'].items():
 if user not in ['1001','2001','2002','9001']:raise SystemExit('Invalid credit user')
 deltas=query(f'SELECT delta FROM credit_event WHERE user_id={user} AND errand_id IN (SELECT id FROM errand WHERE publisher_id=1001 AND title LIKE \'D5页面验证:%\') ORDER BY id;')
 score,version=row if row else [60,0];current=score;effects=0
 for d in deltas.split():current=max(0,min(100,current+int(d)));effects+=1
 if row or effects:commands.append(f'INSERT INTO d5_browser_guard SELECT IF(COUNT(*)=1,0,1) FROM credit_score WHERE user_id={user} AND score={current} AND version={version+effects} FOR UPDATE;')
 else:commands.append(f'INSERT INTO d5_browser_guard SELECT COUNT(*) FROM credit_score WHERE user_id={user} FOR UPDATE;')
 commands.append(f'UPDATE credit_score SET score={score},version={version} WHERE user_id={user};' if row else f'DELETE FROM credit_score WHERE user_id={user};')
commands += [
'DELETE FROM notification WHERE errand_id IN (SELECT id FROM d5_browser_tasks);',
'DELETE FROM fund_event_receipt WHERE request_id IN (SELECT request_id FROM fund_request WHERE errand_id IN (SELECT id FROM d5_browser_tasks));',
'DELETE FROM fund_request WHERE errand_id IN (SELECT id FROM d5_browser_tasks);',
'DELETE FROM credit_event WHERE errand_id IN (SELECT id FROM d5_browser_tasks);',
'DELETE FROM grab_repair_task WHERE errand_id IN (SELECT id FROM d5_browser_tasks);',
'DELETE FROM local_message WHERE errand_id IN (SELECT id FROM d5_browser_tasks);',
'DELETE FROM grab_record WHERE errand_id IN (SELECT id FROM d5_browser_tasks);',
'DELETE FROM grab_request WHERE errand_id IN (SELECT id FROM d5_browser_tasks);',
'DELETE r FROM publish_request r JOIN d5_browser_keys k USING(publisher_id,request_key);',
'DELETE FROM escrow_order WHERE errand_id IN (SELECT id FROM d5_browser_tasks);',
'DELETE FROM errand_status_log WHERE errand_id IN (SELECT id FROM d5_browser_tasks);',
"DELETE FROM wallet_ledger WHERE ref_id IN (SELECT id FROM d5_browser_tasks) AND ref_type<>'OPENING';",
'DELETE FROM errand WHERE id IN (SELECT id FROM d5_browser_tasks);','COMMIT;']
query('\n'.join(commands));snapshot.unlink()
if manifest.exists():manifest.unlink()
print('Restored guarded D5 browser wallets/credit and removed recorded fixtures atomically.')
