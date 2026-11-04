"""Local-only HTTP failure/recovery checks; credentials never appear in output."""
import json
import os
import subprocess
import time
import urllib.error
import urllib.request

BASE = 'http://' + os.environ['CAMPUSGO_API_ADDR']
COMPOSE = ['docker', 'compose', '--env-file', '.env', '-f', 'deploy/local/compose.yaml']


def request(path, token=None, data=None):
    headers = {'Content-Type': 'application/json'}
    if token:
        headers['Authorization'] = 'Bearer ' + token
    req = urllib.request.Request(BASE + path, headers=headers,
                                 data=None if data is None else json.dumps(data).encode())
    try:
        response = urllib.request.urlopen(req, timeout=10)
    except urllib.error.HTTPError as error:
        response = error
    with response:
        return response.status, response.headers, json.load(response)


status, _, login = request('/api/auth/login', data={'userId': '1001'})
assert status == 200, 'initial login failed'
token = login['data']['token']
for dependency in ('redis', 'mysql'):
    try:
        subprocess.run(COMPOSE + ['stop', dependency], check=True)
        for path in ('/api/users/me', '/api/errands?cursor='):
            status, headers, result = request(path, token)
            assert status == 503 and result['code'] == 'DEPENDENCY_UNAVAILABLE', (dependency, path, status, result['code'])
            assert headers.get('Retry-After') == '1'
        status, _, _ = request('/api/auth/logout', token, {})
        # Logout only requires Redis; a database outage must not prevent revocation.
        assert status == (503 if dependency == 'redis' else 200)
    finally:
        subprocess.run(COMPOSE + ['up', '-d', '--wait', dependency], check=True)
    if dependency == 'redis':
        # A failed logout and outage must leave the existing credential valid.
        for attempt in range(20):
            status, _, _ = request('/api/users/me', token)
            if status == 200:
                break
            time.sleep(.2)
        assert status == 200, 'session did not survive Redis recovery'
    else:
        status, _, _ = request('/api/users/me', token)
        assert status == 401, 'logout during DB outage did not revoke session'
        status, _, login = request('/api/auth/login', data={'userId': '1001'})
        assert status == 200, 'login did not recover after database restart'
        token = login['data']['token']
        status, _, _ = request('/api/errands?cursor=', token)
        assert status == 200, 'queries did not recover after database restart'
        status, _, _ = request('/api/auth/logout', token, {})
        assert status == 200
print('Redis/MySQL stop-restart, 503/Retry-After, retained session and independent logout passed.')
