# M0: ручная проверка кирпичей по шагам

Это пошаговая проверка того, что уже есть в main: стек, слой LLM, база знаний,
транскрипция и сервер Telegram Bot API. Каждый шаг проверяется на реальных ключах.
Позже эту проверку автоматизирует `make m0-check` (PR #9), а этот файл — ручной вариант.

Документ написан так, чтобы по нему можно было работать вместе с ИИ-ассистентом в IDE
(Claude Code, Junie и т. п.). Прежде чем начать, дай ассистенту прочитать раздел
«Правила для ассистента».

## Правила для ассистента

- **Не выводи содержимое `.env`** и значения ключей: не делай `cat .env`, `env`, `printenv`,
  `docker compose config` без `--images`. Если нужно проверить, задана ли переменная,
  используй `grep -c '^OPENAI_API_KEY=.\+' .env`: он печатает только число.
- **Не удаляй данные.** Никаких `docker compose down -v`, `docker volume rm`, `rm -rf kb/`,
  `make kb-delete` по чужим документам. Если шаг падает, остановись и покажи ошибку.
- **Не коммить** `.env`, `agents/test/`, содержимое `kb/`, транскрипты, логи.
- Вывод с ошибкой показывай целиком, но замазывай строки вида `sk-...`, `sk-ant-...`
  и любые длинные токены.
- После каждого шага сверяй результат с блоком «Ожидаемый результат». Если результат
  не совпадает, сначала разберись в причине (раздел «Типовые проблемы»), потом переходи дальше.

## Перед началом

В `.env` должны быть заданы:

| Переменная | Для чего | Где взять |
| --- | --- | --- |
| `ANTHROPIC_API_KEY` | шаг 2 (агент через Claude) | [platform.claude.com/settings/keys](https://platform.claude.com/settings/keys). На тарифах Max, Ultra и Team можно привязать организацию Console к подписке и тратить ежемесячные API-кредиты: [статья](https://support.claude.com/en/articles/17154008) |
| `OPENAI_API_KEY` | шаги 3 и 4 (эмбеддинги и Whisper) | [platform.openai.com/api-keys](https://platform.openai.com/api-keys) |
| `TELEGRAM_API_ID`, `TELEGRAM_API_HASH` | шаг 5 (локальный Bot API) | [my.telegram.org](https://my.telegram.org) → API development tools |

Остальные значения из `.env.dist` подходят для локальной проверки как есть.
Значения ключей пишутся в `.env` без кавычек.

Материалы для проверки:
- 1–3 файла бренда (md, docx, txt или pdf) — их нужно положить в `kb/brand/` (шаг 3);
- 1 короткий ролик тренера (1–3 минуты, со звуком) — для шага 4.

## Шаг 0. Подготовка

```bash
git pull
docker compose version     # Docker Desktop запущен, compose v2
go version                 # нужен Go; версия 1.26 скачается сама (GOTOOLCHAIN=auto)
ffmpeg -version | head -1  # для транскрипции; на macOS: brew install ffmpeg
```

**Ожидаемый результат:** все четыре команды отрабатывают без ошибок.

## Шаг 1. Стек, `/readyz`, миграции

```bash
make up
curl -s localhost:8080/readyz; echo
docker compose run --rm migrate up
docker compose exec -T postgres psql -U postgres -d ai_agents -Atc 'select version, dirty from schema_migrations'
```

Если в `.env` изменены `POSTGRES_USER` или `POSTGRES_DB`, подставь их вместо `postgres` и `ai_agents`.

**Ожидаемый результат:**
- `make up` завершается без ошибок, все контейнеры в состоянии `healthy`;
- `/readyz` → `{"status":"ok","dependencies":{"postgres":"ok","redis":"ok","s3":"ok"}}`;
- миграции → `3|f` (версия 3, не dirty).

## Шаг 2. Агент через Claude

Настоящих агентов ещё нет, поэтому проверяем копию шаблона:

```bash
cp -r agents/_example agents/test
sed -i.bak 's/^name: _example$/name: test/' agents/test/agent.md && rm agents/test/agent.md.bak
make agent-run AGENT=test PROMPT="Ответь одним словом: работает?"
```

`sed -i.bak` работает и на macOS, и на Linux.

**Ожидаемый результат:**
- короткий ответ модели;
- блок `--- usage ---` с токенами (input/output) и стоимостью `cost: $0.000...`.

Если вместо ответа пришла строка `--- partial answer: ... ---`, это частичный ответ. Причина указана в этой же строке.

## Шаг 3. База знаний

Файлы берутся только из папки `KB_ROOT` (по умолчанию `./kb`). Она не попадает ни в git, ни в Docker-образ.

```bash
mkdir -p kb/brand
cp <путь к файлу бренда> kb/brand/
make kb-ingest KB_PATH=./kb/brand KB_KIND=brand
make kb-search Q="как мы говорим о питании" LIMIT=3
```

Если в материалах нет темы питания, подставь в `Q` вопрос, ответ на который в файле точно есть.

**Ожидаемый результат:**
- `kb-ingest` печатает по строке на файл: `created   brand/<файл> (N chunks)`. Повторный запуск без изменений файла даёт `unchanged`;
- `kb-search` возвращает результаты вида `[1] brand/<файл> — «<заголовок>» (brand, score 0.xx)` и под ними текст фрагмента. Первым должен идти релевантный фрагмент;
- PDF разбирается в отдельном процессе. Если какой-то файл не удаётся разобрать (скан без текстового слоя, повреждённый PDF), на него выводится ошибка, а остальные файлы всё равно загружаются.

## Шаг 4. Транскрипция

```bash
make transcribe ARGS="-in <путь к ролику>.mp4 -out /tmp/m0-transcript.json"
python3 -c "import json;d=json.load(open('/tmp/m0-transcript.json'));print(d['language'], round(d['duration'],1), len(d['words']), 'слов');print(d['text'][:500])"
```

**Ожидаемый результат:**
- `language` = `ru`, `duration` совпадает с длиной ролика, слов больше нуля;
- текст читается. **Главное в этом шаге — оценить, насколько точно распознана речь тренера**: имена, термины, названия упражнений;
- если в ролике есть музыка, в логе могут быть строки `dropped hallucinated segment`. Это фильтр выдуманных фраз, проверь, что он не выкинул настоящую речь.

Транскрипт хранится в `/tmp`, а не в репозитории.

## Шаг 5. Telegram Bot API (локальный сервер)

```bash
docker compose --profile telegram up -d --wait telegram-bot-api
docker compose ps telegram-bot-api
docker compose logs --tail=30 telegram-bot-api
```

**Ожидаемый результат:**
- статус `healthy`;
- в логах нет ошибок авторизации. Healthcheck проверяет только открытый порт, поэтому неверные `api_id`/`api_hash` видны только в логах.

Остановить сервер, не трогая данные: `docker compose --profile telegram stop telegram-bot-api`.

## Итог прогона

Заполни таблицу и пришли её вместе с выводами в чат проекта:

| Шаг | Результат (PASS / FAIL / SKIP) | Комментарий |
| --- | --- | --- |
| 0. Подготовка | | |
| 1. Стек и миграции | | |
| 2. Агент через Claude | | ответ, токены, стоимость |
| 3. База знаний | | сколько чанков, релевантен ли первый результат |
| 4. Транскрипция | | качество распознавания, ошибки в терминах |
| 5. Telegram Bot API | | |

Посмотреть, сколько стоил прогон:

```bash
docker compose exec -T postgres psql -U postgres -d ai_agents -c \
  "select agent, model, sum(input_tokens) in_tok, sum(output_tokens) out_tok, sum(cost_usd) usd from llm_usage group by 1,2"
```

## Уборка (по желанию)

```bash
rm -rf agents/test                                # временный агент
make kb-delete SOURCE=brand/<файл>                # только если документ больше не нужен
make down                                         # остановить стек; данные в томах сохраняются
```

## Типовые проблемы

| Симптом | Причина и решение |
| --- | --- |
| `make up` висит или падает на сборке | Не запущен Docker Desktop или ему мало памяти (дай 4 ГБ и больше). Причину покажет `docker compose logs <сервис>` |
| порт 8080 или 5432 занят | Поменяй `APP_HOST_PORT` или `POSTGRES_HOST_PORT` в `.env` и выполни `make up` |
| `anthropic api key is not set` | Нет ключа в `.env` или он в кавычках. После правки `.env` снова выполни `make agent-run`: он пересоздаёт контейнер |
| Anthropic: `credit balance is too low` | На организации Console нет кредитов: пополни баланс или привяжи подписку Max/Ultra/Team |
| OpenAI: `401` / `insufficient_quota` | Неверный ключ или нет средств на аккаунте OpenAI |
| `agent not found` | В `agents/test/agent.md` должно быть `name: test`; папки, которые начинаются с `_`, не загружаются |
| `path escapes from parent` / вне `KB_ROOT` | Файл должен лежать внутри `./kb`; симлинки наружу запрещены специально |
| `go: downloading go1.26...` долго | Первый запуск скачивает тулчейн, дальше быстрее |
| bot-api в логах: ошибка авторизации | Проверь `TELEGRAM_API_ID`/`TELEGRAM_API_HASH`; приложение на my.telegram.org должно быть создано |
