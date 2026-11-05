#!/usr/bin/env python3
"""Restore only D4 browser credit and fixture rows, together in one transaction."""
import json
import pathlib
import subprocess
import sys
snapshot=pathlib.Path('work/d4-browser-credit.json')
mysql=['docker','compose','--env-file','.env','-f','deploy/local/compose.yaml','exec','-T','mysql','sh','-c','MYSQL_PWD="$MYSQL_PASSWORD" exec mysql --default-character-set=utf8mb4 -N -B -ucampusgo campusgo_verification']
def query(sql):
    return subprocess.check_output(mysql,input=sql,text=True).strip()
if sys.argv[1]=='capture':
    if snapshot.exists():
        raise SystemExit('Restore prior D4 fixtures before capturing credit')
    data={}
    for user in [2001,2002]:
        row=query(f'SELECT score,version FROM credit_score WHERE user_id={user};')
        data[str(user)]=[int(n) for n in row.split()] if row else None
    snapshot.write_text(json.dumps(data))
else:
    cleanup=pathlib.Path('verification/fixtures/d4-cleanup.sql').read_text()
    commands=['CREATE TEMPORARY TABLE d4_credit_guard(n INT CHECK(n=0));']
    if snapshot.exists():
        data=json.loads(snapshot.read_text())
        if set(data)!={'2001','2002'}:
            raise SystemExit('Unexpected credit snapshot users')
        for user,row in data.items():
            effects=int(query(f"SELECT COUNT(*) FROM credit_event c JOIN errand e ON e.id=c.errand_id WHERE c.user_id={user} AND e.id BETWEEN 9007199254741401 AND 9007199254741416 AND e.publisher_id=1001 AND e.title LIKE 'D4页面验证:%';"))
            old_score,old_version=row if row else [60,0]
            if not (0<=old_score<=100 and old_version>=0):
                raise SystemExit('Invalid credit snapshot')
            expected_score=max(0,old_score-5*effects)
            expected_version=old_version+effects
            if effects or row:
                commands.append(f'INSERT INTO d4_credit_guard SELECT IF(COUNT(*)=1,0,1) FROM credit_score WHERE user_id={user} AND score={expected_score} AND version={expected_version} FOR UPDATE;')
            else:
                commands.append(f'INSERT INTO d4_credit_guard SELECT COUNT(*) FROM credit_score WHERE user_id={user} FOR UPDATE;')
            if row:
                commands.append(f'UPDATE credit_score SET score={old_score},version={old_version} WHERE user_id={user};')
            else:
                commands.append(f'DELETE FROM credit_score WHERE user_id={user};')
    else:
        commands.append('INSERT INTO d4_credit_guard SELECT COUNT(*) FROM credit_event c JOIN d4_browser_owned o ON o.id=c.errand_id;')
    # Credit restoration and deletion of the corresponding test events/tasks are
    # atomic. Unexpected concurrent score changes fail before any deletion.
    cleanup=cleanup.replace('START TRANSACTION;','START TRANSACTION;\n'+'\n'.join(commands))
    query(cleanup)
    if snapshot.exists():
        snapshot.unlink()
