#!/bin/sh
set -eu
cd "$(dirname "$0")/.."
mkdir -p work
node -e 'if (Number(process.versions.node.split(".")[0]) < 24) throw Error("D4 web checks require Node 24+")'
./verification/d4-backend.sh
sql() { docker compose --env-file .env -f deploy/local/compose.yaml exec -T mysql sh -c 'MYSQL_PWD="$MYSQL_PASSWORD" exec mysql --default-character-set=utf8mb4 -ucampusgo campusgo_verification'; }
api_pid=''
worker_pid=''
cleanup() {
 for pid in "$api_pid" "$worker_pid"; do
  if [ -n "$pid" ]; then kill -TERM "$pid" 2>/dev/null || true; wait "$pid" 2>/dev/null || true; fi
 done
 python3 verification/d4-browser-cleanup.py
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
python3 verification/d4-browser-cleanup.py
python3 verification/d4-browser-credit.py capture
sql <verification/fixtures/d4.sql
export CAMPUSGO_ENV=test CAMPUSGO_DATABASE=campusgo_verification
export CAMPUSGO_SESSION_NAMESPACE="d4-browser-$(date +%s)-$$"
export CAMPUSGO_D4_BROWSER=1
./bin/api >work/api-d4.log 2>&1 &
api_pid=$!
./bin/worker >work/worker-d4.log 2>&1 &
worker_pid=$!
ready=false
for attempt in $(seq 1 30); do
 kill -0 "$api_pid"
 kill -0 "$worker_pid"
 if curl -fsS "http://$CAMPUSGO_API_ADDR/livez" >/dev/null 2>&1 && curl -fsS "http://$CAMPUSGO_WORKER_ADDR/livez" >/dev/null 2>&1; then ready=true; break; fi
 sleep 1
done
test "$ready" = true
(cd web && export CAMPUSGO_WEB_API=http://127.0.0.1:18080 && npm ci && npm test && npm run build && npx playwright install chromium && npm run test:e2e -- d4.spec.ts)
cleanup
api_pid=''
worker_pid=''
trap - EXIT
./bin/reconcile -database campusgo_verification
./bin/reconcile
printf '%s\n' 'D4 grab, browser recovery and worker checks passed; verification fixtures removed.'
