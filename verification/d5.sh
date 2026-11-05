#!/bin/sh
set -eu
cd "$(dirname "$0")/.."
mkdir -p work
node -e 'if (Number(process.versions.node.split(".")[0]) < 24) throw Error("D5 requires Node 24+")'
./verification/d5-backend.sh
api_pid=''
worker_pid=''
cleanup() {
 for pid in "$api_pid" "$worker_pid"; do
  if [ -n "$pid" ]; then kill -TERM "$pid" 2>/dev/null || true; wait "$pid" 2>/dev/null || true; fi
 done
 python3 verification/d5-browser-cleanup.py
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
python3 verification/d5-browser-cleanup.py
python3 verification/d5-browser-cleanup.py capture
export CAMPUSGO_ENV=test CAMPUSGO_DATABASE=campusgo_verification
export CAMPUSGO_SESSION_NAMESPACE="d5-browser-$(date +%s)-$$" CAMPUSGO_D5_BROWSER=1
./bin/api >work/api-d5.log 2>&1 &
api_pid=$!
./bin/worker >work/worker-d5.log 2>&1 &
worker_pid=$!
ready=false
for attempt in $(seq 1 30); do
 kill -0 "$api_pid"
 kill -0 "$worker_pid"
 if curl -fsS "http://$CAMPUSGO_API_ADDR/livez" >/dev/null 2>&1 && curl -fsS "http://$CAMPUSGO_WORKER_ADDR/livez" >/dev/null 2>&1; then ready=true; break; fi
 sleep 1
done
test "$ready" = true
(cd web && npm ci && npm test && npm run build && npx playwright install chromium && npm run test:e2e -- d5.spec.ts)
./bin/reconcile -database campusgo_verification
# Receipt is observed before deleting fixture facts; producers/worker stop first.
received=false
for attempt in $(seq 1 240); do
 pending=$(docker compose --env-file .env -f deploy/local/compose.yaml exec -T mysql sh -c 'MYSQL_PWD="$MYSQL_PASSWORD" exec mysql -N -B -ucampusgo campusgo_verification -e "SELECT COUNT(*) FROM fund_request r LEFT JOIN fund_event_receipt c ON c.request_id=r.request_id WHERE r.state=\"COMMITTED\" AND c.request_id IS NULL"')
 if [ "$pending" = 0 ]; then received=true;break;fi
 sleep 1
done
test "$received" = true
curl -fsS "http://$CAMPUSGO_API_ADDR/metrics" >work/d5-api-metrics.txt
cleanup
api_pid=''
worker_pid=''
trap - EXIT
./bin/reconcile -database campusgo_verification
./bin/reconcile
printf '%s\n' 'D5 complete business flow, recovery and browser checks passed.'
