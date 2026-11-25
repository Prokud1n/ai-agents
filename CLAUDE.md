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
`redis`, `s3` на RustFS, `telegram-bot-api` под профилем `telegram`, `media-worker` на образе `media` с ffmpeg). Запуск локально и на хосте — в README.md.

## Команды сверх AGENTS.md

```bash
go test -race -run TestName ./internal/agents/          # один тест
make up && make test-integration                        # интеграционные тесты против compose-postgres (+ одноразовые Redis и RustFS)
make s3-test-up / make s3-test-down                     # одноразовый RustFS для тестов вручную (так же redis-test-up/down)
FFMPEG_TESTS_REQUIRED=1 go test -run TestRunner ./internal/providers/ffmpeg/  # без ffmpeg тесты иначе скипаются
make agent-run AGENT=<name> PROMPT="..."                # прогнать одного агента в compose (нужен ANTHROPIC_API_KEY)
make kb-ingest KB_PATH=./kb/brand KB_KIND=brand         # база знаний в образе (KB_HOST=1 — go run): также kb-search / kb-delete / kb-reembed
make transcribe ARGS="-in video.mp4"                    # нужен ffmpeg и OPENAI_API_KEY
make subtitles ARGS="-in video.mp4 -transcript result.json -out reel.mp4"  # субтитры; нужен ffmpeg с libass
FFMPEG_TESTS_REQUIRED=1 go test -run TestBurnSubtitlesWithFFmpeg ./internal/subtitles/  # рендер субтитров
INTEGRATION=1 go test -run TestIntegrationPipeline ./internal/mediaworker/  # конвейер Монтажёра на ffmpeg, без Redis/S3
docker compose logs -f media-worker                     # шаги монтажа (step done) с секундами
```

- `make lint`/`make fmt` идут через `scripts/golangci-lint.sh` (запиненная версия), а не через глобальный бинарь.
- Интеграционный тест: в имени есть `Integration`, первой строкой вызывает `pgtest.Connect(t)`. Без `INTEGRATION=1`
  он скипается; с ним получает отдельную свежую БД со всеми миграциями (можно `t.Parallel()`).
  `TestIntegrationPipeline` требует ffmpeg с libass на хосте (`make test-integration` без него падает). Redis — через
  `redistest.Connect(t)` (свой префикс ключей), S3 — через `s3test.Connect(t)` (свой бакет на тест, одноразовый
  RustFS из `make s3-test-up` на `127.0.0.1:9100`, вне compose-проекта).

## Архитектура

### Несколько бинарей — один fx-граф по частям

`internal/` собирает наборы fx-модулей под каждую точку входа:

| Бинарь | Сборка | Что внутри |
|---|---|---|
| `main.go` (сервис) | `internal/app.go: DeclareApplicationOptions` | все провайдеры + health/readiness + agents + gateway + tasks + teamchat + HTTP-сервер (последним) |
| `cmd/agent-run` | `DeclareAgentRunOptions` | postgres + anthropic + embeddings + llmusage + knowledge + kbtool + editplan + ideas.ToolModule + agents, без HTTP |
| `cmd/kb` | `internal/kb.go: DeclareKBOptions` / `RunKB` | postgres + embeddings + knowledge + kbcli |
| `cmd/transcribe` | `internal/transcribe.go` | ffmpeg + whisper + transcription; логи в stderr, JSON в stdout |
| `cmd/roughcut` | `internal/roughcut.go` | ffmpeg + whisper + transcription + roughcut; логи в stderr, файлы в `-out` |
| `cmd/subtitles` | `internal/subtitles.go` | ffmpeg + subtitles (ASS через libass, шрифты `assets/fonts`); логи в stderr; код выхода 2 — подмена шрифта |
| `cmd/media-worker` (сервис `media-worker`) | `internal/mediaworker.go: DeclareMediaWorkerOptions` | redis + s3 + media + ffmpeg + whisper + transcription + roughcut + subtitles + editjob + mediaworker; **без** gateway, anthropic, postgres (проверяет `TestMediaWorkerHasNoAppSecrets`); `media-worker healthcheck` — свежесть heartbeat-файла |
| `cmd/migrate` | вне fx (`internal/migrator`) | — |

