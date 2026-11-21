# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- `docs/MVP-BACKLOG.md` and `docs/PLAN.md`: follow-up (technical debt) tasks with priority and deadline — T1.6 `KB_ROOT`
  from `.env` for `make kb-*`, T1.7 integration tests of the Telegram gateway's Redis store on `internal/redistest`,
  T3.6 replacing `whisper-1` before OpenAI shuts it down on 2027-02-26 (main candidate ElevenLabs Scribe, compared on
  Russian speech first).
- Telegram ⇄ tasks (M1, `internal/teamchat`): the gateway's events become tasks and decisions, drafts come back to
  the group. A text message creates a task for the target role's agent (key: chat id + message id; the draft goes
  to the message's topic) and gets a 👀 reaction (in the background, 3 s timeout) instead of an extra reply;
  a forwarded text reaches the agent in a `<forwarded_message>` block marked as data. The editor and the designer
  are not connected yet: they answer with a fixed text, without an agent run, a draft or buttons. A reply to any
  part of the latest draft is a revision, idempotent by the reply message; another reply while the new version is
  being made gets «Уже готовлю новую версию…», a reply to an approved, rejected or failed draft an explanation, to
  an old draft «Ответьте на последний черновик», to any other message — a new task. «Одобрить / Отклонить» are
  answered right after the decision is stored (the draft is closed in the background), «Правки» asks to reply with
  the revision; a button counts only on the draft message of its task and version. `/status` (tasks by status,
  LLM usage for 7 days), `/plan` and `/idea <text>` (marketer tasks of kind `plan` / `idea`) are the marketer's;
  `/pause` answers that it is not supported yet, other commands get the list of commands. Voice messages, videos
  and photos get an honest «not processed yet» answer at once: the gateway does not download them. Errors reach
  the trainer as `tasks.UserMessage`. `teamchat.Notifier` sends drafts from the agent's bot with the buttons on the
  last part of a long draft («Версия N» heads revisions), removes the buttons of a decided draft and answers it
  with «Одобрено.» / «Отклонено.» / «Принял правку…» (a duplicate only loses its buttons), and tells about failed
  tasks. Without bot tokens the notifier is the log one, as before.
- Migration `005_draft_parts`: `task_versions.draft_part_message_ids` (earlier parts of a long draft; a failed part
  makes the next delivery send all parts again, and the parts already sent become old messages of the version) and
  `task_feedback.source_message_id` (a revision remembers its reply message).
- `tasks.ErrDraftNotReady`: a reply to a part of a draft that is still being sent; a button pressed before the draft
  is stored gets «Черновик ещё сохраняется, нажмите ещё раз». `gateway.Event.ReplyToRole` and `Event.IsCommand`
  (a `bot_command` entity at the start): only such messages are commands, so «/2 подхода убрать» stays a revision.
- On stop the gateway and the workers stop before the task service and the team chat handler wait for their
  background work (the wait hooks are registered in the constructors).
- Team agents `marketer` and `copywriter` (working drafts with `search_knowledge_base` and `brand-knowledge`; the
  brand voice comes in T2.1).
- `gateway.Sender.SendTextParts` (IDs of all parts) and `gateway.MediaFilter` (a handler that does not process files
  yet: the gateway does not download them).
- `gateway.Sender.React` and the Bot API client's `setMessageReaction`.
- Agents can search the brand knowledge base: the `search_knowledge_base` tool (`internal/knowledge/kbtool`) is registered
  in `agents.ToolsGroup` in the app and `agent-run`; an agent gets it only when `tools:` of its `agent.md` lists it.
  Fragments come in `<fragment source="…" title="…" kind="…" score="…">` tags with `<` and `&` escaped in the content
  and one-line, length-limited attributes, so a fragment cannot break out of its tag; the model is told that fragments
  are data, not instructions. A call is limited to 30 seconds, `limit` 1–20 (default 5) and about 16 000 runes of
  fragments with titles and sources: short fragments stay whole, long ones share the rest and are cut at a word
  boundary. Bad arguments, timeouts and database/embeddings errors reach the model as an `is_error` tool result
  (infrastructure errors are also logged); "nothing found" is a normal result.
  Without `OPENAI_API_KEY` the app starts, the tool answers that the knowledge base is unavailable and the registry
  warns on start about every agent that lists it (tools may implement `Available() error` for this).
