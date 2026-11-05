import http.client,json,subprocess,time

def get(headers=None):
 c=http.client.HTTPConnection('127.0.0.1',19080,timeout=4);c.request('GET','/__d6/headers',headers=headers or {});r=c.getresponse();data=json.loads(r.read());c.close();return {k.lower():v for k,v in data.items()}
peer=get()['x-campusgo-source'][0]
subprocess.run(['python3','verification/d6-config.py',peer],check=True)
def trusted(chain):
 c=http.client.HTTPConnection('127.0.0.1',19080,timeout=4);c.request('GET','/__d6/trusted',headers={'X-Forwarded-For':chain});r=c.getresponse();data=r.read();c.close()
 if r.status!=200:return None
 return {k.lower():v for k,v in json.loads(data).items()}['x-campusgo-source'][0]
for _ in range(40):
 if trusted('198.51.100.1, 203.0.113.5, 203.0.113.2')=='203.0.113.5':break
 time.sleep(.2)
else:raise AssertionError('trusted chain not resolved from right')
for chain in ['198.51.100.1, not-an-ip, 203.0.113.2','198.51.100.0/24','198.51.100.1,,203.0.113.2']:
 assert trusted(chain)==peer
assert get({'X-Forwarded-For':'198.51.100.1'})['x-campusgo-source'][0]==peer
print('PASS exact trusted peer, rightmost untrusted hop, malformed chain fallback and untrusted peer spoofing')