Долгие CLI-команды (`kb`, `transcribe`, `roughcut`, `subtitles`) выполняются **между** `app.Start` и `app.Stop` или вовсе без `Start`, а не в
start-хуке, чтобы их не ограничивал `APP_START_TIMEOUT`. Новую команду строй так же. `media-worker` — сервис: задания
идут в горутине, запущенной start-хуком; стоп отменяет текущее задание (ffmpeg убивается по контексту). Таймауты старта и остановки
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
  at-most-once), отправка от имени роли — `gateway.Sender`. Без `TELEGRAM_*_TOKEN` шлюз выключен. Слоты обработчиков
  раздельные: сообщения — `TELEGRAM_WORKERS`, нажатия — свои, события со скачиванием файла — `TELEGRAM_MEDIA_WORKERS`.
- `internal/tasks` — задачи с согласованием: очередь Redis Streams, статусы в Postgres, черновик → `tasks.Notifier`.
- `internal/teamchat` связывает их: `Handler` (события → задачи, кнопки, реплай-правки, команды) подменяет
  `gateway.AckHandler` через `fx.Decorate` **на верхнем уровне** опций (`teamchat.Module` — `fx.Options`, а не
  `fx.Module`: декоратор внутри модуля не дошёл бы до `gateway.Module`); `Notifier` шлёт черновики от бота агента.
  Без токенов `tasks.Notifier` — `tasks.LogNotifier`. Имя агента = роль бота (`agents/marketer`, `agents/copywriter`);
  за Дизайнера пока отвечает `teamchat` фиксированным текстом (`stubTexts`); Монтажёр (`agents/editor`) — модель решает
  только, что вырезать (`internal/editplan`), а монтирует конвейер в `media-worker` (раздел ниже); на текст в чате он
  отвечает подсказкой прислать видео.
  Из медиа `teamchat` принимает только видео для Монтажёра (`gateway.MediaFilter` разрешает шлюзу скачать только его):
  файл сразу копируется в S3 через `internal/media` (ключ `<MEDIA_KEY_PREFIX>/<chat>/<message>/<file_unique_id>.<ext>`,
  обратно — `media.Store.Download`) и становится задачей Монтажёра. Голосовые и видеокружки принципиально
  не обрабатываются (тренер диктует текст), фото и аудио — пока; на всё это честный ответ без скачивания.
- `tasks.Executor` возвращает `tasks.Output` (текст и, для видео-черновика, ключ видео в S3 — `task_versions.media_key`).
  Агентов маршрутизирует `tasks.RoutingExecutor`: модуль отдаёт `tasks.Route{Agent, Kind, Executor}` в fx-группу
  `tasks.RoutesGroup` (`fx.ResultTags`), декорировать `tasks.Executor` не нужно. Маршрут агента и вида задачи важнее
  маршрута агента; пустой `Kind` — все задачи агента. Сейчас: `editor` → `editor.Executor`, `marketer`/`idea` →
  `ideas.Executor`, остальное — `AgentExecutor`. Маршрут модуля — экспортируемая опция (`editor.Route`, `ideas.Route`)
  внутри его `Module`; `internal.TestTaskRoutes` собирает настоящий `ideas.Module` (и `editor.Route`) и проверяет группу. Тест может подменить
  `tasks.Executor` целиком через `fx.Decorate`.
- Команды тренера — всегда от бота Маркетолога (`gateway.isTeamCommand`, разбор в `teamchat.Handler.handleCommand`):
  `/plan`, `/idea` — задачи; `/status`, `/ideas`, `/idea_done N`, `/idea_drop N`, `/pause`, `/help` (= `/start`),
  `/feedback`, `/onboard` — ответ сразу, без задачи и модели. Новую команду добавляй константой в `gateway/event.go` и
  в `isTeamCommand`, тексты — в `teamchat/texts.go` (на «вы», без технических деталей), строку — в `helpGeneralText`
  и, если она для тренера, в `teamchat.menuCommands` (`TestHelpMentionsEveryMenuCommand`). `unknownCommandText` только
  отсылает к `/help`. Меню (`setMyCommands`) задаёт шлюз при подключении бота: `gateway.Menu` (в модуле шлюза пустое,
  `teamchat.Module` подменяет его `fx.Decorate`, как `Handler`) — у Маркетолога в области группы, у остальных меню
  удаляется (группа, все группы, default); ошибка только в лог.
