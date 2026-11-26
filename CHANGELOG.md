# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- Screen notes of a talking head (T3.17). The editor's model puts what the trainer lists or names on the screen
  (`screen_notes` of `submit_edit`: a list title «3 вещи», list items «1/ на русском», callouts «6 лет»; at most 12, 40
  characters, from the speech only, no markup, emoji or hashtags; a start by a word or seconds of the source, an
  optional end; the whole list per version, an empty one removes it; talk mode only, allowed in a reel stopped only for
  the hook). They add up over the versions (`roughcut.Edit.ScreenNotes`), go onto the reel's timeline with the cut
  (`roughcut.RemapScreenNotes`: a note that starts in a cut piece is dropped) and are drawn in the hook style at its
  place once the hook is gone: the items pile up under the title line by line until the list ends (its end, the next
  note that is not its item, or 5 s after its last item), at most 4 lines (the earliest items scroll away), a callout
  alone for 1.5–3 s; in the phrase layout the subtitles shown with a block stand under it over the extended shade. The
  revisions «убери пункты», «поменяй пункт 2 на …», «добавь выноску … на …» make a new version without the speech
  recognition. The draft says «На экране: 3 пункта и 2 выноски.» or «Убрал пункты и цифры с экрана.», `decisions.log`
  lists them; the refusal texts name the new skill.
- The trainer's handle and the end card (T3.17): `REELS_HANDLE` (app; empty by default, `.env.dist` shows
  `@prokudina.ana` in a comment; strictly `@` and 1–30 of `a-z0-9._` — `editjob.CheckHandle`, checked at the start of
  the app and again by the worker, which gets it in the job, `editjob.Job.Branding`), `REELS_HANDLE_ICON` (true),
  `REELS_END_CARD` (true). The handle is drawn small in opaque white Manrope Medium 32 px with a 2.5 px shadow of 75 %
  black at the right edge on the row of the words over the whole reel (`handle.y` 0: placed by the layout), with a
  simple Instagram icon drawn as ASS vectors (rings of a rounded square and a circle, and a dot) with the same shadow;
  over the last 2 seconds (at most a third of the reel, with at least 2 s before them) the frame darkens (70 % black,
  500 ms fade) and the icon and the handle stand large in the middle; the words and the screen notes end where the card
  starts — the reel is not longer, the sound is untouched. In talking heads and workouts. Sections `handle` and
  `end_card` of `style.yaml`.
- `TestBurnReferenceStyleWithFFmpeg` (in the media image too): the word on its white plate, a callout, the handle and
  the end card checked by pixels; `TestIntegrationScreenNotes`: the hook, two items and a callout, then «убери пункты»
  without the speech recognition; `TestRenderSpeed` logs what the reference style costs against the phrase style.

