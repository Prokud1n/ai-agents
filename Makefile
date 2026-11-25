.PHONY: setup run build test lint fmt check up down logs migrate migrate-down transcribe roughcut subtitles media-image agent-run edit test-integration redis-test-up redis-test-down s3-test-up s3-test-down kb-ingest kb-search kb-delete kb-reembed

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
# published on 127.0.0.1:POSTGRES_HOST_PORT; each test gets its own migrated database: make up && make test-integration
# The verbose output goes to the log as it runs (tee) and is the proof that the tests ran: with INTEGRATION=1
# a skipped test, or no integration test passed at all, fails the target. bash + pipefail keeps the exit
# status of go test through the pipe.
# Redis integration tests (internal/redistest) use a throwaway Redis started by redis-test-up, S3 tests
# (internal/s3test) a throwaway RustFS started by s3-test-up (the compose Redis and S3 are not published to the
# host); both are removed when the target ends, whatever the result.
test-integration: SHELL := /bin/bash
test-integration: .SHELLFLAGS := -o pipefail -c
test-integration: redis-test-up s3-test-up
	@log=$$(mktemp); trap 'rm -f "$$log"; docker rm -f -v $(REDIS_TEST_CONTAINER) $(S3_TEST_CONTAINER) >/dev/null 2>&1 || true' EXIT; \
	$(HOST_DB_ENV) REDIS_TEST_ADDRESS=127.0.0.1:$(REDIS_TEST_HOST_PORT) \
	S3_TEST_ENDPOINT=127.0.0.1:$(S3_TEST_HOST_PORT) S3_TEST_ACCESS_KEY=$(S3_TEST_ACCESS_KEY) S3_TEST_SECRET_KEY=$(S3_TEST_SECRET_KEY) \
	INTEGRATION=1 go test -race -count=1 -v -run Integration ./... 2>&1 | tee "$$log"; \
	status=$$?; \
	if grep -q -e '--- SKIP' "$$log"; then \
		echo "test-integration: tests were skipped with INTEGRATION=1"; status=1; \
	elif ! grep -q -e '--- PASS: TestIntegration' "$$log"; then \
		echo "test-integration: no integration test passed"; status=1; \
	fi; \
	exit $$status

# Throwaway Redis for integration tests: the image of the compose redis service, no volume, no persistence,
# published on 127.0.0.1 only and outside the compose project (the stack itself publishes only app and postgres).
REDIS_TEST_CONTAINER := ai-agents-redis-test
REDIS_TEST_HOST_PORT ?= 6380
REDIS_TEST_IMAGE = $(shell sed -n 's/^ *image: \(redis:[^ ]*\)$$/\1/p' docker-compose.yml)

redis-test-up:
	@if [ -z "$(REDIS_TEST_IMAGE)" ]; then \
		echo "redis-test: no redis image found in docker-compose.yml (service redis, line 'image: redis:...')"; exit 1; \
	fi
	@if [ -z "$$(docker ps -q --filter 'name=^$(REDIS_TEST_CONTAINER)$$')" ]; then \
		docker run -d --rm --name $(REDIS_TEST_CONTAINER) -p 127.0.0.1:$(REDIS_TEST_HOST_PORT):6379 \
			$(REDIS_TEST_IMAGE) redis-server --save "" --appendonly no >/dev/null; \
	fi
	@for i in $$(seq 1 30); do \
		docker exec $(REDIS_TEST_CONTAINER) redis-cli ping 2>/dev/null | grep -q PONG && exit 0; sleep 1; \
	done; echo "redis-test: not ready"; docker rm -f $(REDIS_TEST_CONTAINER) >/dev/null 2>&1 || true; exit 1

redis-test-down:
	@docker rm -f $(REDIS_TEST_CONTAINER) >/dev/null 2>&1 || true

# Throwaway S3 (RustFS) for integration tests: the image of the compose s3 service (with its digest), an anonymous
# volume removed with the container, test-only keys, published on 127.0.0.1 only and outside the compose project.
S3_TEST_CONTAINER := ai-agents-s3-test
S3_TEST_HOST_PORT ?= 9100
S3_TEST_ACCESS_KEY := s3test-access
S3_TEST_SECRET_KEY := s3test-secret-key
S3_TEST_IMAGE = $(shell sed -n 's/^ *image: \(rustfs\/rustfs:[^ ]*\)$$/\1/p' docker-compose.yml)

