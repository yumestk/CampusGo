.PHONY: build check test run-api run-worker env infra-up infra-down verify-d0 verify-d1 verify-d2 verify-d3 verify-d4 migrate seed reconcile

build:
	go build -o bin/ ./cmd/...

check:
	test -z "$$(gofmt -l cmd internal)"
	go vet ./...
	go test -race ./...
	go test -race github.com/apache/rocketmq-clients/golang/v5 -run TestMetricProviderConcurrentReset

test:
	go test ./...

env:
	go run ./cmd/devsetup

infra-up: env
	docker compose --env-file .env -f deploy/local/compose.yaml --profile identity up -d --wait
	docker compose --env-file .env -f deploy/local/compose.yaml wait rmq-init

infra-down:
	docker compose --env-file .env -f deploy/local/compose.yaml --profile gateway --profile identity down

verify-d0: env build
	./scripts/with-local-env.sh ./verification/d0.sh

run-api:
	./scripts/with-local-env.sh go run ./cmd/api

run-worker:
	./scripts/with-local-env.sh go run ./cmd/worker

migrate: env build
	./scripts/with-local-env.sh ./bin/migrate up

seed: env build
	./scripts/with-local-env.sh ./bin/seed

verify-d1: env build
	./scripts/with-local-env.sh ./verification/d1.sh

verify-d2: env build
	./scripts/with-local-env.sh ./verification/d2.sh

reconcile: env build
	./scripts/with-local-env.sh ./bin/reconcile

verify-d3: env build
	./scripts/with-local-env.sh ./verification/d3.sh

verify-d4: env build
	./scripts/with-local-env.sh ./verification/d4.sh
