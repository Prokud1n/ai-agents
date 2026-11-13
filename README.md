# ai-agents

Команда ИИ-агентов, которая ведёт Instagram фитнес-тренера; тренер общается с агентами в Telegram.

## Локальный запуск

Нужны Docker и Docker Compose v2.

```bash
cp .env.dist .env   # безопасные dev-значения по умолчанию
make up             # собрать и поднять стек, дождаться healthcheck
curl 127.0.0.1:8080/readyz
make logs           # логи
make down           # остановить (данные в volume сохраняются; `docker compose down -v` — удалить)
```

Сервисы: `app`, `migrate` (одноразовый: применяет миграции до старта `app`), `postgres` (pgvector), `redis` (с паролем),
`s3` ([RustFS](https://rustfs.com) — S3-совместимое хранилище). Клиент в приложении — minio-go, работает с любым S3.
Бакет для медиа (`S3_BUCKET`) приложение создаёт само при старте.

Наружу опубликованы только `app` (`127.0.0.1:${APP_HOST_PORT}`) и `postgres` (`127.0.0.1:${POSTGRES_HOST_PORT}`, для отладки).
Redis, S3 и Bot API доступны только внутри сети compose, поэтому приложение запускается в compose, а не на хосте.
Консоль RustFS, если нужна: `docker compose exec s3 ...` или временный `docker-compose.override.yml` с портом на `127.0.0.1`.

- `/healthz` — liveness: процесс жив.
- `/readyz` — readiness: статусы (`ok`/`fail`) PostgreSQL, Redis и S3; `503`, если хоть одна зависимость недоступна.
  Текст ошибок — только в логе.

### Миграции

Файлы — `migrations/NNN_описание.up.sql` + `NNN_описание.down.sql` (golang-migrate). В compose их применяет сервис `migrate`.

```bash
make migrate        # применить все новые
make migrate-down   # откатить одну
```

Запуск миграций при старте приложения (`MIGRATIONS_RUN_ON_START=true`) по умолчанию выключен.

### Профиль `telegram`

Локальный сервер Telegram Bot API нужен, чтобы боты могли скачивать файлы до 2 ГБ (публичный API отдаёт ботам только до 20 МБ).
Он требует `TELEGRAM_API_ID` и `TELEGRAM_API_HASH` — их выдают на https://my.telegram.org (API development tools).
Заполни их в `.env` и подними:

```bash
docker compose --profile telegram up -d
```

Приложение обращается к нему по `TELEGRAM_BOT_API_URL`; том с файлами Bot API смонтирован в `app` только на чтение по тому же пути
(`/var/lib/telegram-bot-api`), так что локальный путь из `getFile` можно читать напрямую.

## Разработка

Правила — в [AGENTS.md](AGENTS.md) и [CODE_STYLE.md](CODE_STYLE.md). Проверки: `make check` (lint + `go test -race`).

CI (`.github/workflows/ci.yml`): job `tidy` делает `go mod tidy` и на ветках `feature/**` сам коммитит `go.mod`/`go.sum`
(после этого сделай `git pull`), затем `check` и `compose-smoke` проверяют итоговый коммит.
