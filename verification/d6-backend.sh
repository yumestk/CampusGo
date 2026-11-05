#!/bin/sh
set -eu
cd "$(dirname "$0")/.."
mkdir -p work
docker compose --env-file .env -f deploy/local/compose.yaml up -d --wait mysql redis
for db in campusgo campusgo_verification; do ./bin/migrate -database "$db" up; ./bin/seed -database "$db"; done
CAMPUSGO_INTEGRATION=1 go test -race -count=1 -p 1 -run 'TestIntegrationJWTLifecycle|TestIntegrationSessionLifecycle|TestIntegrationOIDCSecurity|TestIntegrationD6' -v ./internal/auth ./internal/transport/httpapi
./bin/reconcile -database campusgo_verification
./bin/reconcile