- Онбординг и пилот (T4.3a, T4.5; `internal/pilot`, миграция 008). `/help` — блок топика (`helpTopicTexts`) + общий
  текст, ≤ 4096 (тест). `/onboard` (`TELEGRAM_ADMIN_USER_IDS`, пусто — все из белого списка): приветствия
  (`teamchat.greetings`: «Планёрка» — Маркетолог, «Исходники» и «Продакшн» — Монтажёр) шлёт бот роли, закрепляет
  всегда Маркетолог (`pinRole`: право на закреп нужно только ему); id, `topic_id` и `pinned` — в `onboarding_messages`,
  повтор редактирует (`gateway.ErrNotModified` — успех) и не закрепляет заново уже закреплённое (закреп — служебное
  сообщение в топике); удалённое (`ErrMessageNotFound`), от другого бота или из другого топика — новое сообщение и
  открепление старого. Тексты: в «Продакшн» приходят только видео-черновики Монтажёра, текстовые черновики — ответом в
  топике запроса (обычно «Планёрка»). `gateway.ErrNoRights` — честный ответ и «Техничка». Автопубликации при старте нет.
  `/feedback` → `pilot.Service.SaveFeedback` (идемпотентно по сообщению; реплай на черновик — `FindByDraftMessage` →
  task_id/версия; пересланный не принимается; > 2000 символов обрезается с пометкой) → копия в «Техничку» через
  `TechReporter` (повторная доставка — снова «Спасибо», без копии). Метрики (`pilot.Compute`, чистая функция) — по
  задачам, **созданным** в периоде (`tasks` + `task_versions` версии 1 + число `task_feedback` kind `revise`): правок
  до одобрения — по запросам правок, не версия − 1 (неудачная правка версию не меняет); карточки `/idea`
  (`marketer`/`idea`) — группа `pilot.IdeasGroup`; есть `Failed`; время до черновика = `created_at` версии 1 − задачи.
  `/status` дописывает блок за 7 дней (ошибка метрик — только лог). `teamchat.WeeklyReport` — горутина по
  `PILOT_REPORT_WEEKDAY`/`PILOT_REPORT_TIME` в `TEAM_TIMEZONE`: неделя (дата в поясе) занимается в `pilot_reports`
  до отправки и освобождается при ошибке; пропущенная досылается в течение `reportCatchUp` (сутки); сводка — одно
  сообщение ≤ 4096 (лишние отзывы — «…и ещё N», тест). Меню шлюз задаёт в фоне (горутина в `pollers`), опрос его не ждёт.
- Бэклог идей (`internal/ideas`, T2.4): `/idea <текст>` → задача `marketer`/`idea` (Input — слова тренера как есть) →
  `ideas.Executor`: модель отвечает финальным инструментом `submit_idea` (сессия в контексте — `ideas.WithSession`,
  как у `editplan`), карточка проверяется (`ValidateCard`) и хранится полями в `idea_cards` по версии, текст черновика
  собирает `ideas.FormatCard` и он же хранится в `idea_cards.content`. Идея делается только из карточки, чей `content`
  совпадает с одобренной версией в `task_versions` (иначе карточку записал устаревший прогон: `ErrCardMismatch`, лог),
  а карточку версии, уже сохранённой в `task_versions`, upsert не перезаписывает (`ErrVersionStored`). Пересланная
  `/idea` помечается в Input (`ideas.ForwardedInput`), промпт говорит, что это чужие слова, а в `ideas` метка не попадает
  в `source_text` — вместо неё флаг `forwarded`; пересланные
  `/idea_done`/`/idea_drop` не выполняются. Правка идеи без карточки прошлой версии (идея до бэклога) —
  `PermanentError` с текстом. Даты для тренера — в `TEAM_TIMEZONE` (`config.Team`, tzdata встроена). `teamchat.Notifier` подписывает «Одобрить» у `idea` как «В бэклог» (`tasks.Draft.Kind`)
  и не пишет «Одобрено.» — номер идеи сообщает `Handler.approveIdea` после `Service.SaveApproved` (вставка из карточки
  одобренной версии, только если задача в `tasks` одобрена; одна идея на `task_id`). Повторное нажатие на уже
  одобренную карточку (конфликт в `tasks`) тоже вызывает `SaveApproved`, а `ListNew`/`ListForPlan` досоздают идеи
  одобренных задач, сохранение которых упало (`SyncApproved`). Статусы: `new` → `planned` → `done`/`dropped`
  (закрыть можно и из `new`); изменения — `UpdaterFn` под `SELECT … FOR UPDATE`.
  **Для `/plan` (T2.2):** возьми `ideas.Service.ListForPlan(ctx, limit)` (новые, старые первыми), передай их модели как
  данные (номер, заголовок, суть, формат, хук; `Idea.SourceText` — слова после `/idea` без метки пересылки,
  `Idea.Forwarded` — идея пришла пересланным чужим сообщением, так и скажи модели), после одобрения плана — `MarkPlanned(ctx, ids)` с номерами идей,
  попавших в план (закрытые и уже запланированные пропускаются).
