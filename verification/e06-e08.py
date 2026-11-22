#!/usr/bin/env python3
"""Real cmd/api + cmd/worker experiments; isolated DB and a separate APISIX."""
import concurrent.futures as cf
import re
import collections,hashlib,base64,json,os,pathlib,socket,subprocess,threading,time,urllib.request,urllib.error,uuid,sys
ROOT=pathlib.Path(__file__).resolve().parent.parent
os.chdir(ROOT)
E=sys.argv[1] if len(sys.argv)>1 else ''
if E not in ('e06','e07','e08','e10','e11'):raise SystemExit('usage: with-local-env.sh python3 verification/e06-e08.py e06|e07|e08|e10|e11')
OUT=ROOT/'work/experiments'/E;OUT.mkdir(parents=True,exist_ok=True)
LOCK=ROOT/'work/experiments/.running'
try:LOCK.mkdir()
except FileExistsError:raise SystemExit('Another experiment/unfinished lock exists; inspect first')
compose=['docker','compose','--env-file','.env','-f','deploy/local/compose.yaml']
mysql=compose+['exec','-T','mysql','sh','-c','MYSQL_PWD="$MYSQL_PASSWORD" exec mysql --unbuffered --default-character-set=utf8mb4 -N -B -ucampusgo campusgo_verification']
def sql(q):return subprocess.check_output(mysql,input=q,text=True).strip()
def log(**v):
 print(json.dumps(v,ensure_ascii=False),flush=True)
 with (OUT/'evidence.jsonl').open('a') as f:f.write(json.dumps(v,ensure_ascii=False)+'\n')
def require(ok,msg):
 if not ok:raise AssertionError(msg)
def wait(fn,seconds=45):
 end=time.monotonic()+seconds
 while time.monotonic()<end:
  if fn():return
  time.sleep(.25)
 raise TimeoutError('condition not reached')
def req(path,token=None,body=None,key=None):
 headers={'Content-Type':'application/json'}
 if token:headers['Authorization']='Bearer '+token
 if key:headers['Idempotency-Key']=key
 r=urllib.request.Request('http://127.0.0.1:19126'+path,data=json.dumps(body).encode() if body is not None else None,headers=headers)
 try:
  try:response=urllib.request.urlopen(r,timeout=12)
  except urllib.error.HTTPError as e:response=e
  with response:
   raw=response.read()
   try:data=json.loads(raw)
   except ValueError:data={'code':'NON_JSON'}
   return response.status,data
 except (OSError,TimeoutError):return 0,{'code':'CONNECTION_LOST'}
def ok(path,token=None,body=None,key=None):
 status,data=req(path,token,body,key);require(status==200 and data.get('code')=='OK',str((path,status,data)))
 return data['data']