- `make edit` — the editor without Telegram (T3.18). `make edit FILE=<video> [CAPTION=…] [OUT=./edits/<name>]` runs
  the app's edit of a local video — the editor's model, its decisions, the running media worker — and puts the reel,
  `plan.json`, `decisions.log`, the draft's description and the timings by stage into `OUT/v1/`; `make edit OUT=…
  REVISION="…"` makes the next version as a reply to the draft would. Nothing goes to the group and no task is queued:
  the task is synthetic (ID above 10^18 from a Redis counter), the state is `OUT/state.json`. The summary shows the
  model's calls and their cost for the run (also recorded in `llm_usage`). New binary `cmd/edit-local` in the app image,
  `scripts/edit.sh` (the video is mounted read-only, `OUT` belongs to the user). Ctrl+C takes the job off the worker's
  board; `OUT/.lock` keeps two runs out of one directory (the lock of a killed container is removed by `make edit`); a version made but not downloaded is `pending` in
  `state.json` and `make edit OUT=…` alone downloads it; the source gets a random S3 name and a taken key is never
  overwritten; `~` in paths is expanded, `$` in texts is kept; an app image older than the code asks for `make up`
  (the check is shared with `make kb-*`, `scripts/image-age.sh`).
- Hook title of a reel (T3.15b). The editor's model proposes a hook (`hook_title` of `submit_edit`: an address to the
  audience or a personal fact or thesis from the speech, 1–14 words, at most 90 characters, quotes around it and a
  period at the end dropped, in the language of the reel, no promises the reel does not keep, no emoji, hashtags or
  markup; checked by `editplan`) for every first version, talking head or workout, so with `EDITOR_HOOK_TITLE=true`
  (the default) every auto job stops after the plan (`editjob.Job.Hook`) — one more model call for a short reel that
  had nothing else to decide (~0.03 USD without the prompt cache, ~0.007 USD with it); such a reel is not cut by the
  model (`Request.HookOnly`: `Apply` refuses cuts and trims), and the draft of a first version the model left without a
  hook says how to add one; `false` keeps the old flow. Without the model the reel is rendered without a hook (on
  429/5xx after the task's retries). The media worker draws it with libass in the new `hook` style of `style.yaml`
  (Manrope Regular 60 px — `assets/fonts/Manrope-Regular.ttf`, cut from the same variable font as Manrope Medium —
  plain white with the soft shadow of the subtitles, centered, at most 4 lines wrapped by words, 150 ms fade; a key
  word in color and a plate under the lines are options, off by default) at the subtitles' margin from the start until
  half a second after the first phrase, clamped to `min_duration` 2.5 s .. `max_duration` 8 s and never past the reel.
  The subtitles of that time stand right under it (`MarginV` of their events, by the hook's actual number of lines)
  over an ASS drawing that extends the vignette under the hook and them until the last of them ends (the ffmpeg graph is
  unchanged); the subtitles also drop control and invisible characters of the hook and cut it to 90 characters by
  words. A custom `SUBTITLES_STYLE_FILE` without a `hook` section gets the defaults. The style validation keeps a hook
  of 4 lines and the shifted subtitles clear of each other, the exercise titles and the Instagram zones. The trainer
  changes, adds or removes it by a revision («поменяй заголовок на …», «убери заголовок»): a new version is rendered
  from the stored transcript, and the hook adds up over the versions (`roughcut.Edit.Hook`). The draft says
  «Заголовок: «…».» or «Убрал заголовок.», `decisions.log` names it, `cmd/subtitles` has `-hook`.

- Sound and dynamics of the editor's drafts (T3.15a). The draft is brought to -14 LUFS integrated, whatever the phone
  recorded: the loudness of the source is measured (`ebur128`) in the same ffmpeg pass that extracts the audio for the
  speech recognition and stored in the source transcript (`loudness`); the render and every revision give the sound
  one gain (`volume`, the target minus the measurement) and a peak limiter (`alimiter` at -2.0 dBTP, 5 ms lookahead
  compensated, 50 ms release), so the dynamics of speech stay as recorded and the true peak after AAC is at most
  -1.5 dBTP. A transcript stored before has no measurement and is normalized by `loudnorm` in one pass. A source
  quieter than -50 LUFS (silence, noise) and a muted reel are not raised. A talking head gets a light noise reduction
  of speech first (`highpass` 80 Hz + `afftdn` nr 10 dB, its noise floor 36 dB under the loudness of the recording,
  within -80…-35 dBFS, -50 without a measurement; the 25 ms delay of `afftdn` is compensated, the sound stays in sync
  with the picture); a workout does not (it would eat into the music). A talking head also gets the jump-cut zoom: at
  the joints the framing alternates between the plain frame and one 9 % closer (static, the fixed point at the middle
  across and 40 % down — the face), a piece shorter than 1.2 s keeps the framing; the 1080×1920 frame is zoomed after
  the fit and the tone mapping, under the shade and the subtitles, and in `pad` mode the black bars stay black.
  `decisions.log` ends with «Звук и кадр»: the loudness before → after and how (the gain in dB or `loudnorm` without a
  measurement), the floor of the noise reduction and the number of zooms. New `MEDIA_WORKER_LOUDNESS` (`-14`, `off`),
  `MEDIA_WORKER_DENOISE` (`true`), `MEDIA_WORKER_ZOOM` (`9` %, `0` off). Render speed (`TestRenderSpeed`, 4K60 HEVC
  HLG, 2 CPUs, the talk rendered with and without them on the same runner, 2a1e23a): 3.05 → 3.10 s of render per
  second of the reel, +1.4 % (an earlier run +3.1 %: 3.39 → 3.50); the workout 2.46 s (main: 2.85 on another runner). The synthetic bench does not cover
  the expensive path: its sound is measured (the cheap gain and limiter, not the one-pass `loudnorm` at 192 kHz of an
  old transcript; the zoom costs the same whatever the number of zoomed pieces).
- Editorial cut of a talking head (T3.16a). A first version longer than `EDITOR_REELS_TARGET_DURATION` (60 s) by more
  than `roughcut.TargetMargin` (10 %: over 66 s) after the cleaning is planned by the worker and given to the editor's
  model even without a caption or disputed repeats (shorter ones are rendered at once, no model call; the worker and
  the app decide by the same `roughcut.LongerThanTarget`): the model keeps the best phrases — a hook in the first seconds, one
  thought in finished sentences, the closing thought or call to action — so the reel is 30–60 s, cutting whole
  phrases with `remove_words`/trim (no reordering, no render changes). A length in the caption or a revision («сделай
  40 секунд», «оставь минуту»; «до 30 секунд» as 0.85 of it, «не сокращай» as the current length) goes to the new
  `target_seconds` of `submit_edit`; `Apply` checks the
  reel within ±15 % of it (or of the recommended range), otherwise the model gets the reason once more. The draft of a reel that got shorter says
  «сократил под рилс», how the reel starts and ends and what did not make it; `decisions.log` has the reason and, as
  details not counted among the decisions (`roughcut.Edit.Details`), the length and every phrase left out.
  `fix_words` takes the word as the model reads it at the index and refuses a wrong index. Without a decision the cleaned cut is rendered with a note that the reel is longer
  than recommended. The selection rules are a section of `agents/editor/agent.md`, with a section for the trainer's own
  style to be filled from her best reels.
- Onboarding in the chat (T4.3a). `/help` and `/start` explain what each role does, where to send what, the commands,
  examples of revisions for the editor and how to give feedback; in a topic the part about that topic comes first. On
  start the gateway sets the command menu of the group for the marketer's bot (`setMyCommands`: `/help`, `/idea`,
  `/ideas`, `/idea_done`, `/idea_drop`, `/feedback`, `/status`) and removes the menus of the other three bots, so the
  commands are listed once; a failure is only logged. The service command `/onboard` (`TELEGRAM_ADMIN_USER_IDS`, empty —
  anyone of the allowlist; an admin outside the allowlist is warned about in the log) publishes short greetings in
  «Планёрка» (from the marketer), «Исходники» and «Продакшн» (from the editor) and pins them with the marketer's bot; a
  repeated `/onboard` edits them without a new pin instead of posting duplicates (`onboarding_messages` keeps the
  message, its topic and whether it is pinned); without the right to pin the admin gets an honest answer and
  «Техничка» a note.
- Pilot feedback and metrics (T4.5, `internal/pilot`, migration 008). `/feedback <text>` in any topic is saved
  (`feedback`: author, text up to 2000 characters with a note when cut, topic, and the task and version when it replies
  to a draft), copied to «Техничка» with a link and thanked with «Спасибо, передал команде.»; an empty one gets a
  hint, a forwarded one is refused, a redelivered one is saved and copied once and thanked again. The metrics are
  counted from `tasks`, `task_versions` and `task_feedback` (tasks created in the period, by agent; the `/idea` cards
  apart from the marketer's other tasks): created, approved, approved without revisions (share), average revisions
  asked before the approval, rejected, failed, and for the editor the time from the task to the first draft (median
  and maximum), plus the number of feedbacks. `/status` adds them for 7 days; a weekly report with the week's feedback
  goes to «Техничка» as one message (feedback that does not fit is counted) at `PILOT_REPORT_WEEKDAY` /
  `PILOT_REPORT_TIME` (`TEAM_TIMEZONE`, empty — off), at most once a week (`pilot_reports`), a missed one is sent on
  start within a day.

- Idea backlog (T2.4, `internal/ideas`, migration 007). `/idea <text>` makes an idea card instead of a brief: the
  marketer answers with the final `submit_idea` tool (title, what the reel is about in 1–3 sentences, format — talk,
  workout, carousel or other with a note — and the hook); the card is checked (lengths, known format, no control or
  invisible characters; an invalid card goes back to the model once) and kept by fields per version (`idea_cards`), the
  draft text is made from the fields. «Одобрить» under a card is «В бэклог»: the approved card becomes an idea with a
  short number (`ideas`, one per task: a repeated press or a redelivery makes no duplicate), the marketer answers «Идея №N
  в бэклоге…» instead of «Одобрено.»; a revision by a reply makes a new card version, a rejection saves nothing
  («Отклонено: в бэклог идея не попала.»). New commands of the marketer, answered at once without a task or the model:
  `/ideas` (new ideas, the newest first, 15 lines and «…и ещё N»), `/idea_done N` and `/idea_drop N` (honest answers to
  an unknown number or an idea closed before). Statuses `new` → `planned` → `done` / `dropped`; for `/plan` (T2.2) the
  service has `ListForPlan` and `MarkPlanned`. An approved idea whose save failed is restored by the next list. Ideas in
  plain messages without `/idea` are not recognized on purpose. A card keeps its draft text (`idea_cards.content`): an
  idea is made only of the card that is the approved draft in `task_versions`, and a stored version's card is not
  replaced by a stale run. A forwarded `/idea` is marked as someone else's words in the prompt and flagged in `ideas.forwarded` (the mark is not
  kept in `source_text`); forwarded `/idea_done`
  and `/idea_drop` are not run. A revision of an idea made before the backlog fails at once with a clear text; a card
  the model could not revise gets its own text. Dates for the trainer are in `TEAM_TIMEZONE` (default
  `Europe/Moscow`; the time zone database is built in).
- Workout editing (T3.10). A cut has a mode, kept in `plan.json` (`Edit.Mode`, `Plan.Mode`) and carried over the
  versions: `talk` (as before: pauses, fillers and failed takes are cut) or `workout` — the video stays whole, only what
  the editor cut explicitly goes (words, `Edit.CutTime` fragments by time, trims by time, accepted repeats). The model
  chooses the mode from the caption and the transcript (`submit_edit.mode`), the trainer can ask for it («тренировка»,
  «не режь паузы», «убери паузы»). A source with little speech is no longer cut blindly: when its talking-head cut would
  keep less than `MEDIA_WORKER_MIN_SPEECH_SHARE` (0.6) of the source, or it has no words at all, the auto stage stops
  with a workout plan (nothing cut) and the model decides even without a caption; a video without speech is no longer
  refused as «no speech». Without the model (no key, a cut answer, overload on the last attempt) such a video is
  rendered whole with a note instead of failing. Exercise titles («Выпады — 12 раз»): the model places them at the word
  where the trainer names the exercise or at the seconds of a caption-list, until the next title; exercises it cannot
  place are named in the description with a hint to add them by a revision with time («подпиши выпады с 0:15 до
  0:45»); revisions add, rename, move and remove titles. The app checks them (at most 20, 40 characters, no control,
  invisible characters or markup, not two at once, seen in the reel) and the worker draws them in a new `title` style of
  `assets/subtitles/style.yaml` (Fira Sans Extra Condensed ExtraBold, capitals, #F2B94B, on a translucent box at the
  edge opposite the speech subtitles, clear of them and of the Instagram zones). `mute` removes the sound of the source
  (a silent track stays) and the speech subtitles. The draft of a workout says so («Черновик тренировки готов…»),
  lists the titles with their time and the notes. A draft longer than `EDITOR_REELS_MAX_DURATION` (3 min) gets a
  note right after the header (Instagram hardly shows such reels to non-followers), not a refusal. Every draft
  description is kept within 920 UTF-16 units (the 1024 of a video caption less room for the link to the source):
  the header and the notes always come first, the changes, lists and the quoted caption follow while they fit, and
  the rest is replaced by «Описание сокращено: не всё поместилось в подпись к видео.»; a workout names at most 4
  titles and 5 untimed exercises and does not quote a caption the titles came from. `hide_words` leaves words out of
  the subtitles only (a phrase the speech recognition made up on music): the video and the sound stay, unlike
  `remove_words`, which in a workout would cut seconds of an exercise; hidden words add up over the versions
  (`Edit.Hide`). `plan.json` keeps `speech_share` — the share of the source the automatic talking-head cut keeps — and
  `min_speech_share`, the worker's threshold; the worker decides by them, the model sees both; a talking-head cut that cannot be
  built (every word a filler, more than 500 fragments) also gives the workout plan instead of «no speech», and the
  app refuses an edit of more than 500 fragments before it reaches the worker.

- The editor (Монтажёр) takes revisions by text, applies the caption and decides disputed repeats (T3.4c). A reply to
  the video draft (and «Правки» → reply) is a new version of the task: the editor's agent (`agents/editor`, Sonnet by
  `LLM_DEFAULT_MODEL`, the editing rules in its cached role prompt, calls counted in `llm_usage`) decides on the transcript and the
  plan of the previous version and answers with the `submit_edit` tool — a structured edit (cut or bring back words,
  trim the start or the end — by seconds of the source for a caption, of the reel for a revision, a word counts by its
  middle — keep the second take of a disputed repeat, correct a subtitle
  word) or a refusal (`cannot` / `unclear`). `internal/editplan` checks the edit against the transcript and the plan
  (indices, candidate IDs, text limits, at least 1 s left, the revision changes something), sends a failed check back
  to the model once, and only a checked `roughcut.Edit` goes to the media worker: word indices, candidate IDs and
  corrected subtitle words (no control or invisible characters, no markup), plus the model's short notes that go only
  into `decisions.log`; the trainer's text never does, and no free text reaches ffmpeg or the subtitles. Revisions add up (`plan.json` keeps the edit, `Plan.Edit`); the new version is rendered from the
  stored transcript of the source — **Whisper is not called again** — under its own keys (`…-v2-reel.mp4`), the
  earlier versions stay. The first version: with the trainer's caption or disputed repeats the worker stops after
  the plan (`planned`), the model decides, and the job is replaced by its render (`editjob.Board.Replace`, same job
  ID); without them the reel is rendered at once without a model call. The decisions and their reasons are in
  `decisions.log`. The draft of a new version says what changed («Новая версия рилс: было 0:45, стало 0:42.»,
  «Обрезал начало: убрал первые 3 с.»); a revision the editor cannot do or did not understand gets «Такое я пока не
  умею…» / «Не понял, что именно поменять…» above the previous draft; a caption that cannot be applied leaves the
  automatic cut with a note. Claude API failures: a refused key, an answer cut by `max_tokens` or too many steps are
  technical failures («Техничка»), the network and overload are retried by the task; a temporary failure of a
  decided render queues the same render again (`editjob.Board.Retry`: no new transcription, no new model call). The
  run ends right after an accepted `submit_edit` (a final tool of `agents.Runtime`), and the first user message is a
  cache breakpoint for every agent.

- The editor (Монтажёр) in Telegram, a deterministic pipeline (T3.4b): a video in «Исходники» (stored by T3.4a)
  becomes the editor's task (`reels-edit`, the S3 key in `media`, the caption kept as the input and shown in the
  draft's description), the trainer gets «Исходник получил, начинаю монтаж. Пришлю черновик в «Продакшн»…». The video
  is edited by a new service **`media-worker`** (`cmd/media-worker`, `internal/mediaworker`, the media image, its own
  container: no ports, nonroot, read-only root, `cpus: 2`, `mem_limit: 3g`, healthcheck by a heartbeat file, only
  S3, Redis and `OPENAI_API_KEY` — no bot tokens, Claude API key or PostgreSQL): download → probe (a picture, sound,
  `MEDIA_WORKER_MAX_SOURCE_DURATION`, default 10 min, refused before the transcription) → Whisper → rough cut plan with
  the default settings (disputed repeats are kept and counted) → the cut, the brand subtitles and the Instagram
  export in **one** ffmpeg pass (1080×1920, H.264 High, yuv420p, CRF 20 capped at 10 Mbit/s, AAC 128k 48 kHz,
  the source frame rate up to 60, `+faststart`; a source that is not 9:16 is padded, `MEDIA_WORKER_FIT=crop` crops)
  → the reel, `plan.json`, `decisions.log` and the source transcript in S3 under `MEDIA_RESULT_PREFIX`, next to the
  source by its message. The app and the worker meet on a Redis board (`internal/editjob`: a stream of jobs with a
  consumer group, job states, results, locks with a heartbeat, a «done» channel). The app's side (`internal/editor`)
  is an executor of the editor's tasks that never blocks a task worker: a run without a result is deferred
  (`tasks.DeferredError`, the attempt is not counted) and the worker's announcement wakes the task
  (`tasks.Service.Wake`); the job is keyed by the task and the version, so retries, restarts of the app or the worker
  and redelivered stream entries never edit twice. The draft is a **video** from the editor's bot in «Продакшн» (or
  the source topic when `TELEGRAM_TOPIC_PRODUCTION_ID` is not set) with a link to the source, the description
  (durations before/after, how much was cut, disputed repeats, the trainer's caption) and «Одобрить / Правки /
  Отклонить»; the file goes through `TELEGRAM_OUTBOX_DIR` and is removed after sending. «Правки» and a reply to
  a video draft answer «Правки текстом к видео появятся на следующем шаге…» (`tasks.ErrVideoRevision`), approval and
  rejection work as for text drafts. Failures (no key, a source that is too long, without sound or speech, missing
  brand fonts, a timeout `EDITOR_JOB_TIMEOUT`, default 45 min) reach the trainer as a short text, details go to the
  worker's log. New settings `MEDIA_RESULT_PREFIX`, `EDITOR_STREAM`, `EDITOR_JOB_TIMEOUT`, `EDITOR_POLL_INTERVAL`,
  `EDITOR_QUEUE_TIMEOUT`, `EDITOR_WORKER_WAIT`, `MEDIA_WORKER_*`; migration `006_video_drafts` (`task_versions.media_key`).
- Editor limits and reports (review of T3.4b): the job timeout counts from the moment a worker took the job
  (`<stream>:started:<id>`), waiting in the queue has its own `EDITOR_QUEUE_TIMEOUT` (3 h); a job not taken while no
  worker sends heartbeats fails after `EDITOR_WORKER_WAIT` (3 min). Setup failures (no key, the key refused, no brand
  fonts, no worker) reach the trainer as one plain text, and the editor's bot posts the code and the task to
  «Техничка» (`teamchat.TechReporter`, `TELEGRAM_TOPIC_TECH_ID`). HDR sources (HLG/PQ) are tone mapped to SDR bt709
  (`zscale`, `tonemap`), the export is always tagged bt709 limited range and stereo; ffmpeg runs with a minimal
  environment, without the worker's keys. A video caption is cut to 1024 characters so the buttons stay on the video;
  leftover reels are removed from the outbox on start; `Wake` wakes only the editor's tasks.
- The media worker still has full rights in Redis and S3 (the shared Redis password, the RustFS admin keys): a Redis
  ACL user and a RustFS user limited to its keys and prefixes are a separate task before the VPS deploy.
- `tasks`: `Executor` returns `tasks.Output` (text and an optional video key); `RoutingExecutor` routes agents to
  their executors; `DeferredError` and `Service.Wake` for work that goes on elsewhere; `TemporaryError` carries the
  trainer's text for a retried failure. Video drafts are sent with a longer lease (20 min) and timeout (15 min), and
  interrupted by stop instead of holding it.
- `ffmpeg.BurnSubtitlesData.Cut` renders a rough cut and the subtitles in one pass (the graph from a file in the
  working directory); `roughcut.Result.Render` and `Segments` give the cut of a plan-only run. `gateway.OutgoingMedia`
  sends a video's width and height and marks it streamable.
- Integration tests: `TestIntegrationPipeline` (real ffmpeg, libass and brand fonts on synthetic clips, a fake speech
  recognition; checks the export), run by `make test-integration` (needs ffmpeg on the host) and inside the media
  image in CI; `TestIntegrationEditorWorkerExchange` / `…Failure` (app ↔ worker through a real Redis and S3 with a fake
  pipeline), `TestIntegrationBoard*`, `TestIntegrationDeferredVideoDraft`.

- Video intake in the team chat (T3.4a, `internal/media`, `teamchat`): a video for the editor (topic «Исходники» or
  a mention of the editor's bot; a video or a video file) is downloaded by the gateway and copied to S3 right away,
  in the event's slot, under `<MEDIA_KEY_PREFIX>/<chat id>/<message id>/<file_unique_id>.<ext>` (the extension and
  the content type come from a known video MIME type, the file name only gives a known extension; unsafe file ids
  are refused), with `MEDIA_UPLOAD_TIMEOUT`; the editor's bot answers «Исходник получил и сохранил…», no task is
  created yet (editing is T3.4b, it reads the file back with `media.Store.Download`). A redelivered message writes
  the same key and is answered once (the gateway's mark in Redis). Voice messages and video notes are not processed
  on purpose (the trainer dictates text) and get «Голосовые не обрабатываю — продиктуйте текст…»; photos, audio
  files and videos for other roles get an honest answer; none of them is downloaded (`WantsMedia`). A video above
  `TELEGRAM_MAX_FILE_SIZE`, a failed download or a failed copy get an answer, details go to the log without paths.
  `s3.Client.PutObject` / `GetObject`, `gateway.Media.WithLocalFile` (a local file for handler tests). New settings
  `MEDIA_KEY_PREFIX`, `MEDIA_UPLOAD_TIMEOUT`.
- `media.Store.Download(ctx, key, dir, maxSize)` for the editor (T3.4b): the local copy reports its `Size`, an
  object above `maxSize` is `ErrFileTooLarge` and leaves no file on disk, a `maxSize` that is not positive is
  `ErrInvalidLimit`.
- Media slots in the gateway: events whose file is downloaded (videos for the editor) take one of
  `TELEGRAM_MEDIA_WORKERS` (default 2) slots instead of `TELEGRAM_WORKERS`, so videos do not delay text messages.
- S3 integration tests against RustFS (`internal/s3test`, `make s3-test-up` / `s3-test-down`): `make test-integration`
  starts a throwaway RustFS container (the compose image, published on 127.0.0.1 only, outside the compose
  project) and removes it at the end; each test gets its own bucket. `TestIntegrationClientObjects` (put/get,
  overwrite, `NoSuchKey` → `s3.ErrObjectNotFound`, a multipart upload) and `TestIntegrationStoreSaveAndDownload`.
- A video in «Исходники» addressed to another bot (a mention or a reply) gets «Уберите упоминание другого бота —
  видео монтирует Монтажёр.»; a video for another role with a caption also gets the caption note, as photos do. After
  copying a video the handler tries to answer even when the app is stopping (a context without the cancellation,
  10 s timeout); the gateway may have stopped waiting for it by then, so the answer is not guaranteed.

- Integration tests of the Telegram gateway's Redis store (T1.7, `internal/gateway/store_integration_test.go`, run
  by `make test-integration` on `internal/redistest`): the offset is saved, read, overwritten and kept per bot and
  does not expire; a corrupted offset is a parse error; a repeated `MarkSeen` returns false, after `Unmark` the event
  is new again; the mark has a TTL of at most 48 hours. The store takes a key prefix (empty in production, so the
  keys `telegram:offset:<bot id>` and `telegram:seen:<key>` are unchanged; `redistest.Prefix` in tests).
- `docs/MVP-BACKLOG.md` and `docs/PLAN.md`: follow-up (technical debt) tasks with priority and deadline — T1.6 `KB_ROOT`
  from `.env` for `make kb-*`, T1.7 integration tests of the Telegram gateway's Redis store on `internal/redistest`,
  T3.6 replacing `whisper-1` before OpenAI shuts it down on 2027-02-26 (main candidate ElevenLabs Scribe, compared on
  Russian speech first). Wording refined after review: what exactly ignores `KB_ROOT` in the image, which files
  T1.6 updates, how T1.7 tests are named, where T3.6 records its choice.
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

- The editor's cut sounds whole and dense (T3.19b). The cut points are placed by the level of the source sound (RMS
  in 10 ms windows, `ffmpeg.Runner.AudioLevels`, about a second for a 4-minute source; `roughcut` levels.go): the
  speech is found up to 0.3 s past Whisper's word edges (an eaten ending, a late start, the closure of a stop), a pause
  heard longer than `ROUGHCUT_MAX_PAUSE` (now 300ms, was 400ms) is compressed to a breath of 0.12 s (0.12–0.19 s on the
  frame grid) cut at its quietest frame, breaths and room noise between the phrases are cut, a removed word is cut at
  the quietest points around it; without a usable level the margins around the words (now 0.12/0.18 s, were
  0.08/0.12) are used. The 10 ms fades at every joint stay (no clicks, the length unchanged). `decisions.log` says how
  the cut points were placed and how many pauses longer than 0.3 s are left in the reel (`plan.json`: `sound_cuts`,
  `long_pauses`).
- The loudness of a draft is −14 ± 0.5 LUFS (T3.19b): the limiter took the loudness 1–2 LU under the target, so before
  the render the sound of the cut alone (no picture, seconds) runs through the same chain and is measured, and the
  gain is corrected by the miss (up to 3 passes, ±12 dB at most); the render measures its processed sound, and
  `decisions.log` has that measurement («итог … LUFS, пики … dBTP (замер готового звука)»), not the target.
- An edit renders faster (T3.19b): a cut decodes only what it keeps. Neighbouring segments (gaps under 3 s) are a
  group, each group is its own input of the source seeked to 0.5 s before it and read to 0.5 s after it, at most 8
  inputs (decoders with two threads; more groups are merged where the gaps are the shortest); one group near the
  start is the whole source as before. Frame accuracy, the sync, HDR and the sound are unchanged
  (`TestRunnerRenderSeeksGroups`, the sync and zoom tests). A one-minute draft from a 4-minute 4K source: 96.5 s → see
  `subtitles.TestRenderSpeedLongSource` (CI, 2 CPUs).

- The default subtitle style follows the reference reels (T3.17): one word at a time (a number with its unit together,
  every quantity of its own — «3 подхода» | «10 повторений» —, «10 000 шагов» as one, a word shorter than 0.15 s with
  the next one, no blinking between words, a dash said apart not shown), in small letters without the punctuation of
  its sentence and the quotes, small black Manrope Medium 48 px on an opaque white plate with 9 px padding (ASS
  BorderStyle 3, so the plate always fits the word) at the left Instagram margin right under the hook (x 140, y 600),
  no accents and no vignette (`layout.mode: word`, section `word_box`); the hook and the notes get a 3 px shadow of
  75 % black (`hook.shadow`). The phrase style of before is kept as
  `assets/subtitles/style-phrase.yaml` (`/app/subtitles-style-phrase.yaml` in the media image, `SUBTITLES_STYLE_FILE`
  switches to it). A custom style file without `layout.mode` now gets the word layout: add `layout.mode: phrase` and
  `vignette.enabled: true` to keep the phrases (the handle places itself by the layout). Hidden and corrected subtitle words work in both layouts. The style
  validation keeps the plate, the hook with the notes, the exercise titles and the handle clear of each other and of
  the Instagram zones.

- `EDITOR_REELS_TARGET_DURATION` defaults to `0`: the trainer's talking reels are 2–3 minutes, so a reel is not
  shortened unless the trainer asks for a length in the caption or a revision («сделай 40 секунд», `target_seconds`);
  without a caption the model is called only for the hook title, and no «longer than recommended» note is added. Set
  `60s` to bring the editorial cut of T3.16a back.
- Faster render of the editor's drafts (T3.11): about 3 times faster on the same machine. The draft is at most 30 fps
  (`roughcut.MaxFrameRate`: 60 → 30, 59.94 → 29.97, 50 → 25; 25 and 30 fps are kept), and the plan, the frame cuts and
  the subtitles use that rate; at 60 → 30 every reel frame is the source frame of the sound's time (fps `round=up`, the
  video moved 1/8 frame earlier for container rounding; `round=down` took the next frame, 1/60 s early). The frame is
  scaled to 1080×1920 right after the cut and an HDR (HLG/PQ) picture is tone mapped after the scaling, not on the 4K
  frame; the export uses x264 `veryfast` instead of `medium` (CRF 20, VBV 10 Mbit/s as before). CI (job media-image,
  ffmpeg 8.1, 2 CPUs, a 4K 60 fps 10-bit HEVC HLG source at 50 Mbit/s with grain): a draft with a rough cut takes 3.5 s of
  render per second of the reel, a whole source (a workout) 2.95 s per second, so 10 minutes of 4K60 take about
  30 minutes. Before T3.11 the same benchmark in CI took 9.8 s per second of the reel, on an easier 13 Mbit/s source
  (2.8 times slower; locally on ffmpeg 6.1 7.5 → 2.7 s at 50 Mbit/s). The defaults are raised:
  `MEDIA_WORKER_FFMPEG_TIMEOUT` 30m → 45m, `EDITOR_JOB_TIMEOUT` 45m → 70m. `TestRenderSpeed` measures it in CI and
  `TestBurnSubtitlesWithFFmpegToneMap` compares the colours of the new order with the old one for HLG and PQ.
- The editor's prompt lists the transcript by phrases of at most 8 words (word index ranges, the start in the source,
  the length, the start in the reel or why they are cut) instead of a line per word: over 3 times shorter, the model
  still cuts by word indices. The
  editor's refusal texts name «сократить до нужной длины» among what it can do.
- Dead ends point to `/help`: an unknown command answers «Такой команды нет. Команды — /help.» instead of listing
  every command; the editor's answer to a text, the designer's stub and the answers to voice messages, video notes,
  photos and audio files end with a pointer to `/help`. `/help`, `/start`, `/feedback` and `/onboard` are team commands
  of the marketer now.
- Executor routes are a value group: a module provides `tasks.Route{Agent, Kind, Executor}` with
  `tasks.RoutesGroup` instead of decorating `tasks.Executor` (the editor moved to it); a route of an agent and a kind
  wins over a route of the agent. `tasks.Draft` and `tasks.CloseDraftData` carry the task kind.
- Closing tags of prompt data blocks are matched by one helper (`pkg/helpers.ClosingTag`) in tasks, teamchat, editplan
  and ideas, now also with spaces inside the tag (`< /trainer_revision >`).
- `agents.Runtime` marks the end of the first user message with `cache_control` for every agent: runs with several
  turns read the task from the cache, single-turn runs pay the cache write.

- A revision of a video draft is no longer refused (`tasks.ErrVideoRevision` and its text are removed): «Правки» under
  a video draft asks to reply with the revision («Ответьте на видео текстом правки, например: …»), the caption of a
  source is taken as a wish for the edit («Подпись учту как пожелание к монтажу.»), `tasks.ExecuteData.Revision` carries
  the revision text to executors that do not use the prompt. The worker skips a delivery of a job stage that was
  replaced; plan candidates have `later_words`.

- The answer to a voice message or a video note: «Голосовые и кружки не обрабатываю — продиктуйте текст…»; to a video
  above `TELEGRAM_MAX_FILE_SIZE`: «Файл больше лимита 2 ГБ: …» (the limit in ГБ/МБ, with a comma).
- A text message to the editor gets a hint to send a video to «Исходники» instead of the stub (the designer's stub
  stays).

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

- Editor (T3.14): the media worker no longer edits the video of a task the app has given up. A job the app forgot
  (no worker running, a queue or running timeout, a failure) stays in the Redis stream; the worker now skips and acks an
  entry whose job is not on the board — no download, no Whisper call, no render, no result. A result and the start of a
  job are stored only while that very job (stage and creation time) is on the board, checked atomically with the write
  (`Board.Finish`, `Board.MarkStarted`, `editjob.ErrStale`): a job forgotten while it ran does not leave a result or a
  start for the retry submitted under the same ID, and the old entry of a resubmitted job is skipped while the new one
  runs once. A job forgotten or replaced while it runs (a timeout or the last attempt in the app, a new revision) is
  cancelled at the next lock heartbeat (ffmpeg and Whisper are killed with the context) instead of running to its
  timeout, and acked without a result. `EDITOR_QUEUE_TIMEOUT` + `EDITOR_JOB_TIMEOUT` must stay under 3.5 days, so a current job never outlives
  its record on the board.
- `decisions.log` of a first version without a caption no longer says «Решения монтажёра: 0» when the model decided:
  the model's reason is always logged, and the disputed repeats it left undecided are named.
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