- Закрывающие теги блоков данных в промптах — `pkg/helpers.ClosingTag`/`EscapeClosingTag` (любой регистр, пробелы
  внутри тега: `< /tag >`); своих регулярок не заводи.
- Порядок остановки fx — обратный порядку регистрации хуков. Хук ожидания фоновой работы регистрируй в конструкторе
  того, кто её запускает (`tasks.provideService`, `teamchat.newHandler`): потребители (шлюз, воркеры) создаются позже
  и останавливаются раньше. Проверка — `teamchat.TestIntegrationStopOrder`.

### Монтажёр: app ↔ media-worker

ffmpeg разбирает недоверенные файлы, а `app` держит токены ботов и ключи API, поэтому монтаж — в отдельном сервисе
`media-worker` (образ `media`, nonroot, read-only root, без портов, только S3/Redis/`OPENAI_API_KEY`).

- **Пока у воркера полные права в Redis и S3** (общий пароль Redis, ключи администратора RustFS): отдельный
  ACL-пользователь Redis и пользователь RustFS только на нужные ключи/префиксы — отдельная задача до деплоя на VPS.
- Видео в «Исходники» → `teamchat` создаёт задачу `editor` / `reels-edit` (`Media` = ключ исходника, `Input` = подпись).
- `internal/editjob` — контракт и «доска» в Redis: stream заданий с consumer group воркеров, `<stream>:job:<id>`,
  `<stream>:result:<id>`, блокировка `<stream>:lock:<id>` с heartbeat, канал `<stream>:done`. ID задания =
  `task-<id>-v<версия>`: повторный запуск задачи находит то же задание. Ключи файлов результата обе стороны
  считают сами (`editjob.Files` от ключа исходника и версии, `MEDIA_RESULT_PREFIX`): рилс, `plan.json` и лог решений —
  свои у каждой версии (`…-v<версия>-reel.mp4`), транскрипт исходника — один. От воркера приходят только числа и код.
- Стадии задания (`editjob.Stage`). Первая версия — одно задание `auto`: транскрипция и план; если решать нечего (нет
  подписи — `Job.Decide`, нет `Plan.Candidates`, чистовой план не длиннее `Job.TargetDuration`) — сразу рендер. Иначе воркер кладёт транскрипт и план в S3 и
  завершает задание статусом `planned`; app спрашивает модель и **заменяет** задание тем же ID на стадию `render`
  (`Board.Replace`: атомарно, только при результате `planned`). Доставка старой стадии воркером пропускается (стадия в
  stream ≠ стадии на доске). `render` читает сохранённый транскрипт (Whisper не вызывается, ключ OpenAI не нужен) и
  строит план с `Job.Settings` (настройки плана, который видела модель, — ID кандидатов те же) и `Job.Edit`.
- Правка («Правки» → реплай, или реплай на видео-черновик) = новая версия задачи: `tasks.ExecuteData.Revision`.
  `editor.Executor` читает транскрипт и `plan.json` версии N−1, модель решает, задание `render` версии N. Накопление:
  `roughcut.Plan.Edit` — все правки до этой версии; новая = прошлая + изменение (`editplan.Apply`). Сквозная проверка
  с Redis, S3 и ffmpeg — `mediaworker.TestIntegrationRevisionWithoutTranscription` и
  `TestIntegrationCaptionDecidedBeforeRender` (`make test-integration`).