- Shared skill `brand-knowledge`; the `agents/_example` template lists `search_knowledge_base` and the skill.
- Telegram gateway (T1.2, `internal/gateway`): four team bots (marketer, copywriter, editor, designer) in one service
  through the local Bot API server. Only the team supergroup (`TELEGRAM_GROUP_ID`) and users from
  `TELEGRAM_ALLOWED_USER_IDS` are heard, everything else is silently ignored. Messages are routed to a role by
  @mention, reply to a bot, then topic («Исходники» → editor, the rest → marketer); `/plan`, `/idea`, `/status`,
  `/pause` (also `/cmd@bot`) are passed as commands. Text, voice, audio, video, video notes, video documents and
  photos are accepted; media up to `TELEGRAM_MAX_FILE_SIZE` (2 GiB) are downloaded with `getFile`, and the path is
  accepted only inside `TELEGRAM_FILES_DIR` (no `..` or symlinks out, via `os.Root`); consumers read it with
  `Media.Open`, the path with the token is not exported. Messages on behalf of a chat and from bots are ignored,
  forwarded ones are marked `Forwarded`. Only the marketer receives group messages, the other bots only their
  button callbacks, handled in their own pool; events are marked in Redis (unmarked if the handler fails) and
  offsets are kept in Redis, so a restart does not handle an update twice. A handler panic is logged and does not
  stop the process. A bot whose token is rejected is stopped, the others keep working. Events go to
  `gateway.Handler` (default: a short acknowledgement in the same topic until the task queue is connected);
  `gateway.Sender` sends text (split above 4096 characters), buttons, video/photo/document only from
  `TELEGRAM_OUTBOX_DIR` (created on start, 0700), and edits on behalf of a role. Polling never waits for a busy
  handler: accepted events queue (up to 256) for their pool, and the marketer starts polling once the other bots
  are connected. Without bot tokens the gateway is
  off and the app starts as before.
- Bot API client (`internal/providers/telegram`): getMe, getUpdates, sendMessage, edit, answerCallbackQuery,
  sendVideo/sendPhoto/sendDocument streamed from a local file, getFile; retries 429 with `retry_after`, 5xx and network
  errors; the token is masked in errors and never logged, requests go only to the configured address (no proxy
  from the environment, no redirects).
- Tasks and approval (T1.3, `internal/tasks`): tasks idempotent by the source message (chat id, message id),
  statuses `queued → running → awaiting_approval → approved | rejected`, a revision queues the task again
  (version + 1), `failed`; a failed revision sends the previous draft again with the reason. Transitions are
  validated and applied under `SELECT ... FOR UPDATE`; every run has a random `run_id`, so only its worker stores the
  outcome (migration `004_tasks`: `tasks`, `task_versions`, `task_feedback`). Redis Streams queue with a consumer
  group, `XAUTOCLAIM` of dead workers' messages, delayed retries with exponential backoff, a sweeper that re-queues
  what the queue lost without duplicating it, graceful shutdown that keeps finished runs (`TASKS_*` config).
  Refusals and max_tokens are not retried; a panicking agent does not crash the process. Draft delivery is claimed
  atomically, at least once (a duplicate loses its buttons), and retried up to `TASKS_DELIVERY_ATTEMPTS`. Approve / revise / reject with
  `callback_data` `t:<id>:<version>:<a|r|x>`, revision by a reply to the draft; stale versions and repeated presses
  are refused. The previous version and the revision reach the agent tagged as data. Approved revisions are saved
  to the knowledge base as the trainer's corrections. The trainer sees error classes, not internal errors.
  `StatusReport` for `/status` (tasks by status and LLM usage). Telegram is connected through `tasks.Notifier`;
  until then drafts go to the log.
