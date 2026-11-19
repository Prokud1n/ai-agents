# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

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

### Fixed

- Transcription: a high Whisper `compression_ratio` alone no longer drops a segment, so counting reps and rhythmic cues
  are kept; such a segment is dropped only when it is also poorly decoded or over silence.
- ffmpeg errors tell the runner's own timeout (`FFMPEG_TIMEOUT`) from the caller's deadline or cancellation.

### Changed

- Migration file naming in CODE_STYLE.md: `NNN_description.up.sql` + `NNN_description.down.sql`.
- `/readyz` dependencies are registered in `internal/readiness`; providers no longer import `internal/health`.
- docker compose builds the app image once (in `migrate`); `app` reuses it.
- `make run` reads `.env.local` after `.env` to run the app on the host (see README).
- CI: `check` requires the branch to contain the current main; `tidy` also commits to `fix/**` and `refactoring/**`
  and fails with a ready-to-run command if it cannot dispatch CI on the new commit; `compose-smoke` checks that
  migrate up/down/up ends with `dirty: false`.

### Fixed

- `cmd/migrate` runs outside the fx lifecycle with no start timeout: a long migration is no longer killed midway
  (leaving the database dirty); SIGINT/SIGTERM stop it gracefully after the current migration.