- `internal/editplan` (только app): модель (`agents/editor`, правила монтажа — в role prompt, он кэшируется; через
  `agents.Runtime` — расход в `llm_usage`) отвечает **инструментом** `submit_edit` (JSON-схема): вырезать/вернуть слова
  (индексы транскрипта), обрезать начало/конец (секунды исходника для подписи, текущего ролика для правки; слово — по
  середине), принять кандидатов по ID с объяснением, исправить слово
  субтитров — или `cannot` / `unclear`. `Apply` проверяет против транскрипта и плана (индексы, ID, лимиты текста,
  ролик ≥ 1 с, правка что-то меняет) и строит план тем же `roughcut.BuildPlanEdited`, что и воркер; ошибка проверки
  уходит модели, вторая — отказ. Сессия решения едет в контексте прогона (`editplan.WithSession`), поэтому инструмент
  общий; он финальный (`EndsRun`): после принятого решения `agents.Runtime` завершает прогон без лишнего хода.
  Подпись и правка тренера в промпте — блоки данных, которые текст не может закрыть. Воркеру уходит только
  проверенный `roughcut.Edit`: индексы, ID и исправленные слова субтитров (без управляющих и невидимых символов и
  разметки) плюс короткие заметки модели — только в `decisions.log`; текст тренера воркер не видит, в ffmpeg и ASS
  свободный текст не попадает. `editplan.Module` — в app и `agent-run`
  (иначе `agents/editor` не загрузится); в `media-worker` его нет (`TestMediaWorkerHasNoAppSecrets`).
- `internal/editor` (app): `Executor` **не ждёт** воркер — нет результата → `tasks.DeferredError` (задача в очередь,
  попытка не считается, проверка через `EDITOR_POLL_INTERVAL`), `Waker` по каналу `done` будит задачу
  (`tasks.Service.Wake`). Готово → `Output{Text: описание, MediaKey: рилс версии}` (описание новой версии — что
  изменено, `editjob.Summary` в задании); провал → `PermanentError` с текстом по коду или `TemporaryError` (ретраи
  `tasks`), задание забывается, чтобы ретрай отправил новое; нет результата к `Deadline` (`EDITOR_JOB_TIMEOUT`) →
  таймаут. Модель: `cannot`/`unclear`/дважды невалидно → у правки `PermanentError` с текстом (прошлый черновик
  остаётся), у первой версии — автоматическая склейка и пометка в описании; API не принял ключ, `max_tokens`, лимит
  шагов → технический текст + «Техничка»; сеть/429/5xx → `TemporaryError`. Временный сбой рендера после решения →
  та же стадия `render` заново (`Board.Retry`), без Whisper и модели, но не на последней попытке
  (`ExecuteData.LastAttempt`: тогда задание забывается). У задания — отпечаток текста правки (`Job.RevisionHash`):
  задание или результат версии от другой (проваленной) правки забывается, а не отдаётся новой. Воркер пропускает
  доставку и отбрасывает результат, если на доске уже другое задание (стадия или `CreatedAt`) или задания нет вовсе
  (app забыл его: нет воркера, таймаут, провал — запись в stream при этом остаётся). Результат и старт пишутся
  только для задания, которое сейчас на доске (Lua-проверка вместе с записью: `Board.Finish`/`MarkStarted` →
  `editjob.ErrStale`); задание, исчезнувшее с доски во время работы, отменяется на ближайшем heartbeat блокировки
  (`keepLock`, ошибка Redis не отменяет). `EDITOR_QUEUE_TIMEOUT` + `EDITOR_JOB_TIMEOUT` < 3,5 суток (TTL записей — 7). Проверка —
  `mediaworker.TestIntegrationForgottenJobIsSkipped`. У правок технические тексты свои — без
  «пришлите исходник». Остановку app ничего не держит.
- `internal/mediaworker`: `Worker` берёт задания по одному, под блокировкой; ack — только после записи результата;
  у задания с результатом — ack и повторное уведомление; заблокированное оставляет до повторного claim (через 2 TTL),
  чтобы не потерять задание умершего воркера. `Pipeline`: скачать → probe и лимиты → Whisper (или транскрипт из S3 у
  `render`) → `roughcut.Cut(PlanOnly)` → `subtitles.BurnSubtitles` с `Cut` (склейка, субтитры и экспорт под Instagram —
  один проход ffmpeg) → загрузить рилс, план, лог решений (с решениями модели) и транскрипт исходника.
  Скорость рендера (T3.11): частота черновика ≤ `roughcut.MaxFrameRate` (30; 60 → 30, 59.94 → 29.97, 50 → 25 —
  `targetFrameRate`), по ней же `SnapToFrames`, `trim` по кадрам и субтитры; в графе `fps` стоит до `split`, кадр
  уменьшается до 1080×1920 сразу после склейки, а тонмаппинг HDR (zscale, float32) идёт **после** scale — на 4K он
  самый дорогой фильтр; x264 `veryfast` CRF 20. Замер — `subtitles.TestRenderSpeed` (`RENDER_BENCH=1`, job media-image,
  `--cpus=2`), цвет против старого порядка — `TestBurnSubtitlesWithFFmpegToneMap` (HLG и PQ). Исправленные
  слова субтитров (`Edit.Words`) меняют только субтитры, план строится по исходным словам. Причины отказа —
  `editjob.FailureCode`.
