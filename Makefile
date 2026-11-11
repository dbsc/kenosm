.PHONY: build run db-up db-down tidy test vet

build:
	go build -o bin/kenosm ./cmd/server

run:
	go run ./cmd/server

db-up:
	docker compose up -d db

db-down:
	docker compose down

tidy:
	go mod tidy

vet:
	go vet ./...

test:
	go test ./...