class Proxy:
 def __init__(self):
  self.sock=socket.socket();self.sock.bind(('127.0.0.1',0));self.sock.listen();self.port=self.sock.getsockname()[1]
  self.receiptCommits=collections.Counter();self.receipts=collections.Counter();self.blockedReceipt=None
  self.arm=None;self.hit=threading.Event();self.release=threading.Event();self.count=0;self.connections=[];self.mu=threading.Lock()
  threading.Thread(target=self.accept,daemon=True).start()
 def accept(self):
  while True:
   try:a,_=self.sock.accept();b=socket.create_connection(tuple(os.getenv('CAMPUSGO_MYSQL_ADDR','127.0.0.1:13306').rsplit(':',1)[i] if i==0 else int(os.getenv('CAMPUSGO_MYSQL_ADDR','127.0.0.1:13306').rsplit(':',1)[i]) for i in range(2)))
   except OSError:return
   self.connections.extend([a,b]);state={'match':False,'commit':False,'receiptPrepare':False,'receiptIDs':set(),'receiptKey':None,'isCommit':False}
   for src,dst,client in [(a,b,True),(b,a,False)]:threading.Thread(target=self.pump,args=(src,dst,client,state),daemon=True).start()
 def pump(self,src,dst,client,state):
  def read(n):
   data=b''
   while len(data)<n:
    part=src.recv(n-len(data))
    if not part:raise EOFError()
    data+=part
   return data
  try:
   while True:
    h=read(4);p=read(int.from_bytes(h[:3],'little'))
    if client and p and p[0] in (3,22):
     q=p[1:].decode(errors='replace').lower().strip()
     if 'insert into fund_event_receipt' in q:state['receiptPrepare']=True
     if self.arm and self.arm in q:state['match']=True
     if q=='rollback':state['match']=False;state['receiptKey']=None
     if q=='commit':state['isCommit']=True
     if q=='commit':state['commit']=state['match'];state['match']=False
    if not client and state['receiptPrepare'] and p and p[0]==0 and len(p)>=5:
     state['receiptIDs'].add(p[1:5]);state['receiptPrepare']=False
    if client and p and p[0]==23 and p[1:5] in state['receiptIDs']:
     match=re.search(rb'[0-9a-f]{8}(?:-[0-9a-f]{4}){3}-[0-9a-f]{12}',p)
     if match:
      state['receiptKey']=match.group().decode()
      with self.mu:self.count+=1;self.receipts[state['receiptKey']]+=1
    wasCommit=not client and state['isCommit']
    if wasCommit:
     state['isCommit']=False
     if p and p[0]==0 and state['receiptKey']:
      with self.mu:self.receiptCommits[state['receiptKey']]+=1
    if not client and state['commit']:
     state['commit']=False
     if p and p[0]==0:
      with self.mu:
       block=self.arm is not None
       if block:self.arm=None;self.blockedReceipt=state['receiptKey'];self.hit.set()
      if block:self.release.wait(30)
    dst.sendall(h+p)
    if wasCommit:state['receiptKey']=None
  except (OSError,EOFError):pass
  finally:
   for s in (src,dst):
    try:s.shutdown(socket.SHUT_RDWR);s.close()
    except OSError:pass
 def close(self):
  self.release.set();self.sock.close()
  for s in self.connections:
   try:s.close()
   except OSError:pass
processes=[];proxy=Proxy();gateway='campusgo-'+E+'-gateway';tokens=[];users=[];tasks=[];owns=False
env={**os.environ,'CAMPUSGO_ENV':'test','CAMPUSGO_DATABASE':'campusgo_verification','CAMPUSGO_SESSION_NAMESPACE':E+'-'+uuid.uuid4().hex[:12],'CAMPUSGO_AUTH_MODE':'session','CAMPUSGO_DEMO_LOGIN':'false','CAMPUSGO_OIDC_ISSUER':'','CAMPUSGO_OIDC_CALLBACK_URL':'','CAMPUSGO_REQUIRE_GATEWAY':'false' if E=='e11' else 'true','CAMPUSGO_API_ADDR':'127.0.0.1:18126','CAMPUSGO_WORKER_ADDR':'127.0.0.1:18127','CAMPUSGO_ID_WORKER':'1012','CAMPUSGO_WORKER_ID_WORKER':'1013'}
def start(kind,proxied=False):
 f=(OUT/(kind+'-'+str(len(processes))+'.log')).open('w')
 p=subprocess.Popen(['./bin/'+kind],env={**env,**({'CAMPUSGO_MYSQL_ADDR':'127.0.0.1:'+str(proxy.port)} if proxied else {})},stdout=f,stderr=subprocess.STDOUT)
 processes.append(p)
 def ready():
  require(p.poll() is None,kind+' exited; inspect log')
  try:return urllib.request.urlopen('http://127.0.0.1:'+('18126' if kind=='api' else '18127')+'/livez',timeout=1).status==200
  except OSError:return False
 wait(ready);return p
