#!/usr/bin/env python3
"""One isolated 2000-client same-round grab burst through APISIX and cmd/api."""
import asyncio
import base64
import collections
import hashlib
import json
import os
import pathlib
import socket
import subprocess
import time
import uuid

ROOT = pathlib.Path(__file__).resolve().parent.parent
os.chdir(ROOT)
OUT = ROOT / "work/experiments/e09-hot-grab"
OUT.mkdir(parents=True, exist_ok=True)
LOCK = ROOT / "work/experiments/.running"
LOCK.mkdir()
MANIFEST = ROOT / "work/d5-fixtures.jsonl"
COMPOSE = ["docker", "compose", "--env-file", ".env", "-f", "deploy/local/compose.yaml"]
MYSQL = COMPOSE + ["exec", "-T", "mysql", "sh", "-c", 'MYSQL_PWD="$MYSQL_PASSWORD" exec mysql --default-character-set=utf8mb4 -N -B -ucampusgo campusgo_verification']
REDIS = COMPOSE + ["exec", "-T", "redis", "sh", "-c", 'export REDISCLI_AUTH="$CAMPUSGO_REDIS_PASSWORD"; exec redis-cli --pipe']
N = 2000
namespace = "e09-" + uuid.uuid4().hex[:12]
env = {**os.environ, "CAMPUSGO_ENV": "test", "CAMPUSGO_DATABASE": "campusgo_verification",
       "CAMPUSGO_SESSION_NAMESPACE": namespace, "CAMPUSGO_AUTH_MODE": "session",
       "CAMPUSGO_DEMO_LOGIN": "false", "CAMPUSGO_OIDC_ISSUER": "", "CAMPUSGO_OIDC_CALLBACK_URL": "",
       "CAMPUSGO_REQUIRE_GATEWAY": "true", "CAMPUSGO_API_ADDR": "127.0.0.1:18129",
       "CAMPUSGO_ID_WORKER": "1011"}
gateway = "campusgo-e09-hot-grab-gateway"
api = None
worker = None
gateway_started = False
owned = False
users = []
task_id = None


def sql(query):
    return subprocess.check_output(MYSQL, input=query, text=True).strip()


def record(**fields):
    line = json.dumps(fields, ensure_ascii=False)
    print(line, flush=True)
    with (OUT / "evidence.jsonl").open("a") as file:
        file.write(line + "\n")


def resp(*parts):
    data = [f"*{len(parts)}\r\n".encode()]
    for part in parts:
        part = str(part).encode()
        data.extend((f"${len(part)}\r\n".encode(), part, b"\r\n"))
    return b"".join(data)


async def request(index, token, task, key, release, connected):
    reader = writer = None
    try:
        reader, writer = await asyncio.wait_for(asyncio.open_connection("127.0.0.1", 19129), 12)
        connected[index] = True
        await release.wait()
        body = b'{"expectedRound":0}'
        raw = (f"POST /api/errands/{task}/grab HTTP/1.1\r\nHost: localhost\r\n"
               f"Authorization: Bearer {token}\r\nIdempotency-Key: {key}\r\n"
               f"Content-Type: application/json\r\nContent-Length: {len(body)}\r\n"
               "Connection: close\r\n\r\n").encode() + body
        writer.write(raw)
        await asyncio.wait_for(writer.drain(), 12)
        response = await asyncio.wait_for(reader.read(-1), 35)
        header, _, payload = response.partition(b"\r\n\r\n")
        status = int(header.split(b"\r\n", 1)[0].split()[1])
        code = "UNKNOWN"
        try:
            start = payload.find(b"{")
            code = json.JSONDecoder().raw_decode(payload[start:].decode())[0].get("code", "UNKNOWN")
        except (ValueError, UnicodeError):
            pass
        return f"{status}:{code}"
    except (OSError, asyncio.TimeoutError, ValueError, IndexError) as error:
        return "CLIENT_ERROR:" + type(error).__name__
    finally:
        if writer:
            writer.close()


async def burst(tokens, keys, task):
    release = asyncio.Event()
    connected = [False] * N
    jobs = [asyncio.create_task(request(i, tokens[i], task, keys[i], release, connected)) for i in range(N)]
    deadline = time.monotonic() + 12
    while sum(connected) + sum(job.done() for job in jobs) < N and time.monotonic() < deadline:
        await asyncio.sleep(.05)
    ready = sum(connected)
    if ready != N:
        record(result="CONNECT_INCOMPLETE", clients=N, connectedBeforeRelease=ready)
        for job in jobs:
            job.cancel()
        await asyncio.gather(*jobs, return_exceptions=True)
        raise RuntimeError(f"only {ready}/{N} clients connected before release")
    started = time.monotonic()
    release.set()
    results = await asyncio.gather(*jobs)
    return ready, round(time.monotonic() - started, 3), dict(collections.Counter(results))


