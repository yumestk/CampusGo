#!/usr/bin/env python3
"""Clean reserved browser fixture IDs in both SQL and Redis; never flush a DB."""
import pathlib
import re
import subprocess

base=['docker','compose','--env-file','.env','-f','deploy/local/compose.yaml','exec','-T']
mysql=base+['mysql','sh','-c','MYSQL_PWD="$MYSQL_PASSWORD" exec mysql --default-character-set=utf8mb4 -N -B -ucampusgo campusgo_verification']
count=subprocess.check_output(mysql,input="SELECT COUNT(*) FROM errand WHERE id BETWEEN 9007199254741301 AND 9007199254741308 AND (publisher_id<>1001 OR title NOT LIKE 'D3页面验证:%');",text=True).strip()
if count!='0':
    raise SystemExit('Reserved D3 fixture IDs contain unrelated tasks; no cleanup performed')
subprocess.run(mysql,input=pathlib.Path('verification/fixtures/d3-cleanup.sql').read_text(),text=True,check=True)
redis=base+['redis','sh','-c','export REDISCLI_AUTH="$CAMPUSGO_REDIS_PASSWORD"; exec redis-cli --raw "$@"','d3-browser-cleanup']
for number in range(1,9):
    task=f'900719925474130{number}'
    for pattern in [f'campusgo:campusgo_verification:grab:v2:{{{task}}}:*',f'campusgo:campusgo_verification:detail:{task}:*',f'campusgo:campusgo_verification:detail:rebuild:{task}']:
        keys=subprocess.check_output(redis+['--scan','--pattern',pattern],text=True).splitlines()
        if any(not re.fullmatch(r'[A-Za-z0-9:{}_-]+',key) for key in keys):
            raise SystemExit('Unexpected fixture key')
        for start in range(0,len(keys),100):
            subprocess.run(redis+['DEL']+keys[start:start+100],check=True,stdout=subprocess.DEVNULL)
print('Cleaned reserved D3 browser SQL fixtures and their Redis keys.')
