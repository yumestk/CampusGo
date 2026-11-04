.PHONY: build check test run-api run-worker env infra-up infra-down verify-d0

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
	docker compose --env-file .env -f deploy/local/compose.yaml up -d --wait
	docker compose --env-file .env -f deploy/local/compose.yaml wait rmq-init

infra-down:
	docker compose --env-file .env -f deploy/local/compose.yaml --profile gateway down

verify-d0: env build
	./scripts/with-local-env.sh ./verification/d0.sh

run-api:
	go run ./cmd/api

run-worker:
	go run ./cmd/worker
