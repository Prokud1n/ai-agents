# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

Общие правила для агентов и код-стайл подключаются целиком:

@AGENTS.md
@CODE_STYLE.md

## Хуки Claude Code

В `.claude/settings.json` настроены хуки, которые следят за код-стайлом автоматически:

- после каждой правки `.go`-файла он форматируется (`golangci-lint fmt`);
- перед завершением ответа, если менялись `.go`-файлы, запускается `golangci-lint run ./...`. Если есть ошибки — исправь их, не отключая линтер.

## Что это за проект

Команда ИИ-агентов (Маркетолог, Копирайтер, Монтажёр, Дизайнер), которая ведёт Instagram фитнес-тренера;
тренер общается с агентами в Telegram. Go-сервис + docker compose (`app`, одноразовый `migrate`, `postgres` с pgvector,
`redis`, `s3` на RustFS, `telegram-bot-api` под профилем `telegram`). Запуск локально и на хосте — в README.md.

## Команды сверх AGENTS.md

```bash
go test -race -run TestName ./internal/agents/          # один тест
make up && make test-integration                        # интеграционные тесты против compose-postgres
FFMPEG_TESTS_REQUIRED=1 go test -run TestRunner ./internal/providers/ffmpeg/  # без ffmpeg тесты иначе скипаются
make agent-run AGENT=<name> PROMPT="..."                # прогнать одного агента в compose (нужен ANTHROPIC_API_KEY)
make kb-ingest KB_PATH=./kb/brand KB_KIND=brand         # база знаний в образе (KB_HOST=1 — go run): также kb-search / kb-delete / kb-reembed
make transcribe ARGS="-in video.mp4"                    # нужен ffmpeg и OPENAI_API_KEY
make subtitles ARGS="-in video.mp4 -transcript result.json -out reel.mp4"  # субтитры; нужен ffmpeg с libass
FFMPEG_TESTS_REQUIRED=1 go test -run TestBurnSubtitlesWithFFmpeg ./internal/subtitles/  # рендер субтитров
```

- `make lint`/`make fmt` идут через `scripts/golangci-lint.sh` (запиненная версия), а не через глобальный бинарь.
- Интеграционный тест: в имени есть `Integration`, первой строкой вызывает `pgtest.Connect(t)`. Без `INTEGRATION=1`
  он скипается; с ним получает отдельную свежую БД со всеми миграциями (можно `t.Parallel()`).

## Архитектура

### Несколько бинарей — один fx-граф по частям

`internal/` собирает наборы fx-модулей под каждую точку входа:

| Бинарь | Сборка | Что внутри |
|---|---|---|
| `main.go` (сервис) | `internal/app.go: DeclareApplicationOptions` | все провайдеры + health/readiness + agents + gateway + tasks + teamchat + HTTP-сервер (последним) |
| `cmd/agent-run` | `DeclareAgentRunOptions` | postgres + anthropic + embeddings + llmusage + knowledge + kbtool + agents, без HTTP |
| `cmd/kb` | `internal/kb.go: DeclareKBOptions` / `RunKB` | postgres + embeddings + knowledge + kbcli |
| `cmd/transcribe` | `internal/transcribe.go` | ffmpeg + whisper + transcription; логи в stderr, JSON в stdout |
| `cmd/roughcut` | `internal/roughcut.go` | ffmpeg + whisper + transcription + roughcut; логи в stderr, файлы в `-out` |
| `cmd/subtitles` | `internal/subtitles.go` | ffmpeg + subtitles (ASS через libass, шрифты `assets/fonts`); логи в stderr; код выхода 2 — подмена шрифта |
| `cmd/migrate` | вне fx (`internal/migrator`) | — |

Долгие CLI-команды (`kb`, `transcribe`, `roughcut`, `subtitles`) выполняются **между** `app.Start` и `app.Stop` или вовсе без `Start`, а не в
start-хуке, чтобы их не ограничивал `APP_START_TIMEOUT`. Новую команду строй так же. Таймауты старта и остановки
читаются заранее (`config.ParseLifecycle`), потому что это опции верхнего уровня fx.

Каждая сборка парсит только нужный ей конфиг (`config.Parse`, `ParseTranscribe`, `ParseSubtitlesCommand`, `ParseMigrate`, …). Не добавляй
обязательные переменные в общий конфиг, если CLI без них должен работать.

### Агенты — данные, а не код

- `agents/<name>/agent.md`: YAML-frontmatter (`name` = имя папки, `display_name`, `model`, `tools`, `skills`,
  `skill_overrides`) и тело — role prompt. Шаблон: `agents/_example/`. Папки с `_` и `.` не загружаются.
- Скиллы: общие — `skills/<skill>/SKILL.md` (`SKILLS_DIR`), локальные — `agents/<name>/skills/<skill>/SKILL.md`.
  Локальный скилл с именем общего — ошибка старта. В системный промпт попадают только имя и описание скилла,
  тело модель получает встроенным инструментом `load_skill` (его нельзя перечислять в `tools`).
