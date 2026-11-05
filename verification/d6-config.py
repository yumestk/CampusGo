from pathlib import Path
import json,sys
base=Path('deploy/local/apisix/apisix.yaml').read_text().replace('#END','').replace('campusgo-entry: {}','campusgo-entry: {namespace: d6session, auth_rate: 100, auth_burst: 200}',1)
def route(name,path,config,port=18085,conn=1000):
 return f'''  - id: d6-{name}
    uri: {path}
    priority: 100
    enable_websocket: true
    plugins:
      campusgo-entry: {json.dumps(config)}
      limit-conn:
        conn: {conn}
        burst: 0
        default_conn_delay: 0.1
        key: server_addr
        rejected_code: 503
        rejected_msg: '{{"code":"GATEWAY_BUSY","message":"入口繁忙","data":null}}'
    upstream:
      type: roundrobin
      retries: 0
      timeout: {{connect: 1, send: 1, read: 2}}
      nodes:
        "host.docker.internal:{port}": 1
'''
for name,conf in [('headers',{}),('route',{'namespace':'d6route','route_rate':0.01,'route_burst':2}),('ip',{'namespace':'d6ip','ip_rate':0.01,'ip_burst':2}),('slow',{}),('cancel',{}),('timeout',{}),('ws',{}),('auth',{'namespace':'d6auth','auth_rate':0.01,'auth_burst':2})]:
 base+=route(name,('/api/auth/d6-test' if name=='auth' else '/__d6/'+name),conf,conn=1 if name=='slow' else 1000)
base+=route('dead','/__d6/dead',{},18086)
base+=route('jwt','/api/auth/jwt/*',{},18084)
# JWT business endpoints use a distinct entry hostname in the same gateway.
base+='''  - id: d6-jwt-api
    uri: /api/*
    host: localhost
    priority: 50
    plugins:
      campusgo-entry: {namespace: d6jwt, auth_rate: 100, auth_burst: 200}
    upstream:
      retries: 0
      type: roundrobin
      nodes:
        "host.docker.internal:18084": 1
'''
if len(sys.argv)>1:
 base+=route('trusted','/__d6/trusted',{'namespace':'d6trusted','trusted_proxies':[sys.argv[1],'203.0.113.2']})
Path('work/d6-apisix.yaml').write_text(base+'#END\n')
