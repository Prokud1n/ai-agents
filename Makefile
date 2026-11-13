.PHONY: setup run build test lint fmt check up down logs migrate migrate-down

GOLANGCI_LINT := ./scripts/golangci-lint.sh

setup:
	git config core.hooksPath .githooks

# Runs the app on the host; see README ("Запуск на хосте"): hosts from .env are overridden in .env.local.
run:
	set -a; [ -f .env ] && . ./.env; [ -f .env.local ] && . ./.env.local; set +a; go run .

build:
	go build -o bin/app .

test:
	go test -race ./...

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
	docker compose run --rm --build migrate up

migrate-down:
	docker compose run --rm --build migrate down