uid=int(time.time()*1000000)
def user():
 global uid
 uid+=1;u=uid
 with pathlib.Path('work/d5-fixtures.jsonl').open('a') as f:f.write(json.dumps({'kind':'user','id':str(u)})+'\n')
 sql(f"START TRANSACTION;INSERT INTO app_user(id,campus_id,display_name,role) VALUES({u},1,'D5验证用户','STUDENT');INSERT INTO wallet_account(id,owner_id,owner_type,available,version) VALUES({u},{u},'USER',1000000,1);INSERT INTO wallet_ledger(id,biz_no,account_id,user_id,direction,amount,balance_after,account_version,ref_type,ref_id) VALUES({u},'opening:{u}',{u},{u},'CREDIT',1000000,1000000,1,'OPENING',{u});COMMIT;")
 t=base64.urlsafe_b64encode(os.urandom(32)).decode().rstrip('=')
 key='campusgo:auth:test:live:'+env['CAMPUSGO_SESSION_NAMESPACE']+':session:'+hashlib.sha256(t.encode()).hexdigest()
 command=compose+['exec','-T','redis','sh','-c','export REDISCLI_AUTH="$CAMPUSGO_REDIS_PASSWORD";exec redis-cli --raw "$@"','fixture-session']
 subprocess.run(command+['SET',key,str(u),'EX','3600'],check=True,stdout=subprocess.DEVNULL)
 users.append(u);tokens.append(t);return u,t
def publish(t,key=None):return ok('/api/errands',t,{'campusId':'1','type':'DELIVERY','title':'D5验证任务','rewardCents':'125','slotTotal':1},key or str(uuid.uuid4()))['errandId']
def grab(t,task,key=None):return req('/api/errands/'+str(task)+'/grab',t,{'expectedRound':0},key or str(uuid.uuid4()))
def reconcile(label):
 for db in ['campusgo','campusgo_verification']:
  r=subprocess.check_output(['./bin/reconcile','-database',db],env=os.environ,text=True);(OUT/(label+'-'+db+'.json')).write_text(r)
