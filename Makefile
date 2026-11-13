.PHONY: setup run build test lint fmt check up down logs migrate

GOLANGCI_LINT := ./scripts/golangci-lint.sh

setup:
	git config core.hooksPath .githooks

run:
	set -a && [ -f .env ] && . ./.env; set +a && go run .

build:
	go build -o bin/app .

test:
	go test ./...

lint:
	$(GOLANGCI_LINT) run ./...

fmt:
	$(GOLANGCI_LINT) fmt ./...

check: lint test

up:
	docker compose up -d --build --wait

down:
	docker compose down

logs:
	docker compose logs -f

migrate:
	docker compose run --rm --build --entrypoint /app/migrate app