- `make test-integration` starts a throwaway Redis for queue tests and removes it at the end.
- `docs/M0-MANUAL.md`: step-by-step manual check of the stack, LLM layer, knowledge base, transcription and Bot API with real keys.
- Docker Compose stack: app, one-shot `migrate` service, PostgreSQL with pgvector, Redis (password-protected), RustFS (S3);
  local Telegram Bot API server under the `telegram` profile. Only app and postgres are published, on 127.0.0.1 only.
  All images are pinned by digest.
- Multi-stage Dockerfile with a distroless non-root runtime image.
- Providers for PostgreSQL (pgxpool), Redis (go-redis) and S3 (minio-go) with connection checks on start.
- SQL migrations with golang-migrate (`migrations/NNN_name.up.sql` / `.down.sql`): `make migrate` (up) and
  `make migrate-down` (one step); applied only by `cmd/migrate`, outside the fx lifecycle and with no start timeout.
- HTTP endpoints `/healthz` (liveness) and `/readyz` (statuses of dependencies registered in the `readiness` fx group).
- HTTP server timeouts and fx start/stop timeouts in config; the app exits with code 1 if the HTTP server fails.
- `TELEGRAM_BOT_API_URL` config for the upcoming Telegram integration.
- Make targets `up`, `down`, `logs`, `migrate`, `migrate-down`; tests run with `-race`.
- Transcription (T3.1): `transcribe -in video.mp4 [-lang ru] [-out result.json]` prints JSON with the text, segments and
  word timestamps (seconds) and an ISO language code. ffmpeg extracts mono 16 kHz MP3 from local files only (`file:`
  inputs, `-protocol_whitelist file`, timeout `FFMPEG_TIMEOUT`); long audio is split in pauses
  (`TRANSCRIPTION_SILENCE_*`) or with an overlap to fit the 25 MB Whisper limit and merged without repeating
  the overlap. Segments Whisper hallucinates on silence or music are dropped and logged. The Whisper client retries
  429, 500/502/503 and connection errors before the request is sent; it does not retry timeouts, 504 or failures
  after the body was sent, where the request was likely processed (a retried 5xx still may repeat one). Config:
  `OPENAI_API_KEY`, `OPENAI_BASE_URL`, `WHISPER_*`, read by the command only; without a key it fails before
  preparing the audio.
- Rough cut (T3.2): `roughcut -in video.mp4 [-transcript transcript.json] [-out dir] [-plan-only] [-plan plan.json]`
  removes pauses longer than `ROUGHCUT_MAX_PAUSE` (400ms, margins kept around words), filler sounds from
  `ROUGHCUT_FILLERS` and clearly failed takes: a word broken off and restarted after a pause, or an unfinished start
  said again and continued; the last take is kept. Complete repeats (rhythm, enumerations, a finished sentence said
  again) are not cut but listed as `candidates` for the editor, each with its words and the ready cut interval;
  `-accept 0,2` (`CutData.AcceptCandidates`) rebuilds the plan with the chosen ones cut. Writes `plan.json` (edit decision list: kept
  intervals, cuts with reasons, take decisions, candidates; editable and rendered again with `-plan`, which needs
  `-transcript`), `decisions.log` (Russian, why every take was removed), `transcript.json` (T3.1 format on the new
  timeline, segment punctuation kept and moved or capitalized around cut words, for subtitles) and `roughcut.mp4` (H.264 + AAC, source resolution and
  rotation). The source is decoded once (`split`/`asplit`), converted to its nominal frame rate kept as a rational
  (30000/1001) and cut by frame numbers on the audio timeline (audio and file `start_time` respected), so picture, sound and subtitles stay in sync on
  variable frame rate phone videos; the filter graph goes to ffmpeg as a file; at most 500 fragments; 10 ms audio
  fades at joints. A video without sound is refused before anything is written; an old `roughcut.mp4` is removed
  before a new run writes its results. Without `-transcript` it transcribes the video first. `make roughcut`; the
  binary is in the `media` image (`--entrypoint /app/roughcut`); CI renders synthetic clips on the runner and inside
  the image, and checks rotation, a video without sound and peak memory (1080x1920, 120 s, 60 fragments, cgroup peak < 800 MiB).
