.PHONY: setup run build test lint fmt check up down logs migrate migrate-down transcribe media-image agent-run test-integration kb-ingest kb-search kb-delete kb-reembed

GOLANGCI_LINT := ./scripts/golangci-lint.sh

# Commands that run on the host against the compose postgres (published on 127.0.0.1).
HOST_DB_ENV = set -a && [ -f .env ] && . ./.env; set +a && POSTGRES_HOST=127.0.0.1 POSTGRES_PORT=$${POSTGRES_HOST_PORT:-5432}

setup:
	git config core.hooksPath .githooks

# Runs the app on the host; see README ("Запуск на хосте"): hosts from .env are overridden in .env.local.
run:
	set -a; [ -f .env ] && . ./.env; [ -f .env.local ] && . ./.env.local; set +a; go run .

build:
	go build -o bin/app .

test:
	go test -race ./...

# Integration tests (names contain "Integration", see internal/pgtest) against PostgreSQL from compose,
# published on 127.0.0.1:POSTGRES_HOST_PORT and migrated by the migrate service: make up && make test-integration
test-integration:
	set -a; . ./.env; set +a; \
	POSTGRES_HOST=127.0.0.1 POSTGRES_PORT=$${POSTGRES_HOST_PORT:-5432} INTEGRATION=1 \
	go test -race -count=1 -run Integration ./...

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
#   make agent-run AGENT=copywriter PROMPT="Привет!"
# The image is built by the migrate service, which also applies migrations (usage is recorded to llm_usage).
agent-run:
	docker compose run --rm --build migrate up
	docker compose run --rm --entrypoint /app/agent-run app -agent "$(AGENT)" -prompt "$(PROMPT)"

# Knowledge base CLI (needs `make up` and OPENAI_API_KEY in .env):
#   make kb-ingest KB_PATH=./brand KB_KIND=brand
#   make kb-search Q="как мы говорим о питании" LIMIT=5
#   make kb-delete SOURCE=voice.md
#   make kb-reembed
kb-ingest:
	$(HOST_DB_ENV) go run ./cmd/kb ingest "$(KB_PATH)" -kind "$(or $(KB_KIND),other)"

kb-search:
	$(HOST_DB_ENV) go run ./cmd/kb search "$(Q)" -limit "$(or $(LIMIT),5)"

kb-delete:
	$(HOST_DB_ENV) go run ./cmd/kb delete "$(SOURCE)"

kb-reembed:
	$(HOST_DB_ENV) go run ./cmd/kb reembed
