.PHONY: build run db-up db-down tidy test vet install

build:
	CGO_ENABLED=1 go build -o bin/kenosm ./cmd/kenosm
	go build -o bin/kenosm-server ./cmd/server

run:
	go run ./cmd/kenosm

install: build
	mkdir -p $(HOME)/.local/bin
	cp bin/kenosm $(HOME)/.local/bin/kenosm
	@echo "installed to $(HOME)/.local/bin/kenosm (make sure it is on your PATH)"

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