- Subtitles in the brand style (T3.3): `subtitles -in video.mp4 -transcript result.json -out reel.mp4` burns Reels
  subtitles (ASS via libass, 1080×1920, H.264 + AAC; other sizes padded or cropped with `-fit`), `-ass-only` writes
  only the `.ass`. The video is rendered to a temporary file next to the output and renamed on success. Short 1–2 line
  subtitles broken at sentence ends, segment ends, pauses and commas (not before a short tail, which is glued back),
  prepositions kept with the next word, numbers with their unit, counting never glued; punctuation aligned from Whisper
  segments per segment by time; timing within 0.1 s of the speech, no overlaps. Key words (numbers with units, the
  trainer's dictionary, otherwise the longest word of 7+ letters, not in two subtitles in a row) in an accent font and
  color, on a line of their own in a long phrase, or an explicit list (`-accents`); optional karaoke highlight.
  Default look: small ivory Manrope Medium, warm yellow Fira Sans Extra Condensed ExtraBold capitals, no outline,
  a top vignette (even 60 % under the subtitles, then a fade) and a light shadow, at the top below the Instagram bar. Style from YAML (`-style`,
  `SUBTITLES_STYLE_FILE`; default in `assets/subtitles/style.yaml`), validated against the Instagram interface zones.
  Fonts (SIL OFL) in `assets/fonts`, in the media image at `/app/fonts`; a font fallback by libass is reported
  (exit code 2). `make subtitles`; `subtitles.Service` for the Montage agent.
- Docker target `media` (alpine + ffmpeg 8.1, non-root, `WORKDIR /work`) with the `transcribe` binary;
  `make transcribe`, `make media-image`.
- CI: ffmpeg integration tests in `check`; `media-image` job builds the media target, runs the ffmpeg tests inside it
  and extracts audio from a generated clip; it also renders subtitles inside the image (tests and the CLI) and checks
  the output size, codecs, text position and that the brand fonts were used.
- CI: `tidy` job that commits `go mod tidy` results to `feature/**` branches and re-runs checks on the new commit;
  `compose-smoke` job; actions pinned by SHA with least-privilege permissions.
- LLM layer: thin Claude Messages API client (`ANTHROPIC_*` config, retries on 429/5xx with exponential backoff);
  the app starts without `ANTHROPIC_API_KEY` and the Claude API is not a `/readyz` dependency.
- Agent registry: an agent is a folder `agents/<name>/` with `agent.md` (YAML frontmatter + role prompt);
  validated on start (`AGENTS_DIR`). Directories starting with `_` are templates and are not loaded:
  `agents/_example/`.
- Shared skills `skills/<skill>/SKILL.md` (`SKILLS_DIR`), listed by agents in `skills:`; per-agent additions to a
  skill in `skill_overrides` of `agent.md`, appended when the agent loads the skill. Optional local skills in
  `agents/<name>/skills/`; a local skill named like a shared one, or an override of an unlisted skill, fails the start.
- Project safety rules (`agents.SafetyRules`) in the system prompt of every agent.
- Agent runtime: tool use loop with a turn limit, built-in `load_skill` tool (skill bodies are loaded on demand),
  tools registered through the fx value group `agent_tools`, tool errors returned to the model as `is_error`.
- LLM usage accounting: migration `002_llm_usage`, a record per API call (tokens, cache tokens, cost, duration,
  request id), prices per model in `LLM_PRICES`, `llmusage.Service.UsageSummary` by agent and model.
- `cmd/agent-run` CLI and `make agent-run AGENT=... PROMPT=...` for manual checks with a real key.
- Agent runs handle every stop reason: `max_tokens` (and a full context window) returns `ErrMaxTokens`, `refusal`
  returns `ErrRefusal`, an unknown one `ErrInvalidResponse`, each with the partial result whose history has no
  unanswered `tool_use`; `pause_turn` is continued.
- Usage keeps both the requested and the response model; the cost is calculated by the requested one. Usage is
  recorded even if the run's context is cancelled after a billed call.
- Integration tests against PostgreSQL from compose (`internal/pgtest`, `INTEGRATION=1`, `make test-integration`),
  run in the CI job `compose-smoke`.
- CI: `check` requires new migrations to be numbered above the latest migration in `main`.
- `docs/SETUP.md`: setup from scratch on macOS and a Linux VPS, reference of every `.env` variable, where to get the
  API keys, `KB_ROOT` layout, start/stop/reset and common problems.
- `make m0-check` (`scripts/m0-check.sh`): end-to-end M0 check of the environment, the stack (`/readyz`, migrations),
  an agent run, knowledge base ingest/search/delete, transcription (`M0_VIDEO=...`) and the Telegram Bot API
  (optional `getMe` of a test bot with `M0_BOT_TOKEN`); steps without keys are skipped. It never prints secrets,
  masks them in its logs, reads `.env` without executing it, and removes only what it created: a uniquely named
  temporary agent, folder in `KB_ROOT` and knowledge base document (an existing one is a FAIL, never touched),
  also on Ctrl-C (exit code 130). Summary in `m0-results/report.md` (with LLM usage and image versions). CI runs it
  without keys in `compose-smoke`, after shellcheck.

- Brand knowledge base (`internal/knowledge`): documents split into chunks with embeddings (pgvector, HNSW, cosine),
  search with kind filter (HNSW iterative scan) among vectors of the current embedding model; ingest is idempotent and
  re-indexes a document when its text, the embedding model or the chunker version changes (advisory lock per source);
  limits of 1,000,000 characters and 1000 chunks per document. Trainer's corrections with exact similarity search per
  agent and task kind for few-shot examples; a correction is saved even when embeddings are unavailable.
  `search_knowledge_base` tool function for agents. Migration `003_knowledge_base`.
- Text extraction from `.md`, `.txt`, `.docx` (headings, lists and text boxes kept) and `.pdf` (text layer only), files
  up to 32 MB. PDF parsing is hardened against hostile files: own page-tree walk (at most 1000 pages, no trust in
  `/Count`, `/Kids` cycles rejected), streams inflated through a limit, glyph limits, 30 s timeout.
- Embeddings client for the OpenAI Embeddings API (`EMBEDDINGS_MODEL`, default `text-embedding-3-small`) with batching
  (at most 2048 inputs) and retries on 429/5xx and network errors (honouring `retry-after-ms` / `Retry-After`; no retry
  on `insufficient_quota`); the app starts without `OPENAI_API_KEY`. Shared `OPENAI_API_KEY` / `OPENAI_BASE_URL` config.
- `kb` CLI (`cmd/kb`): `kb ingest <path> [-kind brand]`, `kb search "<query>" [-limit 5]`, `kb delete <source>`,
  `kb reembed` (fills in missing vectors and moves documents and corrections to the current model); make targets
  `kb-ingest`, `kb-search`, `kb-delete`, `kb-reembed`.

### Changed

- The application uses `teamchat.Module` instead of `tasks.LogNotifierModule`: the gateway no longer answers with
  the «Принял…» acknowledgement (`gateway.AckHandler` stays the default of the gateway module alone).
  `tasks.CloseDraftData` has the draft's `TopicID`; `tasks.Notifier.SendDraft` returns `tasks.SentDraft` (the
  message with the buttons and the earlier parts); `Approve`, `Reject` and `RequestRevision` close the draft in the
  background.