s3-test-up:
	@if [ -z "$(S3_TEST_IMAGE)" ]; then \
		echo "s3-test: no rustfs image found in docker-compose.yml (service s3, line 'image: rustfs/rustfs:...')"; exit 1; \
	fi
	@if [ -z "$$(docker ps -q --filter 'name=^$(S3_TEST_CONTAINER)$$')" ]; then \
		docker run -d --rm --name $(S3_TEST_CONTAINER) -p 127.0.0.1:$(S3_TEST_HOST_PORT):9000 -v /data \
			-e RUSTFS_ACCESS_KEY=$(S3_TEST_ACCESS_KEY) -e RUSTFS_SECRET_KEY=$(S3_TEST_SECRET_KEY) \
			-e RUSTFS_ADDRESS=0.0.0.0:9000 -e RUSTFS_CONSOLE_ENABLE=false \
			$(S3_TEST_IMAGE) >/dev/null; \
	fi
	@for i in $$(seq 1 60); do \
		curl -fsS http://127.0.0.1:$(S3_TEST_HOST_PORT)/health >/dev/null 2>&1 && exit 0; sleep 1; \
	done; echo "s3-test: not ready"; docker logs $(S3_TEST_CONTAINER) 2>&1 | tail -n 20; \
		docker rm -f -v $(S3_TEST_CONTAINER) >/dev/null 2>&1 || true; exit 1

s3-test-down:
	@docker rm -f -v $(S3_TEST_CONTAINER) >/dev/null 2>&1 || true

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

# make roughcut ARGS="-in video.mp4 [-transcript transcript.json] [-out roughcut] [-plan-only] [-plan plan.json]"
# Needs ffmpeg; without -transcript it transcribes the video first (OPENAI_API_KEY in .env). See README.
roughcut:
	set -a; [ -f .env ] && . ./.env; [ -f .env.local ] && . ./.env.local; set +a; go run ./cmd/roughcut $(ARGS)

# make subtitles ARGS="-in video.mp4 -transcript result.json -out reel.mp4 [-style style.yaml] [-accents a,b] [-karaoke]"
# make subtitles ARGS="-transcript result.json -out subtitles.ass -ass-only"; rendering needs ffmpeg with libass
subtitles:
	set -a; [ -f .env ] && . ./.env; [ -f .env.local ] && . ./.env.local; set +a; go run ./cmd/subtitles $(ARGS)

media-image:
	docker build --target media -t ai-agents-media:local .

# Run one agent with a prompt inside compose (needs ANTHROPIC_API_KEY in .env):
#   make agent-run AGENT=<name> PROMPT="Привет!"
# Agents: marketer, copywriter, editor, designer (see README, "Агенты и LLM").
# The image is built by the migrate service, which also applies migrations (usage is recorded to llm_usage).
agent-run:
	docker compose run --rm --build migrate up
	docker compose run --rm --entrypoint /app/agent-run app -agent "$(AGENT)" -prompt "$(PROMPT)"

# The editor (Монтажёр) without Telegram on a local video: the app's edit (the model, its decisions and revisions) with
# the running media worker, nothing sent to the group, no task queued (README, «Проверка Монтажёра без Telegram»).
# Needs `make up` and ANTHROPIC_API_KEY, OPENAI_API_KEY in .env. FILE, CAPTION, OUT and REVISION reach scripts/edit.sh
# through the environment (make exports command line variables), so quotes in the texts are kept.
#   make edit FILE=/path/to/video.mov [CAPTION="подпись"] [OUT=./edits/<name>]   version 1 into OUT/v1
#   make edit OUT=./edits/<name> REVISION="сделай 40 секунд"                       a revision of the last version
edit:
	@./scripts/edit.sh

# Knowledge base CLI (needs `make up` and OPENAI_API_KEY in .env). Runs /app/kb inside the app image through
# docker compose: no Go on the host. ./kb is mounted read-only at /kb (KB_ROOT there); KB_PATH is given as on the
# host and must be inside ./kb. KB_HOST=1 runs `go run ./cmd/kb` on the host instead (KB_ROOT from .env).
# See scripts/kb.sh.
#   make kb-ingest KB_PATH=./kb/brand KB_KIND=brand        (without KB_PATH: all of ./kb)
#   make kb-search Q="как мы говорим о питании" LIMIT=5   (flags first, then --: Q="-5 кг" works)
#   make kb-delete SOURCE=brand/voice.md
#   make kb-reembed
KB := ./scripts/kb.sh

kb-ingest:
	$(KB) ingest "$(or $(KB_PATH),./kb)" $(if $(KB_KIND),-kind "$(KB_KIND)") $(if $(KB_FORCE),-force)

kb-search:
	$(KB) search -limit "$(or $(LIMIT),5)" -- "$(Q)"

kb-delete:
	$(KB) delete -- "$(SOURCE)"

kb-reembed:
	$(KB) reembed

# M0: end-to-end check of the environment and every brick with real keys (docs/SETUP.md, "Проверка M0").
# Steps without a key are skipped; the summary goes to m0-results/report.md:
#   make m0-check [M0_VIDEO=./clip.mp4]
# Optional M0_BOT_TOKEN (test bot only, never in .env) reaches the script through the environment, not this recipe,
# so make does not echo it: M0_BOT_TOKEN=... make m0-check
.PHONY: m0-check
m0-check:
	M0_VIDEO="$(M0_VIDEO)" ./scripts/m0-check.sh
