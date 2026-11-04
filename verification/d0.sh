#!/bin/sh
set -eu
cd "$(dirname "$0")/.."
mkdir -p work
docker compose --env-file .env -f deploy/local/compose.yaml --profile gateway up -d --wait
docker compose --env-file .env -f deploy/local/compose.yaml wait rmq-init
./bin/migrate -dir verification/migrations -table goose_db_version -database campusgo_verification up
./bin/api >work/api-verification.log 2>&1 &
api_pid=$!
./bin/worker >work/worker-verification.log 2>&1 &
worker_pid=$!
cleanup() {
  kill -TERM "$api_pid" "$worker_pid" 2>/dev/null || true
  wait "$api_pid" 2>/dev/null || true
  wait "$worker_pid" 2>/dev/null || true
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
ready=false
for attempt in $(seq 1 30); do
  kill -0 "$api_pid" "$worker_pid"
  if curl -fsS "http://$CAMPUSGO_API_ADDR/livez" >/dev/null 2>&1 && curl -fsS "http://$CAMPUSGO_WORKER_ADDR/livez" >/dev/null 2>&1; then
    ready=true
    break
  fi
  sleep 1
done
test "$ready" = true
curl -fsS "http://$CAMPUSGO_API_ADDR/metrics" >/dev/null
curl -fsS "http://$CAMPUSGO_WORKER_ADDR/metrics" >/dev/null
./bin/probe
kill -TERM "$api_pid" "$worker_pid"
wait "$api_pid"
wait "$worker_pid"
printf '%s\n' 'D0 process, protocol and gateway probes passed.'
