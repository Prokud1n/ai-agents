# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

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
- Docker target `media` (alpine + ffmpeg 8.1, non-root, `WORKDIR /work`) with the `transcribe` binary;
  `make transcribe`, `make media-image`.
- CI: ffmpeg integration tests in `check`; `media-image` job builds the media target, runs the ffmpeg tests inside it
  and extracts audio from a generated clip.
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
