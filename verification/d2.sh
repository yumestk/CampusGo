#!/bin/sh
set -eu
cd "$(dirname "$0")/.."
mkdir -p work
node -e 'if (Number(process.versions.node.split(".")[0]) < 24) throw Error("D2 web checks require Node 24+")'
docker compose --env-file .env -f deploy/local/compose.yaml up -d --wait mysql redis
python3 verification/d2-cleanup.py
./verification/d2-backend.sh
api_pid=''
cleanup() {
  if [ -n "$api_pid" ]; then
    kill -TERM "$api_pid" 2>/dev/null || true
    wait "$api_pid" 2>/dev/null || true
  fi
  python3 verification/d2-cleanup.py
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
export CAMPUSGO_ENV=test CAMPUSGO_DATABASE=campusgo_verification
export CAMPUSGO_SESSION_NAMESPACE="d2-$(date +%s)-$$"
export CAMPUSGO_D2_BROWSER=1
./bin/api >work/api-d2.log 2>&1 &
api_pid=$!
ready=false
for attempt in $(seq 1 30); do
  kill -0 "$api_pid"
  if curl -fsS "http://$CAMPUSGO_API_ADDR/livez" >/dev/null 2>&1; then ready=true; break; fi
  sleep 1
done
test "$ready" = true
(cd web && npm ci && npm test && npm run build && npx playwright install chromium && npm run test:e2e -- d2.spec.ts)
./bin/reconcile -database campusgo_verification
cleanup
api_pid=''
trap - EXIT
./bin/reconcile -database campusgo_verification
./bin/reconcile
printf '%s\n' 'D2 funds, publish recovery and browser checks passed; verification money restored.'
