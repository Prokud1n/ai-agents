.PHONY: setup run build test lint fmt check up down logs migrate migrate-down transcribe media-image agent-run

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

# make transcribe ARGS="-in video.mp4 [-lang ru] [-out result.json]"; needs ffmpeg and OPENAI_API_KEY (.env)
transcribe:
	set -a; [ -f .env ] && . ./.env; [ -f .env.local ] && . ./.env.local; set +a; go run ./cmd/transcribe $(ARGS)

media-image:
	docker build --target media -t ai-agents-media:local .

# Run one agent with a prompt inside compose (needs ANTHROPIC_API_KEY in .env):
#   make agent-run AGENT=_example PROMPT="Привет!"
# The image is built by the migrate service, which also applies migrations (usage is recorded to llm_usage).
agent-run:
	docker compose run --rm --build migrate up
	docker compose run --rm --entrypoint /app/agent-run app -agent "$(AGENT)" -prompt "$(PROMPT)"