try:
    if MANIFEST.exists():
        raise RuntimeError("unfinished D5 fixtures")
    if sql("SELECT COUNT(*) FROM wallet_ledger WHERE ref_type<>'OPENING'") != "0":
        raise RuntimeError("verification database has non-opening transfers")
    (OUT / "evidence.jsonl").write_text("")
    before = subprocess.check_output(["./bin/reconcile", "-database", "campusgo_verification"], env=os.environ)
    (OUT / "before-reconcile.json").write_bytes(before)

    base = int(time.time() * 1000000)
    users = list(range(base, base + N + 1))
    owner, runners = users[0], users[1:]
    task_id = base + N + 100
    with MANIFEST.open("w") as file:
        for user in users:
            file.write(json.dumps({"kind": "user", "id": str(user)}) + "\n")
    owned = True
    for offset in range(0, len(users), 250):
        batch = users[offset:offset + 250]
        values = ",".join(f"({user},1,'D5验证用户','STUDENT')" for user in batch)
        wallets = ",".join(f"({user},{user},'USER',0,0,0)" for user in batch)
        sql("START TRANSACTION; INSERT INTO app_user(id,campus_id,display_name,role) VALUES " + values
            + "; INSERT INTO wallet_account(id,owner_id,owner_type,available,frozen,version) VALUES " + wallets
            + "; COMMIT;")
    sql(f"INSERT INTO errand(id,campus_id,publisher_id,type,title,reward_amount,slot_total,status,version) "
        f"VALUES({task_id},1,{owner},'DELIVERY','D5验证任务',125,1,'PUBLISHED',1)")

    tokens = [base64.urlsafe_b64encode(os.urandom(32)).decode().rstrip("=") for _ in runners]
    keys = [str(uuid.uuid4()) for _ in runners]
    prefix = "campusgo:auth:test:live:" + namespace + ":session:"
    commands = b"".join(resp("SET", prefix + hashlib.sha256(token.encode()).hexdigest(), user, "EX", 3600)
                        for user, token in zip(runners, tokens))
    piped = subprocess.run(REDIS, input=commands, capture_output=True, check=True)
    if b"errors: 0" not in piped.stdout:
        raise RuntimeError("Redis session pipeline failed: " + piped.stdout.decode(errors="replace"))

    config = (ROOT / "deploy/local/apisix/apisix.yaml").read_text()
    config = config.replace("host.docker.internal:18080", "host.docker.internal:18129")
    config = config.replace("campusgo-entry: {}", "campusgo-entry: {namespace: e09, route_rate: 10000, route_burst: 20000, ip_rate: 10000, ip_burst: 20000, auth_rate: 1000, auth_burst: 2000}")
    (OUT / "apisix.yaml").write_text(config)
    container = subprocess.check_output(COMPOSE + ["ps", "-q", "mysql"], text=True).strip()
    networks = json.loads(subprocess.check_output(["docker", "inspect", container]))[0]["NetworkSettings"]["Networks"]
    network = next(iter(networks))
    with (OUT / "api.log").open("w") as log:
        api = subprocess.Popen(["./bin/api"], env=env, stdout=log, stderr=subprocess.STDOUT)
    end = time.monotonic() + 30
    while time.monotonic() < end:
        if api.poll() is not None:
            raise RuntimeError("API exited; inspect api.log")
        try:
            with socket.create_connection(("127.0.0.1", 18129), .5):
                break
        except OSError:
            time.sleep(.1)
    else:
        raise TimeoutError("API did not start")
    subprocess.run(["docker", "run", "-d", "--rm", "--name", gateway, "--network", network,
                    "--add-host", "host.docker.internal:host-gateway", "-p", "127.0.0.1:19129:9080",
                    "-e", "CAMPUSGO_REDIS_PASSWORD", "-e", "CAMPUSGO_GATEWAY_SECRET",
                    "-v", str(OUT / "apisix.yaml") + ":/usr/local/apisix/conf/apisix.yaml:ro",
                    "-v", str(ROOT / "deploy/local/apisix/config.yaml") + ":/usr/local/apisix/conf/config.yaml:ro",
                    "-v", str(ROOT / "deploy/local/apisix/campusgo-entry.lua") + ":/usr/local/apisix/apisix/plugins/campusgo-entry.lua:ro",
                    "apache/apisix:3.19.0-debian@sha256:9a7e45dc943fbf10ec916d1232bae4cda9302ae28a4696cfb83c2da01d261141"],
                   check=True, stdout=subprocess.DEVNULL)
    gateway_started = True
    time.sleep(2)
    ready, elapsed, responses = asyncio.run(burst(tokens, keys, task_id))
    durable = sql(f"SELECT COUNT(*) FROM grab_record WHERE errand_id={task_id} AND result='GRABBED'")
    transitions = sql(f"SELECT COUNT(*) FROM errand_status_log WHERE errand_id={task_id} AND to_status='LOCKED'")
    requests = sql(f"SELECT state,COUNT(*) FROM grab_request WHERE errand_id={task_id} GROUP BY state")
    task = sql(f"SELECT status,round,slot_taken,COALESCE(grabber_id,0) FROM errand WHERE id={task_id}")
    record(clients=N, connectedBeforeRelease=ready, burstSeconds=elapsed, responses=responses,
           durableWinners=int(durable), lockTransitions=int(transitions), grabRequests=requests.splitlines(), task=task)
    if sum(responses.values()) != N or any(code.startswith("CLIENT_ERROR:") for code in responses):
        raise AssertionError("incomplete HTTP burst or client transport errors")
    if int(durable) != 1 or int(transitions) != 1 or task.split("\t")[0] != "LOCKED":
        raise AssertionError("same-round unique winner invariant failed")
    worker_env = {**env, "CAMPUSGO_WORKER_ADDR": "127.0.0.1:18130", "CAMPUSGO_ID_WORKER": "1010"}
    with (OUT / "worker.log").open("w") as log:
        worker = subprocess.Popen(["./bin/worker"], env=worker_env, stdout=log, stderr=subprocess.STDOUT)
    deadline = time.monotonic() + 50
    while time.monotonic() < deadline:
        if worker.poll() is not None:
            raise RuntimeError("worker exited; inspect worker.log")
        pending = int(sql(f"SELECT COUNT(*) FROM grab_request WHERE errand_id={task_id} AND state='PROCESSING'"))
        if pending == 0:
            break
        time.sleep(.5)
    else:
        raise TimeoutError("grab requests did not converge")
    final_states = sql(f"SELECT state,COUNT(*) FROM grab_request WHERE errand_id={task_id} GROUP BY state")
    record(phase="converged", grabRequests=final_states.splitlines(), durableWinners=int(sql(f"SELECT COUNT(*) FROM grab_record WHERE errand_id={task_id} AND result='GRABBED'")))
    record(result="ASSERTIONS_PASSED")
