# ai-agents

Команда ИИ-агентов, которая ведёт Instagram фитнес-тренера; тренер общается с агентами в Telegram.

## Локальный запуск

Нужны Docker и Docker Compose v2.

```bash
cp .env.dist .env   # безопасные dev-значения по умолчанию
make up             # собрать и поднять стек, дождаться healthcheck
curl localhost:8080/readyz
make logs           # логи
make down           # остановить (данные в volume сохраняются; `docker compose down -v` — удалить)
```

Стек: `app`, `postgres` (pgvector), `redis`, `minio` (S3, консоль — http://localhost:9001, логин/пароль — `S3_ACCESS_KEY`/`S3_SECRET_KEY`).
Бакет для медиа (`S3_BUCKET`) приложение создаёт само при старте.

- `/healthz` — liveness: процесс жив.
- `/readyz` — readiness: статус PostgreSQL, Redis и S3 в JSON; `503`, если хоть одна зависимость недоступна.

Миграции лежат в `migrations/` (`NNN-описание.up.sql`) и применяются при старте приложения (`MIGRATIONS_RUN_ON_START=true`).
Вручную: `make migrate`.

Запуск приложения без Docker (`make run`) использует те же `.env` с адресами `localhost` — зависимости можно поднять через `docker compose up -d postgres redis minio`.

### Профиль `telegram`

Локальный сервер Telegram Bot API нужен, чтобы боты могли скачивать файлы до 2 ГБ (публичный API отдаёт ботам только до 20 МБ).
Он требует `TELEGRAM_API_ID` и `TELEGRAM_API_HASH` — их выдают на https://my.telegram.org (API development tools).
Заполни их в `.env` и подними:

```bash
docker compose --profile telegram up -d
```

## Разработка

Правила — в [AGENTS.md](AGENTS.md) и [CODE_STYLE.md](CODE_STYLE.md). Проверки: `make check` (lint + test).
