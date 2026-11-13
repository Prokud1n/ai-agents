.PHONY: setup run build test lint fmt check

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
