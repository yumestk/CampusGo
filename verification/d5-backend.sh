#!/bin/sh
set -eu
cd "$(dirname "$0")/.."
mkdir -p work
docker compose --env-file .env -f deploy/local/compose.yaml up -d --wait mysql redis rmqbroker
docker compose --env-file .env -f deploy/local/compose.yaml up -d --force-recreate rmq-init
docker compose --env-file .env -f deploy/local/compose.yaml wait rmq-init
for db in campusgo campusgo_verification; do
 ./bin/migrate -database "$db" up
 ./bin/seed -database "$db"
done
python3 verification/d5-cleanup.py
trap 'python3 verification/d5-cleanup.py' EXIT
CAMPUSGO_D5_INTEGRATION=1 CAMPUSGO_D5_MQ=1 go test -race -count=1 -p 1 -v ./internal/errand ./internal/transport/httpapi
python3 verification/d5-cleanup.py
./bin/reconcile -database campusgo_verification
