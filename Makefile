.PHONY: test check build run migrate replay run-scheduling run-followup
test:
	go test -race ./...
check:
	go vet ./...
build:
	go build -o bin/bia ./cmd/bia
run:
	go run ./cmd/bia
migrate:
	go run ./cmd/bia -migrate
replay:
	go run ./cmd/replay -input examples/replay.jsonl

run-scheduling:
	go run ./cmd/bia -role scheduling
run-followup:
	go run ./cmd/bia -role followup