finally:
    if worker and worker.poll() is None:
        worker.terminate()
        worker.wait(timeout=25)
    if api and api.poll() is None:
        api.terminate()
        api.wait(timeout=25)
    if gateway_started:
        subprocess.run(["docker", "logs", gateway], stdout=(OUT / "gateway.log").open("w"), stderr=subprocess.STDOUT)
        subprocess.run(["docker", "rm", "-f", gateway], stdout=subprocess.DEVNULL)
    if owned:
        subprocess.run(["python3", "verification/d5-cleanup.py"], check=True)
        redis = COMPOSE + ["exec", "-T", "redis", "sh", "-c", 'export REDISCLI_AUTH="$CAMPUSGO_REDIS_PASSWORD"; exec redis-cli --raw "$@"', "e09-cleanup"]
        for pattern in (f"campusgo:auth:test:live:{namespace}:*", f"campusgo:gateway:e09:*",
                        f"campusgo:campusgo_verification:grab:v2:{{{task_id}}}:*",
                        f"campusgo:campusgo_verification:detail:{{{task_id}}}:*",
                        f"campusgo:campusgo_verification:candidates:{{{task_id}}}",
                        f"campusgo:campusgo_verification:candidate-meta:{{{task_id}}}"):
            found = subprocess.check_output(redis + ["--scan", "--pattern", pattern], text=True).splitlines()
            for offset in range(0, len(found), 100):
                subprocess.run(redis + ["DEL"] + found[offset:offset + 100], check=True, stdout=subprocess.DEVNULL)
        found = subprocess.check_output(redis + ["--scan", "--pattern", "campusgo:campusgo_verification:rl:{runner:*"], text=True).splitlines()
        own = {str(user) for user in runners}
        found = [key for key in found if key.split("{runner:", 1)[-1].split("}", 1)[0] in own]
        for offset in range(0, len(found), 100):
            subprocess.run(redis + ["DEL"] + found[offset:offset + 100], check=True, stdout=subprocess.DEVNULL)
        after = subprocess.check_output(["./bin/reconcile", "-database", "campusgo_verification"], env=os.environ)
        (OUT / "after-reconcile.json").write_bytes(after)
        if before != after:
            record(result="RECONCILE_CHANGED")
        else:
            record(result="PASS", cleanup="complete", verificationDatabaseReconciled=True)
    LOCK.rmdir()