- Режим монтажа (T3.10): `roughcut.Edit.Mode` — `talk` (пусто, как раньше) или `workout`: `BuildPlanEdited` в
  workout не режет паузы, паразиты и дубли, только явное (`Edit.Remove`, `Edit.CutTime` — вырезанное по времени
  исходника, принятые кандидаты); кандидаты ищутся одинаково в обоих режимах (ID стабильны). Режим, `CutTime`,
  подписи упражнений (`Edit.Titles`, время исходника, `End` 0 — до следующей) и `Edit.Mute` накапливаются по версиям,
  как остальная правка. Эвристика в воркере на стадии `auto`: `Plan.SpeechShare` (доля исходника, которую
  оставляет автоматическая разговорная склейка; та же цифра в промпте модели); если она меньше
  `MEDIA_WORKER_MIN_SPEECH_SHARE`, разговорный план не строится (`ErrInvalidPlan`) или слов нет — план строится в режиме workout и задание завершается
  `planned` (модель спрашивается и без подписи, «нет речи» больше не отказ); без модели (технический сбой, перегрузка на
  последней попытке) рендерится этот план целиком с пометкой. Подписи: `roughcut.RemapTitles` переносит их на время
  рилса, `subtitles` рисует стилем `title` из `style.yaml` (у края, противоположного субтитрам; валидация — не
  перекрывать субтитры и зоны Instagram); текст проверяет `editplan` (≤ 40 символов, без управляющих/Cf-символов и
  `{}\<>`), ASS экранирует. `Edit.Hide` (`hide_words`) — слова, скрытые только в субтитрах (`roughcut.SubtitleTranscript`),
  видео и звук не режутся; посторонние фразы распознавания в тренировке убираются так, а не `remove_words`. Описание
  черновика ≤ 920 UTF-16 (`editor.fitDescription`): заголовок и пометки первыми, хвост списков режется с пометкой. `Mute` — `volume=0` в графе (дорожка остаётся), субтитров речи нет. Проверка с ffmpeg —
  `mediaworker.TestIntegrationWorkoutKeepsPauses` и `TestIntegrationNoSpeechIsPlanned`. Черновик длиннее
  `EDITOR_REELS_MAX_DURATION` — пометка в описании.
