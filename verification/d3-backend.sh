#!/bin/sh
set -eu
cd "$(dirname "$0")/.."
mkdir -p work
docker compose --env-file .env -f deploy/local/compose.yaml up -d --wait mysql redis
for db in campusgo campusgo_verification; do
 ./bin/migrate -database "$db" up
 ./bin/seed -database "$db"
done
python3 verification/d3-cleanup.py
trap 'python3 verification/d3-cleanup.py' EXIT
CAMPUSGO_D3_INTEGRATION=1 go test -race -count=1 -p 1 -v ./internal/adapter/mysql ./internal/transport/httpapi
./bin/reconcile -database campusgo_verification
printf '%s\n' 'D3 SQL/Redis concurrency, lease fencing, COMMIT acknowledgment loss and SIGKILL recovery checks passed.'
