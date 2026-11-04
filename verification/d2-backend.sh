#!/bin/sh
set -eu
cd "$(dirname "$0")/.."
docker compose --env-file .env -f deploy/local/compose.yaml up -d --wait mysql redis
for db in campusgo campusgo_verification; do
  ./bin/migrate -database "$db" up
  ./bin/seed -database "$db"
done
./bin/reconcile -database campusgo_verification
CAMPUSGO_D2_INTEGRATION=1 go test -race -count=1 -p 1 -v ./internal/errand ./internal/transport/httpapi
./bin/reconcile -database campusgo_verification
printf '%s\n' 'D2 MySQL, Redis, HTTP and COMMIT acknowledgment-loss checks passed.'