- The app and `agent-run` now parse `EMBEDDINGS_*` (defaults as before) and include the knowledge base service:
  an invalid value fails their start.
- The knowledge base CLI `kb` is in the app image (`/app/kb`): `make kb-ingest|kb-search|kb-delete|kb-reembed` run it
  through `docker compose run` (`scripts/kb.sh`), so Go is no longer needed on the host. `./kb` is mounted read-only at
  `/kb` (`KB_ROOT=/kb`), `KB_PATH` is given as on the host and must be inside `./kb`; the container runs as the host
  user's uid:gid (never root). `KB_HOST=1 make kb-*` keeps the `go run` on the host. Before the run the script stops
  with a hint if the image is not built or postgres is not running, and warns when the code changed after the image
  was built. `kb search` and `kb delete` accept `--` before the argument (`make kb-search Q="-5 кг"`). CI checks the
  paths, the `extract-pdf` child process inside the distroless image and a full ingest without the OpenAI key.
- `TELEGRAM_BOT_API_URL` defaults to the compose service `http://telegram-bot-api:8081`; `app` in compose gets the
  supplementary group `101` of the Bot API server, which creates downloaded media with mode 0640.
- `/readyz` lists `telegram`: it fails only when Telegram rejected a bot token (401).
- `make m0-check` masks the bot tokens (`TELEGRAM_*_TOKEN`) in its logs.
- `kb ingest` takes files only from `KB_ROOT` (default `./kb`, ignored by git and docker): a document's source is its
  path relative to it, so the same file always gets the same source and same-named files of different folders no
  longer overwrite each other; a file outside `KB_ROOT` is an error. Changing the kind of an existing document needs
  `-force`; without `-kind` the stored kind is kept. Files are walked and read through `os.Root`: a symlink leading
  outside `KB_ROOT` is never read (skipped in folders, an error when named).
