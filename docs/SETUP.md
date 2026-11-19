# Установка и проверка M0

Пошаговая установка с нуля на macOS (Docker Desktop) и на Linux-VPS, справочник переменных `.env`
и сквозная проверка `make m0-check`, которая за один проход проверяет все «кирпичи»: стек, LLM, базу знаний,
транскрипцию и Telegram Bot API.

## 1. Требования

| Что | Зачем | Версия |
|---|---|---|
| Docker | весь стек работает в контейнерах | Docker Desktop (macOS) или Docker Engine (Linux) |
| Docker Compose v2 | `docker compose …` (через пробел; старый `docker-compose` v1 не подходит) | v2.20+ (`--wait`) |
| make, git, curl | команды проекта, клонирование, проверка `/readyz` | любые актуальные |
| Go | **нужен для базы знаний и транскрипции на хосте**: `make kb-*` и `make transcribe` запускают `go run`; также разработка (`make check`) | 1.21+; нужную версию (1.26 из `go.mod`) Go скачает сам, если не задан `GOTOOLCHAIN=local` |
| ffmpeg, ffprobe | только `make transcribe` на хосте; без них транскрипция работает в образе `media` | 8.x |

Без Go работают стек, агенты (`make agent-run`) и транскрипция в образе `media`; не работает CLI базы знаний
(`make m0-check` пропустит этот шаг с подсказкой). Перенос `kb` в образ запланирован отдельной задачей.

### macOS