- `internal/agents.Registry` загружает и валидирует всё при старте приложения. Сломанный `agents/` валит старт,
  все ошибки собираются сразу и снабжаются путями к файлам.
- Системный промпт = role + `SafetyRules` (`internal/agents/prompt.go`, единственное место, где они сформулированы) +
  список скиллов.
- `agents.Runtime.Run` — цикл tool use поверх Claude API (`internal/providers/anthropic`): ограничен
  `maxToolIterations`. Ошибка инструмента возвращается модели как `tool_result` с `is_error` и не прерывает прогон.
  При `max_tokens`/`refusal` возвращается частичный `Result` вместе с ошибкой, его `Messages` можно передать в следующий прогон.
- Новый инструмент: реализуй `agents.Tool` и зарегистрируй конструктор в своём модуле с
  `fx.As(new(agents.Tool))` и `fx.ResultTags(agents.ToolsGroup)`. Runtime править не нужно.
  Пример — поиск по базе знаний `search_knowledge_base` (`internal/knowledge/kbtool`, отдельный пакет, чтобы
  `knowledge` и `cmd/kb` не зависели от `agents`). Инструмент, который может быть недоступен (нет ключа), реализует
  `Available() error`: он остаётся зарегистрированным, а реестр предупреждает в логе об агентах, которые его используют.

### Учёт расходов LLM

Каждый вызов API пишется в `llm_usage` (`internal/llmusage`) даже при отменённом контексте, потому что вызов уже
оплачен; ошибка записи только логируется. Цены задаются строкой `LLM_PRICES`
(`model=input/output/cache_write/cache_read`, USD за 1M токенов) и хранятся целыми нано-долларами, без float.
У модели без цены токены считаются, а стоимость — `NULL`.

### Telegram-шлюз и задачи

- `internal/gateway` — четыре бота в одном сервисе: события группы → `gateway.Handler` (быстрый, идемпотентный,
  at-most-once), отправка от имени роли — `gateway.Sender`. Без `TELEGRAM_*_TOKEN` шлюз выключен.
- `internal/tasks` — задачи с согласованием: очередь Redis Streams, статусы в Postgres, черновик → `tasks.Notifier`.
- `internal/teamchat` связывает их: `Handler` (события → задачи, кнопки, реплай-правки, команды) подменяет
  `gateway.AckHandler` через `fx.Decorate` **на верхнем уровне** опций (`teamchat.Module` — `fx.Options`, а не
  `fx.Module`: декоратор внутри модуля не дошёл бы до `gateway.Module`); `Notifier` шлёт черновики от бота агента.
  Без токенов `tasks.Notifier` — `tasks.LogNotifier`. Имя агента = роль бота (`agents/marketer`, `agents/copywriter`);
  за Монтажёра и Дизайнера пока отвечает `teamchat` фиксированным текстом (`stubTexts`), агентов для них нет.
  Из медиа `teamchat` принимает только видео для Монтажёра (`gateway.MediaFilter` разрешает шлюзу скачать только его):
  файл сразу копируется в S3 через `internal/media` (ключ `<MEDIA_KEY_PREFIX>/<chat>/<message>/<file_unique_id>.<ext>`,
  обратно — `media.Store.Download`), Монтажёр отвечает заглушкой, задачи нет. Голосовые и видеокружки принципиально
  не обрабатываются (тренер диктует текст), фото и аудио — пока; на всё это честный ответ без скачивания.
- Порядок остановки fx — обратный порядку регистрации хуков. Хук ожидания фоновой работы регистрируй в конструкторе
  того, кто её запускает (`tasks.provideService`, `teamchat.newHandler`): потребители (шлюз, воркеры) создаются позже
  и останавливаются раньше. Проверка — `teamchat.TestIntegrationStopOrder`.

### База знаний

`internal/knowledge`: извлечение текста (`extract/`: md/txt/docx/pdf с защитой от вредоносных PDF, фикстуры в `testdata/`)
→ чанкинг (`chunk/`) → эмбеддинги OpenAI (`providers/embeddings`) → pgvector. Загрузка и управление — через CLI `cmd/kb`,
поиск для агентов — инструмент `search_knowledge_base` (`knowledge/kbtool`).

### CI (`.github/workflows/ci.yml`)

- Ветка должна содержать актуальный `main` (иначе rebase).
- Номер новой миграции должен быть **больше** последнего в `main`: golang-migrate молча пропускает миграции с
  меньшим номером. После rebase перенумеруй миграции.
- На ветках `feature/**`, `fix/**`, `refactoring/**` CI сам коммитит `go mod tidy`. На других ветках неаккуратный
  `go.mod` роняет сборку.
- `compose-smoke` поднимает стек и проверяет, что наружу опубликованы только `app` и `postgres` на `127.0.0.1`.
  Не публикуй другие порты в `docker-compose.yml`.