- Редакторский отбор (T3.16a): `editjob.Job.TargetDuration` (`EDITOR_REELS_TARGET_DURATION`, по умолчанию `0` — отбор выключен, ролик
  сокращается только по длине из подписи или правки через `target_seconds`; при `60s` — стадия `auto`
  разговорного ролика (не мало речи), чей чистовой план длиннее цели × `roughcut.TargetMargin` (1,1), завершается
  `planned` и без подписи/кандидатов; app решает по той же `roughcut.LongerThanTarget`.
  `editplan.Request.TargetSeconds` → `Selecting()` (первая версия, talk, длиннее цели с запасом; диапазон `TargetRange` —
  половина цели…цель): промпт просит отбор, `Apply` (`buildSized`) требует длину в диапазоне ±15 %; `target_seconds`
  в `submit_edit` — длина, названная тренером (подпись или правка, любой режим), ±15 %. Отбор — только `remove_words`/
  trim, без перестановки и рендера. `Outcome.Selected` (только если ролик стал короче хотя бы на 0,5 с; «не сокращай» — `target_seconds` = текущая
  длина) → `Summary.Selected` (заголовок «сократил под рилс», строки
  «Оставил главное…», «Не вошло…» вместо строки на каждый фрагмент). Без решения (сбой — `fallBack` как у тренировки,
  или `cannot`/`invalid`/нет ответа) рендерится чистовой план с пометкой `longFailedNote`/`longUnchosenNote`. `Edit.Notes` —
  решения (их считает «Решения монтажёра: N»), `Edit.Details` — служебные строки (длина, невошедшие фразы, нерешённые
  повторы, обе фразы которых в ролике). `fix_words.word` — слово по индексу, как его прочитала модель: не то — ошибка
  проверки. Промпт: расшифровка по фразам
  (`editplan.phrases`: интервал или причина выреза, сегмент, пауза > 0,7 с, ≤ 8 слов; начало, длительность, начало в
  ролике) с диапазонами индексов слов.
  Проверка — `mediaworker.TestIntegrationSelectionForLongTalk`.
- Заголовок-хук (T3.15b): `roughcut.Edit.Hook` (накапливается по версиям, `omitempty`), `hook_title` в `submit_edit`
  (текст — поставить/поменять, `""` — убрать, нет поля — оставить; `editplan.checkHook`: 1–14 слов, ≤ 90 символов, без
  управляющих/Cf-символов, `{}\<>`, эмодзи и `#`; обрамляющие кавычки и точка в конце снимаются — `normalizeHook`). `EDITOR_HOOK_TITLE` (по умолчанию `true`) → `editjob.Job.Hook`:
  стадия `auto` всегда завершается `planned`, `editplan.Request.Hook` просит модель предложить хук.
  `Request.HookOnly()` (первая версия talk без подписи, спорных повторов и отбора): промпт «предложи только
  заголовок-хук», `Apply` отклоняет вырезы и обрезку; без модели — `fallBack`, черновик без заголовка. Модель не
  поставила хук (без подписи) — последняя строка описания `editor.noHookHint`. Воркер передаёт
  `Plan.Edit.Hook` в `subtitles.CreateSubtitlesData.Hook`: событие стиля `hook` (Manrope Regular 60, белый, по центру; слой 2,
  `\fad`, ≤ 4 строк с переносом по словам и `\fscx`; ключевое слово `highlight` и плашка `box` — опции, выключены;
  текст без управляющих/Cf, ≤ 90 символов по словам) с 0 до min(длина ролика, clamp(конец первого сегмента + 0,5 с,
  `hook.min_duration` 2,5, `hook.max_duration` 8)); субтитры, начавшиеся до конца хука,
  получают `MarginV` под ним на всё своё время (`Style.hookSubtitlesMargin` по фактическому числу строк хука), а под
  ними — подложка `Shade` (первое событие, слой 0, векторный прямоугольник с `\blur` и прозрачностью виньетки) до
  конца последнего сдвинутого субтитра; граф ffmpeg не меняется. Валидация стиля — хук и сдвинутые субтитры не
  пересекают друг друга, подписи упражнений и зоны Instagram. Строка описания —
  первой в `Outcome.Changes` (`editplan.hookLines`). Проверка — `mediaworker.TestIntegrationHookTitle`,
  `TestIntegrationHookRender` (без Redis/S3, в media-image), `subtitles.TestBurnHookWithFFmpeg`.
- Оформление как у референсов (T3.17). Стиль по умолчанию — `layout.mode: word` (`subtitles.DefaultStyle`;
  прежний — `PhraseStyle`, файл `assets/subtitles/style-phrase.yaml`, тесты фразовой раскладки берут его явно):
  `subtitles.wordCues` — по слову (число с единицей — одним событием: `joinThousands` склеивает «10 000»,
  `markNumberGroups(…, true)` размечает и группу сразу после группы, новая группа начинается с числа; тире не
  показывается; слово, за которым следующее
  начинается раньше `word_box.min_duration`, — вместе с ним, но не через паузу), строчными, без пунктуации
  предложения; событие держится до следующего (`timeWords`), стиль `Word` — BorderStyle 3 (плашка = outline цвета
  `word_box.box` вокруг глифов, поэтому всегда по слову) с `\an7` у `word_box.x/y`, `\q2`, длинное слово — `\fscx`.
  Виньетки и сдвига под хук в word нет (`shade` → nil, `hookSubtitlesMargin` → 0), хук всегда сверху, подписи
  упражнений — снизу. Hide/fix слов — до субтитров (`roughcut.SubtitleTranscript`), в обеих раскладках.
  Пункты на экране: `roughcut.Edit.ScreenNotes` (`list_title`/`list_item`/`callout`, время исходника, `omitempty`,
  накапливаются; `screen_notes` в `submit_edit` — весь список версии, `[]` убирает, нет поля — оставить;
  `editplan.checkNotesInput`: ≤ 12, ≤ 40 символов, как хук без разметки/эмодзи/`#`; только talk; `HookOnly` их
  разрешает; новые элементы на вырезанном слове — отказ модели, прежние просто пропадают) → `roughcut.RemapScreenNotes`
  (начало в вырезанном куске — убрать) → `roughcut.Result.ScreenNotes` → `subtitles.CreateSubtitlesData.Notes`.
  `subtitles.noteBlocks`: элемент ждёт конца хука; пункты копятся под заголовком до конца списка (свой `end`, следующий
  не-пункт или 5 с после последнего), ≤ `hookLines` строк — ранние пункты уезжают; выноска — одна на 1,5–3 с; блок —
  событие стиля `Hook` (слой 2), `\fad` только на краях серии. Во фразовой раскладке субтитры под блоком сдвигаются как
  под хуком, подложки хука и блоков — `shadeEvents` (не перекрываются). Описание — `editplan.notesLine` сразу после
  строки хука. Ник и карточка: `REELS_HANDLE`/`_ICON`/`REELS_END_CARD` (`config.Editor`, app) → `editjob.Job.Branding`
  (проверка `editjob.CheckHandle` `^@[a-z0-9._]{1,30}$`, `subtitles` вызывает её же — `fx.Invoke(checkBranding)` при старте app и
  `Pipeline.branding` в воркере: невалидный — лог и черновик без ника) → `subtitles.Branding`: `brandEvents` — стиль
  `Handle` (`\an9`, слой 3) с векторным значком (кольца — внешний и обратный внутренний контур с заливкой: libass
  вырезает заливку из обводки, поэтому не `\bord`) до начала карточки; карточка (`end_card.duration` 2 с от
  `Transcript.Duration`, не больше трети ролика и только если до неё ≥ 2 с — `cardTimes`, слои 4–5): затемнение
  векторным прямоугольником с `\fad`, значок и ник `\an5` по центру; слова, субтитры и пункты обрезаются по её началу
  (`clipWords`, `clipCues`). `handle.y` 0 — авто (`Style.handleY`: 620 в word, 840 во фразовом, 700 при субтитрах в
  середине). Тени: `hook.shadow` (word — 3 px 75 %), `handle.shadow` (ник, значок и карточка). Валидация стиля: плашка, хук/пункты (4 строки), подписи, ник
  (`handle.max_width` справа, выше кнопок Instagram — `rightButtonsTop` 1000) и фразовые субтитры не пересекаются.
  Проверка — `subtitles.TestBurnReferenceStyleWithFFmpeg` (пиксели; в media-image), `mediaworker.TestIntegrationScreenNotes`,
  скорость — `TestRenderSpeed` (референсный стиль против фразового).
- Звук и зумы (T3.15a): `ffmpeg.Runner.ExtractAudioMeasured` меряет громкость исходника (`ebur128`) в том же проходе,
  что извлекает звук для Whisper, → `transcription.Result.Loudness` (хранится в транскрипте исходника, render и правки
  не меряют заново). `mediaworker.soundSettings.cut` (`MEDIA_WORKER_LOUDNESS`/`_DENOISE`/`_ZOOM`) добавляет к
  `roughcut.Result.Render`: `RenderOptions.Denoise` (только talk; `apad` → `highpass` → `afftdn` с порогом
  `ffmpeg.NoiseFloorFor` = громкость − 36 dB → `atrim`: задержка `afftdn` 25 мс компенсируется) и `Loudness`
  (`ffmpeg.LoudnessTarget`: −14 LUFS; с измерением — `volume` на цель − измерение и `alimiter` −2 dBTP с `latency=1`;
  без него — `loudnorm` в один проход; исходник тише −50 LUFS и `Mute` — без выравнивания) — цепочка после `concat`
  (`soundChain`); `ffmpeg.Cut.Zoom` (только talk) — jump-cut zoom на кадре 1080×1920 после fit/тонмаппинга, до тени и
  ass: `split` → `crop`+`scale` → `overlay` с `enable` по отрезкам рилса (`zoomGraph`, кусок < 1,2 с не меняет кадр).
  Строки «Звук и кадр» дописываются в `decisions.log` после рендера. Проверка с ffmpeg — `TestRunnerLoudness`,
  `TestRunnerSoundSync` (вспышка и щелчок через склейки), `TestRunnerZoom` (кадры на стыках); скорость —
  `TestRenderSpeed` (talk с T3.15 и без на одном раннере).
- Черновик-видео: `teamchat.Notifier` скачивает рилс из S3 в `TELEGRAM_OUTBOX_DIR`, шлёт `sendVideo` от бота
  Монтажёра в «Продакшн» (ссылка на исходник вместо реплая между топиками), удаляет файл. Отправка видео прерывается
  стопом (аренда доставки 20 мин).

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
