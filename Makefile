.PHONY: run test build

run:
	PM_DATA_FILE=./data/project-management.json go run ./cmd/api

test:
	go test ./...

build:
	go build ./cmd/api
