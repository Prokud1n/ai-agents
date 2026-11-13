.PHONY: setup build test lint fmt check

setup:
	git config core.hooksPath .githooks

build:
	go build -o bin/app .

test:
	go test ./...

lint:
	golangci-lint run ./...

fmt:
	golangci-lint fmt ./...

check: lint test
