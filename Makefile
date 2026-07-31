.PHONY: run worker migrate test build

run:
	go run ./cmd/api

worker:
	go run ./cmd/worker

migrate:
	go run ./cmd/migrate

test:
	go test ./...

build:
	go build ./cmd/api ./cmd/worker ./cmd/migrate