- `pgtest.Connect` gives every integration test its own temporary database with all migrations applied and drops it
  afterwards, instead of truncating tables of `POSTGRES_DB`; `pgtest.Truncate` is removed and integration tests may
  run in parallel.
- CI: the migrations check in `check` also fails on a changed, deleted or renamed merged migration
  (`git diff --no-renames --name-status`); `make test-integration` runs verbose (`-v`, streamed through `tee`,
  with `HOST_DB_ENV`) and fails if a test is skipped with `INTEGRATION=1` or no integration test passed; the rule
  is documented in `internal/pgtest`.
- Agent runs: a history ending with an assistant message and no new input is `ErrInvalidRunData` (no accidental
  prefill); `ErrIterationLimit` returns the partial result (history, tokens) like `ErrMaxTokens`, and `agent-run`
  marks a partial answer with its reason; a listed skill in a `_` directory reports that such directories are skipped.
- README: how to try the LLM layer with a copy of `agents/_example` in `agents/test` (ignored by git).
- Migration file naming in CODE_STYLE.md: `NNN_description.up.sql` + `NNN_description.down.sql`.
- `/readyz` dependencies are registered in `internal/readiness`; providers no longer import `internal/health`.
- docker compose builds the app image once (in `migrate`); `app` reuses it.
- `make run` reads `.env.local` after `.env` to run the app on the host (see README).
- CI: `check` requires the branch to contain the current main; `tidy` also commits to `fix/**` and `refactoring/**`
  and fails with a ready-to-run command if it cannot dispatch CI on the new commit; `compose-smoke` checks that
  migrate up/down/up ends with `dirty: false`.

### Removed

- `knowledge.RunSearchTool`, `knowledge.SearchTool` and the `knowledge.SearchTool*` constants: the agent tool is
  `kbtool.SearchTool`; `knowledge.FormatSearchResults` stays for `kb search`.

### Fixed

- Transcription: a high Whisper `compression_ratio` alone no longer drops a segment, so counting reps and rhythmic cues
  are kept; such a segment is dropped only when it is also poorly decoded or over silence.
- ffmpeg errors tell the runner's own timeout (`FFMPEG_TIMEOUT`) from the caller's deadline or cancellation.
- PDF parsing runs in a child process (`kb extract-pdf`) with a timeout, `GOMEMLIMIT`, `RLIMIT_AS` on Linux,
  a 64 MB stack and limited output: a crash of the PDF library (stack overflow on a self-referencing object stream)
  or a memory blow-up fails only that file. Inflated content is budgeted at 1 MB per page (repeated references
  counted) and 16 MB per document. The child runs in its own process group (killed as a whole), exits by itself
  5 s after the timeout if orphaned, caches font decoders per font name and refuses ToUnicode maps of more than
  4096 entries (a PDF switching fonts 50,000 times or scanning a huge map no longer burns the whole timeout).
- `kb reembed` takes the ingest lock of the document and writes nothing if the document changed meanwhile.
- `pgtest`: the admin connection is closed when creating the test database fails; database names use a counter;
  test databases older than an hour, left by killed runs, are dropped (without `FORCE`: one in use is kept).
- `cmd/migrate` runs outside the fx lifecycle with no start timeout: a long migration is no longer killed midway
  (leaving the database dirty); SIGINT/SIGTERM stop it gracefully after the current migration.
