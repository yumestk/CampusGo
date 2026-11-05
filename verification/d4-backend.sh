#!/bin/sh
set -eu
cd "$(dirname "$0")/.."
mkdir -p work
docker compose --env-file .env -f deploy/local/compose.yaml up -d --wait mysql redis rmqbroker
docker compose --env-file .env -f deploy/local/compose.yaml up -d --force-recreate rmq-init
docker compose --env-file .env -f deploy/local/compose.yaml wait rmq-init
./bin/probe -component mq-route
for db in campusgo campusgo_verification; do
 ./bin/migrate -database "$db" up
 ./bin/seed -database "$db"
done
python3 verification/d4-cleanup.py
trap 'python3 verification/d4-cleanup.py' EXIT
CAMPUSGO_D4_INTEGRATION=1 CAMPUSGO_D4_MQ=1 go test -race -count=1 -p 1 -v ./internal/errand ./internal/transport/httpapi
./bin/reconcile -database campusgo_verification
printf '%s\n' 'D4 confirmation, quota, promotion, re-pool and durable timeout recovery passed.'