1. Установи [Docker Desktop](https://docs.docker.com/desktop/setup/install/mac-install/) и запусти его
   (иконка кита в строке меню должна перестать «крутиться»). Compose v2 и `docker compose` входят в комплект.
2. В Docker Desktop → Settings → Resources дай не меньше 4 ГБ памяти: сборка образа и pgvector.
3. `make`, `git`, `curl` — из Xcode Command Line Tools: `xcode-select --install`.
4. Go и ffmpeg (по желанию): `brew install go ffmpeg` ([Homebrew](https://brew.sh)) или установщик с
   [go.dev/dl](https://go.dev/dl/).

### Linux-VPS (Ubuntu/Debian)

1. Docker Engine и плагин compose — по [официальной инструкции](https://docs.docker.com/engine/install/)
   (пакеты `docker-ce` и `docker-compose-plugin` из репозитория Docker, не `docker.io` из дистрибутива — в нём часто
   нет compose v2).
2. Чтобы запускать docker без sudo: `sudo usermod -aG docker "$USER"`, затем перелогиниться
   ([post-install](https://docs.docker.com/engine/install/linux-postinstall/)).
3. `sudo apt-get install -y make git curl`; Go — с [go.dev/dl](https://go.dev/dl/) (в apt обычно старый), ffmpeg —
   `sudo apt-get install -y ffmpeg` (по желанию).
4. Наружу стек публикует только `app` и `postgres`, и только на `127.0.0.1`, поэтому файрвол для них открывать не нужно.
   Для доступа к Postgres с ноутбука используй SSH-туннель: `ssh -L 5432:127.0.0.1:5432 user@vps`.

Проверка: `docker compose version`, `docker info` (без ошибки), `make --version`, `go version` (если нужен).

## 2. Клонирование и `.env`

```bash
git clone https://github.com/Prokud1n/ai-agents.git
cd ai-agents
cp .env.dist .env
```

`.env` в `.gitignore`: ключи и пароли в git не попадают. Значения из `.env.dist` годятся для локальной разработки как
есть; ключи API добавляются по мере надобности (раздел 4). Для продакшна обязательно смени пароли (ниже).

После обновления репозитория сравни `.env` с `.env.dist`: новые переменные в `.env` сами не появятся
(`make m0-check` покажет, каких не хватает).

## 3. Справочник переменных `.env`

«Секрет» — значение нельзя коммитить, показывать в скриншотах и логах. «Обязательна» — без неё не стартует стек
или соответствующая функция; остальные можно не трогать. Колонка «В `.env.dist`» — значение из шаблона, с которым
работает `cp .env.dist .env`. Если переменную из `.env` удалить, код возьмёт свой дефолт, и он бывает другим
(например, `POSTGRES_HOST=localhost`, `TELEGRAM_BOT_API_URL=https://api.telegram.org`), поэтому лучше не удалять,
а менять значение.

Формат `.env`: `KEY=VALUE`, по строке на переменную. Кавычки допустимы для `make` и `docker compose`, но
`docker run --env-file` (ручной запуск образа `media` из README) их не снимает: для него пиши значения без кавычек.
`make m0-check` передаёт переменные в `docker run` сам, кавычки ему не мешают.

> **Продакшн.** Обязательно смени `POSTGRES_PASSWORD`, `REDIS_PASSWORD`, `S3_ACCESS_KEY` и `S3_SECRET_KEY`: значения
> из `.env.dist` публичны. Сгенерировать: `openssl rand -base64 32 | tr -d '/+='`. Пароль Postgres задаётся только
> при первом создании тома, поэтому меняй его до первого `make up` (или см. «Типовые проблемы»).

### Приложение

| Переменная | Обязательна (для чего) | В `.env.dist` | Где взять / как выбрать | Секрет |
|---|---|---|---|---|
| `LOG_LEVEL` | нет | `info` | `debug`, `info`, `warn`, `error` | нет |
| `HTTP_ADDRESS` | нет | `:8080` | адрес HTTP-сервера; в compose всегда `:8080` (переопределён), важен только для `make run` | нет |
| `HTTP_READ_HEADER_TIMEOUT` | нет | `5s` | таймауты HTTP-сервера, формат Go duration (`500ms`, `15s`, `5m`) | нет |
| `HTTP_READ_TIMEOUT` | нет | `15s` | — | нет |
| `HTTP_WRITE_TIMEOUT` | нет | `15s` | — | нет |
| `HTTP_IDLE_TIMEOUT` | нет | `60s` | — | нет |
| `APP_START_TIMEOUT` | нет | `15s` | сколько ждать подключения к зависимостям при старте | нет |
| `APP_STOP_TIMEOUT` | нет | `15s` | держи меньше `stop_grace_period` (20 с) в `docker-compose.yml` | нет |

### PostgreSQL

| Переменная | Обязательна (для чего) | В `.env.dist` | Где взять / как выбрать | Секрет |
|---|---|---|---|---|
| `POSTGRES_HOST` | да (приложение) | `postgres` | имя сервиса compose; для `make run` — `localhost` в `.env.local` | нет |
| `POSTGRES_PORT` | нет | `5432` | порт внутри сети compose | нет |
| `POSTGRES_USER` | да (контейнер postgres, приложение) | `postgres` | любое имя; нужно право `CREATEDB` для интеграционных тестов | нет |
| `POSTGRES_PASSWORD` | да | `postgres` | **сменить в продакшне**; задаётся при первом создании тома | **да** |
| `POSTGRES_DB` | да | `ai_agents` | имя базы | нет |
| `POSTGRES_SSL_MODE` | нет | `disable` | внутри compose SSL не нужен; для внешней БД — `require`/`verify-full` | нет |

### Redis

| Переменная | Обязательна (для чего) | В `.env.dist` | Где взять / как выбрать | Секрет |
|---|---|---|---|---|
| `REDIS_ADDRESS` | да (приложение) | `redis:6379` | сервис compose; для `make run` — `localhost:6379` | нет |
| `REDIS_PASSWORD` | да (контейнер redis, приложение) | `redis-dev-password` | **сменить в продакшне**; применяется при перезапуске контейнера | **да** |
| `REDIS_DB` | нет | `0` | номер базы Redis | нет |

### S3 (RustFS)

| Переменная | Обязательна (для чего) | В `.env.dist` | Где взять / как выбрать | Секрет |
|---|---|---|---|---|
| `S3_ENDPOINT` | да (приложение) | `s3:9000` | сервис compose; для `make run` — `localhost:9000` | нет |
| `S3_ACCESS_KEY` | да (контейнер s3, приложение) | `rustfsadmin` | логин администратора RustFS; **сменить в продакшне** | **да** |
| `S3_SECRET_KEY` | да | `rustfsadmin` | пароль администратора RustFS; **сменить в продакшне** | **да** |
| `S3_BUCKET` | нет | `media` | бакет для медиа; приложение создаёт его само | нет |
| `S3_REGION` | нет | `us-east-1` | для RustFS не важен | нет |
| `S3_USE_SSL` | нет | `false` | `true` для внешнего S3 по HTTPS | нет |

### Telegram

| Переменная | Обязательна (для чего) | В `.env.dist` | Где взять / как выбрать | Секрет |
|---|---|---|---|---|
| `TELEGRAM_BOT_API_URL` | нет | `http://telegram-bot-api:8081` | локальный Bot API (профиль `telegram`) или публичный `https://api.telegram.org` | нет |
| `TELEGRAM_API_ID` | для профиля `telegram` | пусто | [my.telegram.org](https://my.telegram.org) → API development tools (раздел 4) | **да** |
| `TELEGRAM_API_HASH` | для профиля `telegram` | пусто | там же | **да** |

Токен бота от @BotFather в `.env` пока не нужен: переменная появится вместе с ботом (следующие задачи).

### OpenAI: Whisper и эмбеддинги

| Переменная | Обязательна (для чего) | В `.env.dist` | Где взять / как выбрать | Секрет |
|---|---|---|---|---|
| `OPENAI_API_KEY` | для транскрипции и базы знаний | пусто | [OpenAI Platform → API keys](https://platform.openai.com/api-keys) (раздел 4) | **да** |
| `OPENAI_BASE_URL` | нет | `https://api.openai.com/v1` | менять только для совместимого прокси | нет |
| `WHISPER_MODEL` | нет | `whisper-1` | единственная модель OpenAI с таймкодами слов; отключается 26.02.2027 | нет |
| `WHISPER_LANGUAGE` | нет | `ru` | ISO 639-1; пусто — автоопределение | нет |
| `WHISPER_REQUEST_TIMEOUT` | нет | `5m` | таймаут одного запроса | нет |
| `WHISPER_MAX_RETRIES` | нет | `3` | повторы при 429/5xx и сетевых ошибках | нет |
| `WHISPER_RETRY_BASE_DELAY` | нет | `2s` | база экспоненциальной задержки | нет |
| `TRANSCRIPTION_SILENCE_NOISE_DB` | нет | `-30` | порог тишины для нарезки длинного аудио; с фоновой музыкой — `-25` | нет |
| `TRANSCRIPTION_SILENCE_MIN_DURATION` | нет | `500ms` | минимальная длина паузы | нет |
| `FFMPEG_TIMEOUT` | нет | `10m` | лимит одного запуска ffmpeg/ffprobe | нет |
| `EMBEDDINGS_MODEL` | нет | `text-embedding-3-small` | вектор должен быть 1536-мерным; после смены — `make kb-reembed` | нет |
| `EMBEDDINGS_BATCH_SIZE` | нет | `100` | фрагментов в одном запросе (не больше 2048) | нет |
| `EMBEDDINGS_MAX_RETRIES` | нет | `4` | повторы при 429/5xx | нет |
| `EMBEDDINGS_RETRY_BASE_DELAY` | нет | `1s` | — | нет |
| `EMBEDDINGS_TIMEOUT` | нет | `60s` | таймаут одного запроса | нет |
| `KB_ROOT` | нет | `./kb` | папка с материалами базы знаний (раздел 5); в `.gitignore` и `.dockerignore` | нет |

### Claude API (LLM-слой)

| Переменная | Обязательна (для чего) | В `.env.dist` | Где взять / как выбрать | Секрет |
|---|---|---|---|---|
| `ANTHROPIC_API_KEY` | для запусков агентов | пусто | [Claude Console](https://platform.claude.com/docs/en/get-api-key) → Settings → API keys (раздел 4); без ключа приложение стартует, запуски агентов падают | **да** |
| `ANTHROPIC_BASE_URL` | нет | `https://api.anthropic.com` | менять только для прокси | нет |
| `ANTHROPIC_TIMEOUT` | нет | `5m` | таймаут одной HTTP-попытки | нет |
| `ANTHROPIC_MAX_RETRIES` | нет | `3` | повторы при 429/5xx и сетевых ошибках | нет |
| `ANTHROPIC_RETRY_BASE_DELAY` | нет | `1s` | задержка = база × 2^попытка | нет |
| `ANTHROPIC_RETRY_MAX_DELAY` | нет | `30s` | потолок задержки (и `retry-after`) | нет |
| `LLM_DEFAULT_MODEL` | нет | `claude-sonnet-5-5` | модель по умолчанию; агент может задать свою в `agent.md` | нет |
| `LLM_DEFAULT_MAX_TOKENS` | нет | `8192` | лимит ответа | нет |
| `LLM_PRICES` | нет | см. `.env.dist` | цены USD за 1M токенов `модель=input/output/cache_write/cache_read`; [страница цен](https://platform.claude.com/docs/en/about-claude/pricing). Модель без цены пишется в `llm_usage` с `cost_usd = NULL` | нет |
| `AGENTS_DIR` | нет | `./agents` | папка агентов; в образе `/app/agents` | нет |
| `SKILLS_DIR` | нет | `./skills` | общие скиллы; в образе `/app/skills` | нет |

### docker compose

| Переменная | Обязательна (для чего) | В `.env.dist` | Где взять / как выбрать | Секрет |
|---|---|---|---|---|
| `APP_HOST_PORT` | нет | `8080` | порт `app` на `127.0.0.1` хоста; поменяй, если 8080 занят | нет |
| `POSTGRES_HOST_PORT` | нет | `5432` | порт Postgres на `127.0.0.1` хоста (отладка, `make kb-*`, интеграционные тесты); поменяй, если на хосте свой Postgres | нет |

## 4. Где получить ключи

Все ключи — только на официальных сайтах. Ключ показывается один раз: сразу вставь его в `.env`.
Не присылай ключи в чаты и не прикладывай `.env` к отчётам: `make m0-check` печатает только «задан (…последние 4 символа)».

- **Claude API (`ANTHROPIC_API_KEY`).** [Claude Console](https://platform.claude.com/docs/en/get-api-key) → войти →
  Settings → API keys → Create Key. Нужны пополненный баланс (Settings → Billing) или кредиты по подписке (ниже)
  и, по желанию, лимит расходов. Ключ начинается с `sk-ant-`. Без денег на балансе API отвечает
  `credit balance is too low`.

  **API-кредиты по подписке Claude.** На тарифах **Max, Ultra и Team** организацию Claude Console можно привязать
  к подписке и получать ежемесячные кредиты на API: claude.ai → Settings → Billing (на Team — Organization
  settings → Billing) → раздел API credits → Link organization. **Pro (и Free, Enterprise) не подходит.** Кредиты
  можно получить через 7 дней после оформления тарифа, они не переносятся на следующий месяц, тратятся раньше
  купленных и общие для всех ключей организации. Привязать можно одну организацию, сменить её — только через
  поддержку, поэтому привязывай ту, где будет ключ проекта. Нужны роли: подписчик (на Team — Owner/Primary Owner)
  и Admin или Billing в организации Console. Подробности — в официальных статьях:
  [Help Center](https://support.claude.com/en/articles/17154008) и
  [документация](https://platform.claude.com/docs/en/about-claude/api-credits-for-subscribers).

  Суммы на момент написания (19.11.2026; актуальные — по ссылкам выше):

  | Тариф | Кредиты в месяц |
  |---|---|
  | Max | $100 |
  | Ultra | $200 |
  | Team, Standard seat | $20 за место |
  | Team, Premium seat | $100 за место |

  На Team кредиты всех мест складываются в общий баланс, не больше $500 в месяц.
- **OpenAI (`OPENAI_API_KEY`).** [platform.openai.com/api-keys](https://platform.openai.com/api-keys) → Create new
  secret key. Нужен положительный баланс в Billing; без него Whisper и эмбеддинги отвечают `insufficient_quota`.
  Ключ начинается с `sk-`.
- **Telegram API (`TELEGRAM_API_ID`, `TELEGRAM_API_HASH`).** [my.telegram.org](https://my.telegram.org) → войти по
  номеру телефона → API development tools → создать приложение (название и short name — любые) → `App api_id` и
  `App api_hash` ([описание](https://core.telegram.org/api/obtaining_api_id)). Нужны только для локального Bot API
  (файлы до 2 ГБ).
- **Токен бота.** В Telegram написать [@BotFather](https://t.me/BotFather) → `/newbot` → имя и username, оканчивающийся
  на `bot` → токен вида `123456:ABC…` ([документация](https://core.telegram.org/bots/features#botfather)). В `.env`
  он пока не нужен: сохрани его в менеджере паролей до задачи с ботом. Для `make m0-check` заведи **отдельного
  тестового бота** (раздел 7).

## 5. Материалы базы знаний (`KB_ROOT`)

`kb ingest` берёт файлы только из `KB_ROOT` (по умолчанию `./kb`). Папка в `.gitignore` и `.dockerignore`: материалы
тренера не попадают ни в git, ни в образ. `source` документа — путь относительно `KB_ROOT` (например, `brand/voice.md`).

Форматы: `.md`, `.txt`, `.docx`, `.pdf` (только текстовый слой: сканы сначала распознать). Файл до 32 МБ, до
1 000 000 символов. Скрытые файлы и папки (`.…`) пропускаются. Заголовок `# …` в начале файла становится названием.

Рекомендуемая структура — по `kind` (тип задаётся при загрузке и используется как фильтр поиска):

```
kb/
  brand/     # kind=brand: голос бренда, ЦА, ценности, брендбук, табу, примеры формулировок
  product/   # kind=product: программы тренировок, марафоны, консультации, цены, условия
  post/      # kind=post: лучшие посты и сценарии Reels — образцы стиля
  other/     # kind=other: всё остальное (FAQ, биография, кейсы клиентов)
```

```bash
make up
make kb-ingest KB_PATH=./kb/brand KB_KIND=brand
make kb-ingest KB_PATH=./kb/product KB_KIND=product
make kb-ingest KB_PATH=./kb/post KB_KIND=post
make kb-search Q="как мы говорим о питании" LIMIT=5
```

Без `KB_KIND` у уже загруженного документа сохраняется его тип (у нового — `other`); сменить тип можно только
с `KB_FORCE=1`. Один документ — одна тема: так поиск точнее. Временные папки `kb/_m0check.XXXXXX/` создаёт и удаляет
`make m0-check`, не клади туда свои файлы.

## 6. Запуск, проверка, остановка

```bash
make up                              # собрать образ, применить миграции (сервис migrate), поднять всё и дождаться healthy
curl 127.0.0.1:8080/readyz           # {"status":"ok","dependencies":{"postgres":"ok","redis":"ok","s3":"ok"}}
docker compose ps                    # app, postgres, redis, s3 — healthy; migrate — Exited (0)
make logs                            # логи всех сервисов (Ctrl+C — выйти)
```

Миграции применяются автоматически при каждом `make up`. Вручную: `make migrate` (все новые), `make migrate-down`
(откатить одну). Текущая версия:

```bash
docker compose exec postgres sh -c 'psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -c "SELECT * FROM schema_migrations"'
```

Должна совпадать с номером последнего файла в `migrations/`, `dirty = f`.

Telegram Bot API (по желанию, нужны `TELEGRAM_API_ID`/`TELEGRAM_API_HASH`): `docker compose --profile telegram up -d`.

Остановка и сброс:

```bash
make down                            # остановить; данные (тома postgres, redis, s3, bot api) сохраняются
docker compose --profile telegram down -v   # остановить и УДАЛИТЬ все данные: базу, загруженные медиа, базу знаний
```

`down -v` необратим: перед ним сделай дамп, если данные нужны
(`docker compose exec postgres sh -c 'pg_dump -U "$POSTGRES_USER" "$POSTGRES_DB"' > dump.sql`).

## 7. Проверка M0 (`make m0-check`)

Сквозная проверка: что окружение настроено и каждый кирпич работает с реальными ключами.

```bash
make m0-check                        # шаги без ключей — SKIP
make m0-check M0_VIDEO=./clip.mp4    # плюс транскрипция ролика (лучше 10–60 с речи)

# плюс getMe тестового бота через локальный Bot API; токен — только в окружении, не в .env и не в истории shell
read -rs M0_BOT_TOKEN && export M0_BOT_TOKEN && make m0-check; unset M0_BOT_TOKEN
```

> **`M0_BOT_TOKEN` — только для тестового бота.** При первом запросе локальный сервер Bot API логинит бота у себя;
> боту, который в это время работает через облачный `api.telegram.org`, это может помешать (обновления и вебхук
> переходят к локальному серверу). Заведи у @BotFather отдельного бота для проверок. Токен не пишется в `.env`,
> логи и отчёт; ответ `getMe` не сохраняется.

| Шаг | Что проверяет | Когда SKIP |
|---|---|---|
| 1. Окружение | docker, compose v2, daemon, make, curl, `.env`; обязательные переменные заданы; WARN — dev-пароли, переменные из `.env.dist`, которых нет в `.env`, и отсутствие git (в отчёте `Git: unknown`) | никогда |
| 2. Стек | `make up`, все сервисы healthy, `/readyz` = ok по всем зависимостям, версия миграций = последней в `migrations/`, не dirty | шаг 1 не прошёл |
| 3. LLM | временная копия `agents/_example` в `agents/m0check-<pid>`, `make agent-run PROMPT="Ответь одним словом: работает"`: ответ не пустой, есть строка расхода | нет `ANTHROPIC_API_KEY` |
| 4. База знаний | документ `m0-check-<pid>-<время>.md` во временной `KB_ROOT/_m0check.XXXXXX/`; проверка, что такого source в базе нет; `make kb-ingest`, `make kb-search` по фразе из него — документ первый, `make kb-delete` | нет `OPENAI_API_KEY` или Go |
| 5. Транскрипция | `make transcribe` на `M0_VIDEO` (без Go/ffmpeg на хосте — в образе `media`): JSON с непустым `language` и `words` | нет ключа или `M0_VIDEO` |
| 6. Telegram | поднимает `telegram-bot-api` и ждёт его healthcheck: PASS «порт открыт, ключи не проверены». С `M0_BOT_TOKEN` — ещё `getMe` через локальный сервер, это проверяет `api_id`/`api_hash` и токен. Если сервис был остановлен — останавливает обратно | нет `TELEGRAM_API_ID`/`TELEGRAM_API_HASH` |

В конце — таблица PASS / FAIL / SKIP; код выхода не 0, если есть хоть один FAIL. Всё сохраняется в `m0-results/`
(в `.gitignore`):

- `report.md` — **приложи его к отчёту**: дата, git SHA, шаги с длительностью, предупреждения, расход LLM из
  `llm_usage` за прогон, версии образов. Секретов в нём нет;
- `up.log`, `llm.out`/`llm.log`, `kb.log`, `transcribe.log`, `telegram.log` — подробности для FAIL. Ключи
  (`sk-…`, токены ботов, значения ключей и паролей из `.env`) в них маскируются, но логи — для разбора на месте:
  **не пересылай их** и не прикладывай к отчёту, там могут быть адреса, тексты и прочие внутренние детали;
- `transcript.json` — результат транскрипции (содержит текст ролика).

Скрипт можно запускать повторно. Он не трогает пользовательские данные и удаляет только то, что создал сам:
временный агент `agents/m0check-<pid>`, папку `KB_ROOT/_m0check.XXXXXX` и документ в базе знаний с уникальным
именем. Если такой путь или документ уже есть — шаг FAIL, чужое не трогается. `.env` читается построчно и не
исполняется. Ctrl+C прерывает весь прогон (код 130) и убирает временное; `down -v` скрипт не делает. Стоимость прогона с ключами — доли цента (один короткий запрос
к Claude, эмбеддинги двух коротких текстов) плюс Whisper по длине ролика.

Ограничение шага 6 без `M0_BOT_TOKEN`: у Bot API нет health-эндпоинта, healthcheck проверяет только, что порт 8081
принимает соединения. Неверные `api_id`/`api_hash` видны в `docker compose --profile telegram logs telegram-bot-api`
или при проверке с токеном тестового бота.

## 8. Типовые проблемы

| Симптом | Причина и решение |
|---|---|
| `Cannot connect to the Docker daemon` | Docker Desktop не запущен (macOS) или нет прав (Linux: группа `docker`, перелогиниться) |
| `docker: 'compose' is not a docker command` | стоит compose v1 или нет плагина: установи `docker-compose-plugin` (Linux) / обнови Docker Desktop |
| `POSTGRES_PASSWORD is required` (и т.п.) при `make up` | нет `.env` или в нём пустая переменная: `cp .env.dist .env` |
| `bind: address already in use` на 8080 или 5432 | порт занят (свой Postgres, другой проект): задай `APP_HOST_PORT` / `POSTGRES_HOST_PORT` в `.env` |
| `make up` висит, `app` unhealthy | `docker compose logs app migrate`; чаще всего неверные пароли или упавшая миграция |
| `password authentication failed` после смены `POSTGRES_PASSWORD` | пароль применяется только при первом создании тома. Смени его в самой базе (внутри контейнера вход по сокету без пароля): `docker compose exec postgres sh -c 'psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -c "\password $POSTGRES_USER"'` — новый пароль спросит интерактивно, он должен совпасть с `.env`; затем `make up`. Если данные не нужны — `docker compose down -v` |
| `app` не проходит проверку S3 после смены `S3_ACCESS_KEY`/`S3_SECRET_KEY` | ключи — учётка администратора RustFS. На свежей установке проще всего `down -v`; на рабочей — меняй ключи до первого запуска |
| `/readyz` = 503 | в JSON видно, какая зависимость `fail`; причина — в `docker compose logs app` |
| `migrate` упал, база `dirty` | `docker compose logs migrate`; исправь миграцию (если она не смержена) и повтори `make migrate`. Смерженные миграции не редактируются |
| `anthropic api key is not set` | пустой `ANTHROPIC_API_KEY` в `.env` |
| Claude: `401` / `invalid x-api-key` | ключ неверный или отозван: создай новый в Console |
| Claude: `credit balance is too low` | на балансе организации Console нет денег и кредитов: пополни Billing или привяжи подписку (раздел 4) |
| OpenAI: `401` / `invalid_api_key` | ключ неверный или отозван |
| OpenAI: `insufficient_quota` | на балансе OpenAI нет денег (Billing на platform.openai.com) |
| `make kb-*`: `go: command not found`; в `m0-check` шаг 4 SKIP «нет Go» | CLI базы знаний запускается на хосте: установи Go (раздел 1) |
| `make kb-*`: `connection refused` к `127.0.0.1:5432` | стек не поднят (`make up`) или `POSTGRES_HOST_PORT` отличается от того, с которым поднят стек |
| `kb ingest`: файл вне `KB_ROOT` | положи материалы в `./kb` (или задай `KB_ROOT`) |
| `make transcribe`: `"ffmpeg": executable file not found` | установи ffmpeg или используй образ `media` (README, «Транскрипция видео») |
| Транскрипция режет речь посреди фраз, видео с музыкой | подними `TRANSCRIPTION_SILENCE_NOISE_DB` до `-25` |
| Apple Silicon: сборка медленная | первый `make up` собирает образ (несколько минут); дальше работает кэш |
| `m0-check`: шаг 4 FAIL «не первый в поиске» | в базе есть очень похожий документ или сменилась модель эмбеддингов без `make kb-reembed`; подробности — `m0-results/kb.log` |
| `m0-check`: шаг 6 FAIL «getMe не ok» | неверный `M0_BOT_TOKEN` или `TELEGRAM_API_ID`/`TELEGRAM_API_HASH`; смотри `docker compose --profile telegram logs telegram-bot-api` |
| `docker run --env-file .env`: ключ «неверный», хотя в `.env` он правильный | `--env-file` не снимает кавычки: убери кавычки вокруг значений в `.env` |
