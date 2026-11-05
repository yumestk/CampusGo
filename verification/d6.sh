#!/bin/sh
set -eu
cd "$(dirname "$0")/.."
mkdir -p work
./verification/d6-backend.sh
docker compose --env-file .env -f deploy/local/compose.yaml --profile identity up -d --wait mysql redis
docker compose --env-file .env -f deploy/local/compose.yaml --profile identity up -d --force-recreate dex
python3 verification/d6-config.py
docker compose --env-file .env -f deploy/local/compose.yaml -f verification/d6-compose.yaml --profile gateway up -d --force-recreate apisix
api_pid='';jwt_pid='';probe_pid=''
cleanup(){
 for pid in "$api_pid" "$jwt_pid" "$probe_pid"; do if [ -n "$pid" ]; then kill -TERM "$pid" 2>/dev/null || true;wait "$pid" 2>/dev/null || true;fi; done
 docker compose --env-file .env -f deploy/local/compose.yaml up -d --wait redis >/dev/null
 docker compose --env-file .env -f deploy/local/compose.yaml exec -T -e "CAMPUSGO_D6_NAMESPACE=$CAMPUSGO_SESSION_NAMESPACE" redis sh -ec 'export REDISCLI_AUTH="$CAMPUSGO_REDIS_PASSWORD";redis-cli --scan --pattern "campusgo:auth:test:demo:$CAMPUSGO_D6_NAMESPACE:*" | while read -r key;do redis-cli del "$key" >/dev/null;done'
 docker compose --env-file .env -f deploy/local/compose.yaml --profile gateway up -d --force-recreate apisix >/dev/null
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
export CAMPUSGO_ENV=test CAMPUSGO_DATABASE=campusgo_verification CAMPUSGO_REQUIRE_GATEWAY=true
export CAMPUSGO_SESSION_NAMESPACE="d6-$(date +%s)-$$"
export CAMPUSGO_OIDC_CALLBACK_URL=http://127.0.0.1:19080/api/auth/oidc/callback
d6_api_host=${CAMPUSGO_D6_API_HOST:-127.0.0.1}
CAMPUSGO_API_ADDR="$d6_api_host:18080" CAMPUSGO_AUTH_MODE=session ./bin/api >work/api-d6-session.log 2>&1 & api_pid=$!
CAMPUSGO_AUTH_MODE=jwt CAMPUSGO_API_ADDR="$d6_api_host:18084" CAMPUSGO_ID_WORKER=9 CAMPUSGO_SESSION_TTL=10s CAMPUSGO_OIDC_CALLBACK_URL=http://localhost:19080/api/auth/oidc/callback ./bin/api >work/api-d6-jwt.log 2>&1 & jwt_pid=$!
go build -o work/d6-probe verification/d6-probe.go
./work/d6-probe >work/d6-probe.log 2>&1 & probe_pid=$!
ready=false
for attempt in $(seq 1 40); do
 kill -0 "$api_pid" "$jwt_pid" "$probe_pid"
 if curl -fsS http://127.0.0.1:18085/__d6/headers >/dev/null && curl -fsS http://127.0.0.1:18080/livez >/dev/null && curl -fsS http://127.0.0.1:18084/livez >/dev/null && curl -fsS http://127.0.0.1:19080/api/auth/config >/dev/null; then ready=true;break;fi
 sleep 1
done
test "$ready" = true
# Clear verification-only bucket keys before deterministic limit assertions.
docker compose --env-file .env -f deploy/local/compose.yaml exec -T redis sh -ec 'export REDISCLI_AUTH="$CAMPUSGO_REDIS_PASSWORD";redis-cli --scan --pattern "campusgo:gateway:d6*" | while read -r key;do redis-cli del "$key" >/dev/null;done'
python3 verification/d6-faults.py
python3 verification/d6-trusted.py
./work/d6-probe ws
for attempt in $(seq 1 20);do test -f work/d6-ws-closed && break;sleep .1;done
test -f work/d6-ws-closed
# Run each selected mode through the actual gateway; credentials are never in screenshots/traces.
(cd web && npm ci && npm test && npm run build && npx playwright install chromium)
for mode in session jwt; do
 (cd web && CAMPUSGO_D6_BROWSER=1 CAMPUSGO_D6_MODE="$mode" CAMPUSGO_WEB_AUTH_MODE="$mode" CAMPUSGO_WEB_API=http://127.0.0.1:19080 npm run test:e2e -- d6.spec.ts)
done
./bin/reconcile -database campusgo_verification
./bin/reconcile
printf '%s\n' 'D6 identity, gateway and both browser modes passed.'
