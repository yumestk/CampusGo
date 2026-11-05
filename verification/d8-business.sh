#!/bin/sh
set -eu
cd "$(dirname "$0")/.."
./verification/d8-business-backend.sh
docker compose --env-file .env -f deploy/local/compose.yaml up -d --wait rmqbroker
docker compose --env-file .env -f deploy/local/compose.yaml up -d --force-recreate rmq-init
docker compose --env-file .env -f deploy/local/compose.yaml wait rmq-init
./bin/probe -component mq-route
python3 verification/d6-config.py
docker compose --env-file .env -f deploy/local/compose.yaml -f verification/d6-compose.yaml --profile gateway up -d --force-recreate apisix
api_pid='';worker_pid=''
cleanup(){
 for pid in "$api_pid" "$worker_pid";do if [ -n "$pid" ];then kill -TERM "$pid" 2>/dev/null||true;wait "$pid" 2>/dev/null||true;fi;done
 docker compose --env-file .env -f deploy/local/compose.yaml logs --no-color apisix >work/d8-business/gateway.log 2>&1
 python3 verification/d5-browser-cleanup.py
 docker compose --env-file .env -f deploy/local/compose.yaml --profile gateway up -d --force-recreate apisix >/dev/null
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
python3 verification/d5-browser-cleanup.py
python3 verification/d5-browser-cleanup.py capture
export CAMPUSGO_ENV=test CAMPUSGO_DATABASE=campusgo_verification CAMPUSGO_D5_BROWSER=1 CAMPUSGO_D8_BROWSER=1
export CAMPUSGO_OIDC_ISSUER='' CAMPUSGO_OIDC_CALLBACK_URL=''
export CAMPUSGO_SESSION_NAMESPACE="d8-business-$(date +%s)-$$"
# Docker-to-host upstream requires a listener reachable from the container.
# The API still enforces the gateway secret; this is a test-only binding.
d8_api_host=${CAMPUSGO_D8_API_HOST:-0.0.0.0}
./bin/worker >work/d8-business/worker.log 2>&1 & worker_pid=$!
(cd web && npm ci && npm test && npm run build && npx playwright install chromium)
for mode in session jwt;do
 CAMPUSGO_AUTH_MODE="$mode" CAMPUSGO_SESSION_TTL=2h CAMPUSGO_REQUIRE_GATEWAY=true CAMPUSGO_API_ADDR="$d8_api_host:18080" ./bin/api >"work/d8-business/api-$mode.log" 2>&1 & api_pid=$!
 ready=false
 for attempt in $(seq 1 40);do
  kill -0 "$api_pid" "$worker_pid"
  if curl -fsS http://127.0.0.1:19080/api/auth/config >/dev/null && curl -fsS http://127.0.0.1:18081/livez >/dev/null;then ready=true;break;fi
  sleep 1
 done
 test "$ready" = true
 # Existing normal business browser cases plus the new publish-to-credit flow.
 # No mocked network failures, stopped services or crash injection in this suite.
 (cd web && CAMPUSGO_WEB_API=http://127.0.0.1:19080 PLAYWRIGHT_JSON_OUTPUT_FILE="../work/d8-business/browser-$mode.json" npm run test:e2e -- d5.spec.ts d8.spec.ts --grep 'runner fulfills|published cancellation|dispute and arbitration|identity permissions and mobile layout|automatic settlement|D8 business' --reporter=line,json)
 kill -TERM "$api_pid";wait "$api_pid";api_pid=''
done
# Check eventual durable notification results, without injecting MQ faults.
received=false
for attempt in $(seq 1 120);do
 pending=$(docker compose --env-file .env -f deploy/local/compose.yaml exec -T mysql sh -c 'MYSQL_PWD="$MYSQL_PASSWORD" exec mysql -N -B -ucampusgo campusgo_verification -e "SELECT COUNT(*) FROM fund_request r LEFT JOIN fund_event_receipt c ON c.request_id=r.request_id WHERE r.state=\"COMMITTED\" AND c.request_id IS NULL"')
 if [ "$pending" = 0 ];then received=true;break;fi
 sleep 1
done
test "$received" = true
./bin/reconcile -database campusgo_verification
cleanup
api_pid='';worker_pid=''
trap - EXIT
./bin/reconcile -database campusgo_verification
./bin/reconcile
printf '%s\n' 'D8 business acceptance passed in Session and JWT modes; D8 is not yet a final release.'
