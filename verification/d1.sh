#!/bin/sh
set -eu
cd "$(dirname "$0")/.."
mkdir -p work
node -e 'if (Number(process.versions.node.split(".")[0]) < 24) throw Error("D1 web checks require Node 24+")'
docker compose --env-file .env -f deploy/local/compose.yaml --profile identity up -d --wait mysql redis dex
for db in campusgo campusgo_verification; do
  ./bin/migrate -database "$db" up
  ./bin/seed -database "$db"
done
sql() {
  docker compose --env-file .env -f deploy/local/compose.yaml exec -T mysql sh -c 'MYSQL_PWD="$MYSQL_PASSWORD" exec mysql --default-character-set=utf8mb4 -ucampusgo campusgo_verification'
}
api_pid=''
cleanup() {
  if [ -n "$api_pid" ]; then
    kill -TERM "$api_pid" 2>/dev/null || true
    wait "$api_pid" 2>/dev/null || true
  fi
  docker compose --env-file .env -f deploy/local/compose.yaml up -d --wait mysql redis >/dev/null 2>&1 || true
  sql <verification/fixtures/d1-cleanup.sql || true
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
sql <verification/fixtures/d1.sql
export CAMPUSGO_ENV=test CAMPUSGO_DATABASE=campusgo_verification
export CAMPUSGO_SESSION_NAMESPACE="d1-$(date +%s)-$$"
CAMPUSGO_INTEGRATION=1 go test -race -count=1 -p 1 ./internal/auth ./internal/transport/httpapi
./bin/api >work/api-d1.log 2>&1 &
api_pid=$!
ready=false
for attempt in $(seq 1 30); do
  kill -0 "$api_pid"
  if curl -fsS "http://$CAMPUSGO_API_ADDR/livez" >/dev/null 2>&1 && curl -fsS "$CAMPUSGO_OIDC_ISSUER/.well-known/openid-configuration" >/dev/null 2>&1; then
    ready=true
    break
  fi
  sleep 1
done
test "$ready" = true
python3 verification/d1-faults.py
(cd web && npm ci && npm test && npm run build && npx playwright install chromium && npm run test:e2e)
printf '%s\n' 'D1 identity, dependency recovery, query and browser checks passed.'
