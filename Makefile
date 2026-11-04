.PHONY: build check test run-api run-worker

build:
	go build -o bin/api ./cmd/api
	go build -o bin/worker ./cmd/worker

check:
	test -z "$$(gofmt -l cmd internal)"
	go vet ./...
	go test -race ./...

test:
	go test ./...

run-api:
	go run ./cmd/api

run-worker:
	go run ./cmd/worker
