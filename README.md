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
Redis, S3 и Bot API доступны только внутри сети compose, поэтому по умолчанию приложение запускается в compose
(запуск на хосте — ниже).
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

Приложение миграции не применяет — только `cmd/migrate` (сервис `migrate`). Он работает без таймаута старта:
по SIGINT/SIGTERM дожидается конца текущей миграции и выходит с ошибкой, так что база не остаётся `dirty`.

### Запуск на хосте (`make run`)

Удобно для отладки. Зависимости остаются в compose, а Redis и S3 нужно временно опубликовать на `127.0.0.1`:

```bash
cat > docker-compose.override.yml <<'YAML'   # в .gitignore; compose подхватывает его сам
services:
  redis:
    ports: ["127.0.0.1:6379:6379"]
  s3:
    ports: ["127.0.0.1:9000:9000"]
YAML

cat > .env.local <<'ENV'                      # в .gitignore; make run читает его после .env
POSTGRES_HOST=localhost
REDIS_ADDRESS=localhost:6379
S3_ENDPOINT=localhost:9000
HTTP_ADDRESS=127.0.0.1:8082
ENV

docker compose up -d --wait postgres redis s3
make migrate                                  # миграции применяются в compose
make run
```

`POSTGRES_PORT` в `.env.local` нужен, только если `POSTGRES_HOST_PORT` не 5432. `HTTP_ADDRESS` — порт, не занятый `app` из compose (8080)
и Bot API (8081), чтобы всё могло работать одновременно. Bot API на хосте недоступен: для него опубликуй порт 8081 так же
и задай `TELEGRAM_BOT_API_URL=http://localhost:8081`, либо используй публичный API.
Когда override не нужен, удали его: иначе `make up` тоже опубликует Redis и S3.

### Профиль `telegram`

Локальный сервер Telegram Bot API нужен, чтобы боты могли скачивать файлы до 2 ГБ (публичный API отдаёт ботам только до 20 МБ).
Он требует `TELEGRAM_API_ID` и `TELEGRAM_API_HASH` — их выдают на https://my.telegram.org (API development tools).
Заполни их в `.env` и подними:

```bash
docker compose --profile telegram up -d
```

Приложение обращается к нему по `TELEGRAM_BOT_API_URL`; том с файлами Bot API смонтирован в `app` только на чтение по тому же пути
(`/var/lib/telegram-bot-api`), так что локальный путь из `getFile` можно читать напрямую.

## Транскрипция видео

`transcribe` извлекает речь из видео (ffmpeg) и отдаёт JSON с текстом, сегментами и таймкодами слов (Whisper `whisper-1`).
Нужны `ffmpeg`/`ffprobe` и `OPENAI_API_KEY` в `.env`:

```bash
make transcribe ARGS="-in video.mp4 -out result.json"   # -lang ru по умолчанию (WHISPER_LANGUAGE)
```

В образе с ffmpeg: текущая папка монтируется в `/work` (рабочая папка образа). Пути `-in` и `-out` — относительно неё,
`--user` нужен, чтобы `result.json` на хосте принадлежал вам и был доступен для записи:

```bash
make media-image
docker run --rm --env-file .env --user "$(id -u):$(id -g)" -v "$PWD:/work" ai-agents-media:local \
  -in video.mp4 -out result.json
```

Если в роликах фоновая музыка и паузы не находятся, подними `TRANSCRIPTION_SILENCE_NOISE_DB` (например, до `-25`).

## Агенты и LLM

- **Агент** — папка `agents/<name>/` с файлом `agent.md`: YAML frontmatter (`name` = имя папки, `display_name`,
  `model`, `tools`, `skills`, `skill_overrides`) и тело — системный промпт роли.
- **Общие скиллы** — `skills/<skill>/SKILL.md` (`SKILLS_DIR`): frontmatter `name`, `description` и тело — инструкция.
  Агент подключает скилл по имени в `skills:`, один скилл может использоваться несколькими агентами.
- **Доработка скилла под агента** — `skill_overrides: {<skill>: "<инструкция>"}` в `agent.md`. Когда агент
  вызывает `load_skill`, текст дописывается к общему SKILL.md. Override на скилл не из `skills:` — ошибка старта.
- **Локальные скиллы** (необязательно) — `agents/<name>/skills/<skill>/SKILL.md`. Локальный скилл с именем
  общего — ошибка старта.
- Runtime добавляет к роли каждого агента общие правила проекта (`agents.SafetyRules`) и список его скиллов.

Агенты и скиллы проверяются при старте: ошибка — приложение не стартует, текст с путями к файлам в логе.
Папки с префиксом `_` — шаблоны, они не загружаются. Шаблон — `agents/_example/`: скопируй его в
`agents/<name>/` и поменяй `name`. Новый инструмент регистрируется модулем в fx-группе `agents.ToolsGroup`.

Ручная проверка с настоящим ключом (`ANTHROPIC_API_KEY` в `.env`): ответ агента и расход токенов и денег.

```bash
make agent-run AGENT=<name> PROMPT="Привет! Представься."
```

## Разработка

Правила — в [AGENTS.md](AGENTS.md) и [CODE_STYLE.md](CODE_STYLE.md). Проверки: `make check` (lint + `go test -race`).

CI (`.github/workflows/ci.yml`): job `tidy` делает `go mod tidy` и на ветках `feature/**`, `fix/**`, `refactoring/**`
сам коммитит `go.mod`/`go.sum` (после этого сделай `git pull`), затем `check` и `compose-smoke` проверяют итоговый коммит.
`check` требует, чтобы ветка содержала актуальный `main`: перед мержем сделай rebase. Новые миграции ветки
должны иметь номер больше последней миграции в `main` (golang-migrate молча пропускает меньший номер).

### Интеграционные тесты

Тесты с `Integration` в имени работают с PostgreSQL из compose (миграции накатывает сервис `migrate`) и
подключаются через `internal/pgtest`. Без `INTEGRATION=1` они пропускаются, поэтому `make test` базы не требует.

```bash
make up && make test-integration
```

В CI они идут в job `compose-smoke`. Каждый пакет работает только со своими таблицами и чистит их через
`pgtest.Truncate`; тесты, которые делят таблицу, не вызывают `t.Parallel`.
