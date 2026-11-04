.PHONY: build check test run-api run-worker env infra-up infra-down verify-d0 migrate seed

build:
	go build -o bin/ ./cmd/...

check:
	test -z "$$(gofmt -l cmd internal)"
	go vet ./...
	go test -race ./...

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
	go run ./cmd/worker

migrate: env build
	./scripts/with-local-env.sh ./bin/migrate up

seed: env build
	./scripts/with-local-env.sh ./bin/seed
