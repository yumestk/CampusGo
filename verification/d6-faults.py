import concurrent.futures, http.client,json,os,socket,subprocess,time,uuid
from pathlib import Path
compose=['docker','compose','--env-file','.env','-f','deploy/local/compose.yaml']
def request(path,method='GET',body=None,headers=None,port=19080):
 c=http.client.HTTPConnection('127.0.0.1',port,timeout=5)
 h={'Content-Type':'application/json',**(headers or {})};c.request(method,path,json.dumps(body) if body is not None else None,h);r=c.getresponse();data=r.read();c.close()
 try:value=json.loads(data)
 except:value={}
 return r.status,dict(r.getheaders()),value
checks=[]
def check(name,fn):fn();checks.append(name);print('PASS',name,flush=True)
def paths():
 for path in ['/api/internal/secret','/api/internal','/api/%69nternal/secret','/api//internal/secret','/api/../api/internal/secret','/api/internal;foo/secret','/api/internal\\secret','/api/%252e%252e/internal']:
  status,_,_=request(path);assert status in [400,404],(path,status)
check('canonical paths and internal denial',paths)
rid=str(uuid.uuid4());status,h,v=request('/api/auth/config',headers={'X-Request-Id':rid});assert status==200 and h.get('X-Request-Id')==rid
def headers():
 forged={'X-User-Id':'9001','X-Runner-Id':'9001','X-Campus-Id':'2','X-Internal-Token':'forged','Forwarded':'for=203.0.113.9','X-Forwarded-For':'203.0.113.9','X-Forwarded-Host':'evil','X-Forwarded-Proto':'https','X-Forwarded-Port':'443','X-Forwarded-Prefix':'evil','X-Real-IP':'203.0.113.9','X-CampusGo-Gateway':'forged','X-CampusGo-Source':'forged'}
 status,_,v=request('/__d6/headers',headers=forged);assert status==200,(status,v)
 normalized={k.lower():val for k,val in v.items()}
 for k,val in forged.items():assert val not in str(normalized.get(k.lower(),[])),k
 # The verification peer must not reflect the actual gateway secret into logs.
 assert v.get('X-Campusgo-Gateway') or v.get('X-CampusGo-Gateway') or any(k.lower()=='x-campusgo-gateway' for k in v)
 assert request('/api/users/me',headers=forged)[0]==401
 assert request('/api/auth/config',port=18080)[0]==403
 assert request('/api/auth/config',headers={'X-CampusGo-Gateway':'forged'},port=18080)[0]==403
check('identity sanitization and direct API rejection',headers)
def upstream():
 for path in ['/__d6/dead','/__d6/timeout']:
  status,h,v=request(path);assert status==503 and v.get('code')=='UPSTREAM_UNAVAILABLE' and h.get('Retry-After')=='1',(status,v)
check('upstream connection refusal and timeout retain JSON contract',upstream)
def rate():
 for path in ['/__d6/route','/__d6/ip','/api/auth/d6-test']:
  for expected in [200,200,429]:
   status,h,v=request(path,headers={'X-Forwarded-For':str(uuid.uuid4())});assert status==expected,(path,status,v)
   if expected==429:assert v['code']=='RATE_LIMITED' and int(h['Retry-After'])>=1
check('Redis route, IP, and auth token buckets',rate)
def concurrency():
 with concurrent.futures.ThreadPoolExecutor() as pool:
  first=pool.submit(request,'/__d6/slow');time.sleep(.2);status,h,v=request('/__d6/slow');assert status==503 and v['code']=='GATEWAY_BUSY' and h.get('Retry-After')=='1',(status,v);assert first.result()[0]==200
 assert request('/__d6/slow')[0]==200
check('native concurrency rejection and release',concurrency)
def cancel():
 Path('work/d6-cancelled').unlink(missing_ok=True)
 c=socket.create_connection(('127.0.0.1',19080));c.sendall(b'GET /__d6/cancel HTTP/1.1\r\nHost: localhost.invalid\r\n\r\n');time.sleep(.3);c.shutdown(socket.SHUT_RDWR);c.close()
 for _ in range(30):
  if Path('work/d6-cancelled').exists():return
  time.sleep(.1)
 raise AssertionError('client cancellation not propagated')
check('client cancellation reaches upstream',cancel)
def outage():
 token=request('/api/auth/login','POST',{'userId':'1001'})[2]['data']['token']
 jwt=request('/api/auth/login','POST',{'userId':'1001'},headers={'Host':'localhost'})[2]['data']
 try:
  subprocess.run(compose+['stop','redis'],check=True,stdout=subprocess.DEVNULL)
  for header,access in [({},token),({'Host':'localhost'},jwt['token'])]:
   status,h,v=request('/api/users/me',headers={**header,'Authorization':'Bearer '+access});assert status==503 and v['code']=='RATE_LIMIT_UNAVAILABLE' and h.get('Retry-After')=='1',(status,v)
  status,_,v=request('/api/fund-requests/'+str(uuid.uuid4()),'POST',{});assert status==503 and v['code']=='RATE_LIMIT_UNAVAILABLE'
  # A recovery allowance forwards to API, whose Redis authentication still fails closed.
  path='/api/fund-requests/'+str(uuid.uuid4());seen=[]
  start=time.monotonic()
  with concurrent.futures.ThreadPoolExecutor(max_workers=40) as pool:
   for status,h,v in pool.map(lambda _:request(path,headers={'Authorization':'Bearer '+token}),range(45)):
    assert status==503,(status,v);seen.append(v.get('code'))
  assert 'DEPENDENCY_UNAVAILABLE' in seen and 'RATE_LIMIT_UNAVAILABLE' in seen,seen
 finally:subprocess.run(compose+['up','-d','--wait','redis'],check=True,stdout=subprocess.DEVNULL)
 for _ in range(30):
  if request('/api/users/me',headers={'Authorization':'Bearer '+token})[0]==200:break
  time.sleep(.2)
 else:raise AssertionError('Session did not recover')
 assert request('/api/users/me',headers={'Host':'localhost','Authorization':'Bearer '+jwt['token']})[0]==200
 assert request('/api/auth/logout','POST',headers={'Authorization':'Bearer '+token})[0]==200
 assert request('/api/auth/logout','POST',headers={'Host':'localhost','Authorization':'Bearer '+jwt['token']})[0]==200
check('Redis fault closure, bounded result recovery and credential survival',outage)
print(f'{len(checks)} real gateway fault groups passed.',flush=True)