try:
 require(not pathlib.Path('work/d5-fixtures.jsonl').exists(),'Unfinished D5 fixtures; clean first')
 require(sql("SELECT COUNT(*) FROM wallet_ledger WHERE ref_type<>'OPENING'")=='0','Verification DB has transfers')
 owns=True
 reconcile('before');(OUT/'evidence.jsonl').write_text('')
 cfg=(ROOT/'deploy/local/apisix/apisix.yaml').read_text().replace('host.docker.internal:18080','host.docker.internal:18126').replace('campusgo-entry: {}',f'campusgo-entry: {{namespace: {E}, ip_rate: 10000, ip_burst: 20000, auth_rate: 1000, auth_burst: 2000}}')
 (OUT/'apisix.yaml').write_text(cfg)
 cid=subprocess.check_output(compose+['ps','-q','mysql'],text=True).strip()
 network=next(iter(json.loads(subprocess.check_output(['docker','inspect',cid],text=True))[0]['NetworkSettings']['Networks']))
 subprocess.run(['docker','run','-d','--rm','--name',gateway,'--network',network,'--add-host','host.docker.internal:host-gateway','-p','127.0.0.1:19126:9080','-e','CAMPUSGO_REDIS_PASSWORD','-e','CAMPUSGO_GATEWAY_SECRET','-v',str(OUT/'apisix.yaml')+':/usr/local/apisix/conf/apisix.yaml:ro','-v',str(ROOT/'deploy/local/apisix/config.yaml')+':/usr/local/apisix/conf/config.yaml:ro','-v',str(ROOT/'deploy/local/apisix/campusgo-entry.lua')+':/usr/local/apisix/apisix/plugins/campusgo-entry.lua:ro','apache/apisix:3.19.0-debian@sha256:9a7e45dc943fbf10ec916d1232bae4cda9302ae28a4696cfb83c2da01d261141'],check=True,stdout=subprocess.DEVNULL)
 api=start('api',E=='e06');wait(lambda:req('/api/auth/config')[0]==200)
 owner,ot=user()
 if E=='e06':
  for mode in ['publish','grab']:
   if mode=='grab':task=publish(ot);runner,rt=user();token=rt;marker='insert into grab_record'
   else:token=ot;marker='insert into publish_request'
   balanceBefore=int(sql(f'SELECT available FROM wallet_account WHERE id={owner}'))
   key=str(uuid.uuid4());proxy.hit.clear();proxy.release.clear();proxy.arm=marker
   with cf.ThreadPoolExecutor(1) as pool:
    future=pool.submit(req,'/api/errands' if mode=='publish' else f'/api/errands/{task}/grab',token,{'campusId':'1','title':'D5验证任务','rewardCents':'125','slotTotal':1} if mode=='publish' else {'expectedRound':0},key)
    require(proxy.hit.wait(10),'COMMIT checkpoint not reached')
    if mode=='publish':task=sql(f"SELECT errand_id FROM publish_request WHERE publisher_id={owner} AND request_key='{key}'")
    require(sql(f'SELECT status FROM errand WHERE id={task}')==('PUBLISHED' if mode=='publish' else 'LOCKED'),'durable state missing')
    api.kill();api.wait();proxy.release.set();lost=future.result()
   api=start('api',True)
   result=ok(f'/api/publish-requests/{key}' if mode=='publish' else f'/api/errands/{task}/grab-requests/{key}',token)
   require(result['state']=='SUCCEEDED','same key result not recovered')
   if mode=='publish':replay=ok('/api/errands',ot,{'campusId':'1','title':'D5验证任务','rewardCents':'125','slotTotal':1},key);require(str(replay['errandId'])==str(task),'duplicate task')
   else:
    time.sleep(2.1);require(grab(token,task,key)[1]['data']['state']=='SUCCEEDED','grab replay failed')
    worker=start('worker');wait(lambda:sql(f"SELECT COUNT(*) FROM grab_repair_task WHERE errand_id={task} AND state<>'DONE'")=='0');worker.terminate();worker.wait(timeout=25)
   balanceAfter=int(sql(f'SELECT available FROM wallet_account WHERE id={owner}'));require(balanceBefore-balanceAfter==(125 if mode=='publish' else 0),'unexpected balance effect')
   log(experiment=E,balanceBefore=balanceBefore,balanceAfter=balanceAfter,mode=mode,key=key,task=task,lostStatus=lost[0],lostCode=lost[1].get('code'),recovered=result['state'],records=sql(f'SELECT COUNT(*) FROM grab_record WHERE errand_id={task}'))
 elif E=='e07':
  runner,rt=user();worker=start('worker',True);worker.kill();worker.wait()
  keys=[];ids=[]
  for i in range(3):
   task=publish(ot);require(grab(rt,task)[0]==200,'grab preparation');ok(f'/api/errands/{task}/confirm',rt,{'expectedRound':0})
   for action in ['PICK_UP','DELIVER']:ok(f'/api/errands/{task}/actions',rt,{'action':action,'expectedRound':0})
   key=str(uuid.uuid4());wait(lambda:req('/api/auth/config')[0]==200)
   # Producer startup is asynchronous; retry the same request key while unavailable.
   response=None
   for attempt in range(12):
    response=req(f'/api/errands/{task}/actions',ot,{'action':'SETTLE','expectedRound':0},key)
    if response[0]==200:break
    time.sleep(1)
   require(response[0]==200,str(response));keys.append(key);ids.append(str(task));time.sleep(2.1)
  require(sql('SELECT COUNT(*) FROM fund_event_receipt')=='0','worker offline but receipts present')
  before=sql('SELECT id,available,frozen,version FROM wallet_account ORDER BY id')
  log(experiment=E,phase='offline',tasks=ids,receipts=0,states=sql('SELECT status FROM errand WHERE id IN ('+','.join(ids)+')'))
  proxy.arm='insert into fund_event_receipt';proxy.hit.clear();proxy.release.clear();worker=start('worker',True)
  require(proxy.hit.wait(45),'receipt COMMIT checkpoint missing');worker.kill();worker.wait();proxy.release.set()
  committed=int(sql('SELECT COUNT(*) FROM fund_event_receipt'));require(committed>0,'receipt not committed')
  first=proxy.count;started=time.monotonic();worker=start('worker',True)
  wait(lambda:sql('SELECT COUNT(*) FROM fund_event_receipt')=='3',90)
  wait(lambda:proxy.receiptCommits[proxy.blockedReceipt]>=2,90)
  require(before==sql('SELECT id,available,frozen,version FROM wallet_account ORDER BY id'),'consumer changed money')
  notices=int(sql('SELECT COUNT(*) FROM notification WHERE errand_id IN ('+','.join(ids)+')'));require(notices==6,'duplicate/missing notifications')
  log(experiment=E,phase='recovered',receipts=3,notifications=notices,receiptSQLExecutions=proxy.count,preRestartExecutions=first,redeliveredRequest=proxy.blockedReceipt,redeliveredExecutions=proxy.receipts[proxy.blockedReceipt],redeliveredCommits=proxy.receiptCommits[proxy.blockedReceipt],recoverySeconds=round(time.monotonic()-started,2),walletUnchanged=True)
 elif E=='e10':
  runner,rt=user();samples=[]
  require(sql('SELECT COUNT(*) FROM fund_event_receipt')=='0','verification receipts must start empty')
  for sample in range(20):
   task=publish(ot);require(grab(rt,task)[0]==200,'grab preparation')
   ok(f'/api/errands/{task}/confirm',rt,{'expectedRound':0})
   for action in ['PICK_UP','DELIVER']:ok(f'/api/errands/{task}/actions',rt,{'action':action,'expectedRound':0})
   key=str(uuid.uuid4())
   for attempt in range(12):
    status,data=req(f'/api/errands/{task}/actions',ot,{'action':'SETTLE','expectedRound':0},key)
    if status==200:break
    time.sleep(1)
   require(status==200 and data.get('code')=='OK',str((status,data)))
   require(sql(f'SELECT status FROM errand WHERE id={task}')=='SETTLED','settlement did not commit')
   wallet=sql('SELECT id,available,frozen,version FROM wallet_account ORDER BY id')
   proxy.arm='insert into fund_event_receipt';proxy.hit.clear();proxy.release.clear()
   worker=start('worker',True)
   require(proxy.hit.wait(45),'receipt COMMIT checkpoint missing')
   blocked=proxy.blockedReceipt
   worker.kill();worker.wait();proxy.release.set()
   started=time.monotonic();worker=start('worker',True)
   wait(lambda:proxy.receiptCommits[blocked]>=2 and int(sql(f'SELECT COUNT(*) FROM notification WHERE errand_id={task}'))==2,90)
   seconds=time.monotonic()-started
   require(int(sql('SELECT COUNT(*) FROM fund_event_receipt'))==sample+1,'receipt count mismatch')
   require(wallet==sql('SELECT id,available,frozen,version FROM wallet_account ORDER BY id'),'consumer changed money')
   samples.append(seconds)
   log(experiment=E,phase='sample',number=sample+1,task=str(task),recoverySeconds=round(seconds,3),receiptCommits=proxy.receiptCommits[blocked],notifications=2,walletUnchanged=True)
   worker.terminate();worker.wait(timeout=25)
  ordered=sorted(samples)
  log(experiment=E,phase='summary',samples=len(samples),definition='worker restart to broker redelivery and duplicate receipt ACK; receipt and notifications committed before crash',p95Method='nearest-rank',p95Seconds=round(ordered[18],3),medianSeconds=round((ordered[9]+ordered[10])/2,3),minSeconds=round(ordered[0],3),maxSeconds=round(ordered[-1],3))
 elif E=='e11':
  publish_key=str(uuid.uuid4())
  publish_body={'campusId':'1','type':'DELIVERY','title':'D5验证任务','rewardCents':'125','slotTotal':1}
  for attempt in range(12):
   status,data=req('/api/errands',ot,publish_body,publish_key)
   if status==200 and data.get('code')=='OK':break
   time.sleep(1)
  require(status==200 and data.get('code')=='OK',str((status,data)))
  task=data['data']['errandId']
  path=f'/api/errands/{task}'
  def detail_req():
   request=urllib.request.Request('http://127.0.0.1:18126'+path,headers={'Authorization':'Bearer '+ot})
   try:
    try:response=urllib.request.urlopen(request,timeout=12)
    except urllib.error.HTTPError as error:response=error
    with response:return response.status,json.load(response).get('code')
   except (OSError,TimeoutError):return 0,'CONNECTION_LOST'
  for _ in range(20):require(detail_req()==(200,'OK'),'detail warmup failed')
  def metrics():
   raw=urllib.request.urlopen('http://127.0.0.1:18126/metrics',timeout=3).read().decode()
   return {name:int(float(value)) for name,value in re.findall(r'campusgo_detail_operations_total\{operation="([^"]+)"\} ([0-9.e+]+)',raw)}
  before=metrics()
  rate,duration=int(os.getenv('CAMPUSGO_E11_RATE','100')),30
  def sample(planned):
   started=time.monotonic();status,code=detail_req()
   return (round((time.monotonic()-started)*1000,3),round(max(0,started-planned)*1000,3),status,code)
  start_at=time.monotonic()+.5
  with cf.ThreadPoolExecutor(max_workers=80) as pool:
   futures=[]
   for i in range(rate*duration):
    due=start_at+i/rate
    if due>time.monotonic():time.sleep(due-time.monotonic())
    futures.append(pool.submit(sample,due))
   results=[f.result() for f in futures]
  after=metrics();codes=dict(collections.Counter(f'{status}:{code}' for _,_,status,code in results))
  times=sorted(r[0] for r in results);delays=sorted(r[1] for r in results)
  delta={name:after.get(name,0)-before.get(name,0) for name in set(before)|set(after)}
  with (OUT/'detail-latencies.jsonl').open('w') as raw:
   for latency,queued,status,code in results:raw.write(json.dumps({'latencyMs':latency,'clientQueueMs':queued,'status':status,'code':code})+'\n')
  log(experiment=E,phase='detail-hot',scope='direct HTTP API, auth + Redis cache (zero-SQL hit); excludes APISIX',task=str(task),rate=rate,durationSeconds=duration,samples=len(results),responses=codes,p50Ms=times[len(times)//2],p95Ms=times[int(.95*len(times))-1],p99Ms=times[int(.99*len(times))-1],maxMs=times[-1],clientQueueP99Ms=delays[int(.99*len(delays))-1],detailMetrics=delta)
  require(codes=={'200:OK':len(results)},'detail HTTP errors')
  require(delta.get('hit')==len(results) and delta.get('sql_body_query',0)==0 and delta.get('sql_authority_query',0)==0,'expected all detail requests to hit cache without detail SQL')
 else:
  pairs=[user() for _ in range(100)]
  task=publish(ot);barrier=threading.Barrier(100)
  def hot(pair):
   key=str(uuid.uuid4());barrier.wait();return pair[1],key,grab(pair[1],task,key)
  with cf.ThreadPoolExecutor(100) as pool:results=list(pool.map(hot,pairs))
  require(sql(f'SELECT COUNT(*) FROM grab_record WHERE errand_id={task}')=='1','hot task has wrong winner count')
  log(experiment=E,phase='hot-task',task=task,responses=dict(collections.Counter(str(r[2][0])+':'+r[2][1].get('code','') for r in results)),durableWinners=1)
  hotTask=task;worker=start('worker');wait(lambda:sql(f"SELECT COUNT(*) FROM grab_request WHERE errand_id={hotTask} AND state='PROCESSING'")=='0',45)
  finalStates=collections.Counter()
  for token,key,initial in results:
   status,data=req(f'/api/errands/{hotTask}/grab-requests/{key}',token)
   if status==202 and data['data']['state']=='NOT_OBSERVED':status,data=grab(token,hotTask,key)
   require(status in (200,409) and data.get('data',{}).get('state') in ('SUCCEEDED','REJECTED','EXPIRED'),str((status,data)))
   finalStates[data['data']['state']]+=1
  require(finalStates['SUCCEEDED']==1 and sum(finalStates.values())==100,'hot-key replay mismatch')
  log(experiment=E,phase='hot-final-results',states=dict(finalStates))

  # Use fresh identities so the hot-task attempt cannot consume their rate budget.
  fresh=[user() for _ in range(60)];jobs=[(t,publish(ot),str(uuid.uuid4())) for _,t in fresh]
  locker=subprocess.Popen(mysql,stdin=subprocess.PIPE,stdout=subprocess.PIPE,text=True)
  locker.stdin.write('START TRANSACTION;SELECT id FROM errand WHERE id IN ('+','.join(str(t) for _,t,_ in jobs)+") FOR UPDATE;SELECT 'LOCK_READY';DO SLEEP(1.5);ROLLBACK;\n");locker.stdin.close()
  while locker.stdout.readline().strip()!='LOCK_READY':require(locker.poll() is None,'lock failed')
  barrier=threading.Barrier(60)
  def load(job):barrier.wait();return grab(*job)
  with cf.ThreadPoolExecutor(60) as pool:responses=list(pool.map(load,jobs))
  require(locker.wait()==0,'locker failed');rejected=[i for i,r in enumerate(responses) if r[0]==503];require(rejected,'did not trigger protection')
  time.sleep(2.2);recovered=0
  for i in rejected:
   t,task,key=jobs[i];s,d=req(f'/api/errands/{task}/grab-requests/{key}',t)
   require(s==202 and d['data']['state']=='NOT_OBSERVED',str((s,d)))
   s,d=grab(t,task,key);require(s==200 and d['data']['state']=='SUCCEEDED',str((s,d)));recovered+=1
  require(int(sql('SELECT COUNT(*) FROM grab_record WHERE errand_id IN ('+','.join(str(t) for _,t,_ in jobs)+')'))==60,'lost valid grabs')
  log(experiment=E,phase='overload-recovery',requests=60,initialResponses=dict(collections.Counter(str(r[0])+':'+r[1].get('code','') for r in responses)),sameKeyRecovered=recovered,durableWinners=60)
 reconcile('during');log(experiment=E,result='ASSERTIONS_PASSED')
finally:
 proxy.release.set()
 for p in reversed(processes):
  if p.poll() is None:
   p.terminate()
   try:p.wait(timeout=25)
   except subprocess.TimeoutExpired:p.kill();p.wait()
 proxy.close()
 subprocess.run(['docker','logs',gateway],stdout=(OUT/'gateway.log').open('w'),stderr=subprocess.STDOUT)
 subprocess.run(['docker','rm','-f',gateway],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
 try:
  if owns:
   ids=','.join(str(u) for u in users) or '0'
   tasks=sql('SELECT id FROM errand WHERE publisher_id IN ('+ids+')').splitlines()
   (OUT/'fixtures-owned.json').write_text(json.dumps({'users':users,'tasks':tasks,'sessionNamespace':env['CAMPUSGO_SESSION_NAMESPACE']}))
   subprocess.run(['python3','verification/d5-cleanup.py'],check=True)
  redis=compose+['exec','-T','redis','sh','-c','export REDISCLI_AUTH="$CAMPUSGO_REDIS_PASSWORD";exec redis-cli --raw "$@"','e06-cleanup']
  patterns=['campusgo:auth:test:live:'+env['CAMPUSGO_SESSION_NAMESPACE']+':*','campusgo:gateway:'+E+':*']
  patterns += ['campusgo:campusgo_verification:grab:v2:{'+task+'}:*' for task in tasks]
  patterns += ['campusgo:campusgo_verification:detail:'+task+':*' for task in tasks]
  patterns += ['campusgo:campusgo_verification:rl:{runner:'+str(u)+'}:*' for u in users]
  for pattern in patterns:
   ks=subprocess.check_output(redis+['--scan','--pattern',pattern],text=True).splitlines()
   for i in range(0,len(ks),100):subprocess.run(redis+['DEL']+ks[i:i+100],stdout=subprocess.DEVNULL,check=True)
  reconcile('after')
 finally:LOCK.rmdir()

log(experiment=E,result='PASS',cleanup='complete',bothDatabasesReconciled=True)
