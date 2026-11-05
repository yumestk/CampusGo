#!/bin/sh
set -eu
cd "$(dirname "$0")/.."
mkdir -p work
./verification/d7-backend.sh
docker compose --env-file .env -f deploy/local/compose.yaml up -d --wait rmqbroker
docker compose --env-file .env -f deploy/local/compose.yaml up -d --force-recreate rmq-init
docker compose --env-file .env -f deploy/local/compose.yaml wait rmq-init
./bin/probe -component mq-route
# The D6 test entry configuration raises only auth rate limits for deterministic
# browser scenarios; native gateway and business policies remain in force.
python3 verification/d6-config.py
docker compose --env-file .env -f deploy/local/compose.yaml -f verification/d6-compose.yaml --profile gateway up -d --force-recreate apisix
api_pid='';second_pid='';worker_pid=''
cleanup(){
 for pid in "$api_pid" "$second_pid" "$worker_pid";do if [ -n "$pid" ];then kill -TERM "$pid" 2>/dev/null||true;wait "$pid" 2>/dev/null||true;fi;done
 python3 verification/d5-browser-cleanup.py
 docker compose --env-file .env -f deploy/local/compose.yaml --profile gateway up -d --force-recreate apisix >/dev/null
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
# Reuse D5's guarded wallet/credit snapshot and task manifest, not global cleanup.
python3 verification/d5-browser-cleanup.py
python3 verification/d5-browser-cleanup.py capture
export CAMPUSGO_ENV=test CAMPUSGO_DATABASE=campusgo_verification CAMPUSGO_AUTH_MODE=session
export CAMPUSGO_SESSION_NAMESPACE="d7-$(date +%s)-$$" CAMPUSGO_D7_BROWSER=1
export CAMPUSGO_OIDC_ISSUER='' CAMPUSGO_OIDC_CALLBACK_URL=''
d7_api_host=${CAMPUSGO_D7_API_HOST:-127.0.0.1}
./bin/worker >work/worker-d7.log 2>&1 & worker_pid=$!
(cd web && npm ci && npm test && npm run build && npx playwright install chromium)
for mode in session jwt; do
 d7_ttl=2h
 if [ "$mode" = jwt ];then d7_ttl=30s;fi
 CAMPUSGO_AUTH_MODE="$mode" CAMPUSGO_SESSION_TTL="$d7_ttl" CAMPUSGO_REQUIRE_GATEWAY=true CAMPUSGO_API_ADDR="$d7_api_host:18080" ./bin/api >"work/api-d7-$mode.log" 2>&1 & api_pid=$!
 CAMPUSGO_AUTH_MODE="$mode" CAMPUSGO_SESSION_TTL="$d7_ttl" CAMPUSGO_REQUIRE_GATEWAY=false CAMPUSGO_API_ADDR=127.0.0.1:18086 CAMPUSGO_ID_WORKER=10 ./bin/api >"work/api-d7-second-$mode.log" 2>&1 & second_pid=$!
 ready=false
 for attempt in $(seq 1 40);do
  kill -0 "$api_pid" "$second_pid" "$worker_pid"
  if curl -fsS http://127.0.0.1:19080/api/auth/config >/dev/null && curl -fsS http://127.0.0.1:18086/livez >/dev/null && curl -fsS http://127.0.0.1:18081/livez >/dev/null;then ready=true;break;fi
  sleep 1
 done
 test "$ready" = true
 (cd web && CAMPUSGO_D7_MODE="$mode" CAMPUSGO_WEB_API=http://127.0.0.1:19080 npm run test:e2e -- d7.spec.ts)
 for pid in "$api_pid" "$second_pid";do kill -TERM "$pid";wait "$pid";done
 api_pid='';second_pid=''
done
./bin/reconcile -database campusgo_verification
cleanup
api_pid='';second_pid='';worker_pid=''
trap - EXIT
./bin/reconcile -database campusgo_verification
./bin/reconcile
printf '%s\n' 'D7 multi-node realtime, durable notifications and browser checks passed.'
