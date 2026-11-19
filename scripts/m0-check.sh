#!/usr/bin/env bash
# M0: сквозная проверка окружения и всех «кирпичей» с реальными ключами (docs/SETUP.md, раздел «Проверка M0»).
#
#   make m0-check                       # шаги без ключей пропускаются (SKIP)
#   make m0-check M0_VIDEO=./clip.mp4   # плюс транскрипция этого видео
#   M0_BOT_TOKEN=... make m0-check      # плюс getMe тестового бота через локальный Bot API
#
# Шаги: 1 окружение, 2 стек, 3 LLM, 4 база знаний, 5 транскрипция, 6 Telegram Bot API; сводка — в конце
# и в m0-results/report.md. Код выхода 1, если хоть один шаг FAIL; 130 — прерван (Ctrl-C).
#
# Скрипт не печатает значения секретов и не трогает пользовательские данные: создаёт только свои временные
# agents/m0check-<pid> и KB_ROOT/_m0check.XXXXXX с документом m0-check-<pid>-<время>.md и удаляет только то,
# что создал сам (в trap, в том числе при прерывании). Если такой путь или документ уже есть — FAIL.
# `down -v` не делает. Повторный запуск безопасен.
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."
ROOT="$PWD"

RESULTS_DIR="$ROOT/m0-results"
REPORT="$RESULTS_DIR/report.md"
AGENT_NAME="m0check-$$"
AGENT_DIR="$ROOT/agents/$AGENT_NAME"
# Уникальная фраза тестового документа: по ней ищем его в базе знаний.
KB_PHRASE="Кодовая фраза проверки M0: лазурный кенгуру делает выпады на рассвете"
LLM_PROMPT="Ответь одним словом: работает"
MAKE=(make --no-print-directory -s)
# Значения этих переменных маскируются в логах (если не короче 12 символов: короткое — не ключ, а слово).
SECRET_VARS=(ANTHROPIC_API_KEY OPENAI_API_KEY TELEGRAM_API_HASH POSTGRES_PASSWORD REDIS_PASSWORD S3_ACCESS_KEY S3_SECRET_KEY)
# Переменные transcribe для образа media: передаются через -e из разобранного .env (--env-file не снимает кавычки).
TRANSCRIBE_VARS=(OPENAI_API_KEY OPENAI_BASE_URL WHISPER_MODEL WHISPER_LANGUAGE WHISPER_REQUEST_TIMEOUT
	WHISPER_MAX_RETRIES WHISPER_RETRY_BASE_DELAY TRANSCRIPTION_SILENCE_NOISE_DB
	TRANSCRIPTION_SILENCE_MIN_DURATION FFMPEG_TIMEOUT LOG_LEVEL)

if [[ -t 1 ]]; then
	C_PASS=$'\e[32m' C_FAIL=$'\e[31m' C_SKIP=$'\e[33m' C_WARN=$'\e[33m' C_BOLD=$'\e[1m' C_OFF=$'\e[0m'
else
	C_PASS="" C_FAIL="" C_SKIP="" C_WARN="" C_BOLD="" C_OFF=""
fi

# Шаги по порядку; bash 3.2 (macOS) без ассоциативных массивов, поэтому всё — по индексу шага.
STEP_IDS=(env stack llm kb transcribe telegram)
STEP_TITLE=(
	"1. Окружение"
	"2. Стек (make up, /readyz, миграции)"
	"3. LLM (agent-run)"
	"4. База знаний (ingest/search/delete)"
	"5. Транскрипция"
	"6. Telegram Bot API"
)
STEP_STATUS=() STEP_NOTE=() STEP_SECONDS=()
CURRENT=0
TITLE_WIDTH=40
WARNINGS=()
ENV_KEYS=() ENV_VALUES=()

ENV_OK=false
STACK_OK=false
HAS_GIT=false
# Флаги «создано мной»: ставятся только после успешного создания, cleanup удаляет только их.
AGENT_CREATED=false
KB_ROOT_CREATED=false
KB_TMP_DIR=""
KB_SOURCE=""
KB_ROOT_DIR=""
TELEGRAM_STARTED=false
LLM_SINCE=""
SUMMARY_DONE=false

log() { printf '%s\n' "$*"; }
info() { printf '    %s\n' "$*"; }
warn() {
	WARNINGS+=("$*")
	printf '    %sWARN%s %s\n' "$C_WARN" "$C_OFF" "$*"
}
header() { printf '\n%s==> %s%s\n' "$C_BOLD" "${STEP_TITLE[CURRENT]}" "$C_OFF"; }

# result <step> PASS|FAIL|SKIP <note>; шаг передаётся для читаемости, записывается текущий (CURRENT).
result() {
	STEP_STATUS[CURRENT]=$2
	STEP_NOTE[CURRENT]=$3
	local color=$C_PASS
	[[ $2 == FAIL ]] && color=$C_FAIL
	[[ $2 == SKIP ]] && color=$C_SKIP
	printf '    %s%s%s %s\n' "$color" "$2" "$C_OFF" "$3"
}

# load_env: разбирает .env в ENV_KEYS/ENV_VALUES. Файл не исполняется: KEY=VALUE построчно, необязательный
# `export`, строки-комментарии, кавычки '…' и "…" снимаются, у значения без кавычек отрезается ` # комментарий`.
# Значения с `)`, `&`, `$` и пробелами читаются как есть.
load_env() {
	ENV_KEYS=()
	ENV_VALUES=()
	[[ -f "$ROOT/.env" ]] || return 0
	local line key value
	while IFS= read -r line || [[ -n "$line" ]]; do
		line=${line%$'\r'}
		line=${line#"${line%%[![:space:]]*}"}
		[[ -z "$line" || $line == \#* ]] && continue
		if [[ $line == export[[:space:]]* ]]; then
			line=${line#export}
			line=${line#"${line%%[![:space:]]*}"}
		fi
		[[ $line == *=* ]] || continue
		key=${line%%=*}
		key=${key%"${key##*[![:space:]]}"}
		value=${line#*=}
		value=${value#"${value%%[![:space:]]*}"}
		case $value in
		\"*)
			value=${value#\"}
			value=${value%%\"*}
			;;
		\'*)
			value=${value#\'}
			value=${value%%\'*}
			;;
		*)
			value=${value%%[[:space:]]#*}
			value=${value%"${value##*[![:space:]]}"}
			;;
		esac
		ENV_KEYS+=("$key")
		ENV_VALUES+=("$value")
	done <"$ROOT/.env"
}

# env_lookup <NAME>: значение из .env (при повторе — последнее); код 1, если переменной там нет.
env_lookup() {
	((${#ENV_KEYS[@]} > 0)) || return 1
	local i found=""
	for i in "${!ENV_KEYS[@]}"; do
		if [[ ${ENV_KEYS[i]} == "$1" ]]; then
			found=$i
		fi
	done
	[[ -n "$found" ]] || return 1
	printf '%s' "${ENV_VALUES[found]}"
}

# env_get <NAME>: значение из .env или пусто.
env_get() { env_lookup "$1" || true; }

# describe_secret <value>: «задан (…abcd)» без самого значения.
describe_secret() {
	local value=$1
	if [[ -z "$value" ]]; then
		printf 'не задан'
	elif ((${#value} >= 12)); then
		printf 'задан (…%s)' "${value: -4}"
	else
		printf 'задан'
	fi
}

compose() { docker compose "$@"; }

docker_server_version() {
	local version
	version=$(docker info --format '{{.ServerVersion}}' 2>/dev/null || true)
	printf '%s' "${version:-?}"
}

# pad <text> <width>: дополняет пробелами по числу символов (printf %-Ns считает байты, а не кириллицу).
pad() {
	local text=$1 width=$2 chars
	# Символы UTF-8 = байты без байтов продолжения (0x80–0xBF): не зависит от локали.
	chars=$(printf '%s' "$text" | LC_ALL=C tr -d '\200-\277' | wc -c)
	printf '%s' "$text"
	if ((chars < width)); then
		printf '%*s' "$((width - chars))" ''
	fi
}

# psql_query: SQL из stdin в базу compose; переменные раскрывает shell контейнера.
psql_query() {
	# shellcheck disable=SC2016
	compose exec -T postgres sh -c 'psql -X -q -U "$POSTGRES_USER" -d "$POSTGRES_DB" -tA -F "|" -v ON_ERROR_STOP=1'
}

# mask_logs: ключи и пароли в m0-results/*.log и *.out заменяются на ***. Логи — для разбора на месте,
# пересылать их всё равно не стоит.
mask_logs() {
	local secrets=() name value file line
	for name in "${SECRET_VARS[@]}"; do
		value=$(env_get "$name")
		((${#value} < 12)) || secrets+=("$value")
	done
	value=${M0_BOT_TOKEN-}
	[[ -z "$value" ]] || secrets+=("$value")

	for file in "$RESULTS_DIR"/*.log "$RESULTS_DIR"/*.out; do
		[[ -f "$file" ]] || continue
		# Ключи OpenAI/Anthropic (sk-…) и токены ботов (<id>:<секрет>) — по шаблону, остальное — по значению.
		sed -E -e 's/sk-[A-Za-z0-9_-]{8,}/sk-***/g' -e 's/[0-9]{6,}:[A-Za-z0-9_-]{30,}/***:***/g' "$file" >"$file.tmp"
		if ((${#secrets[@]} > 0)); then
			while IFS= read -r line || [[ -n "$line" ]]; do
				for value in "${secrets[@]}"; do
					line=${line//"$value"/***}
				done
				printf '%s\n' "$line"
			done <"$file.tmp" >"$file"
			rm -f "$file.tmp"
		else
			mv "$file.tmp" "$file"
		fi
	done
}

cleanup() {
	local status=$?
	# Повторный Ctrl-C не обрывает уборку.
	trap '' INT TERM
	if [[ $AGENT_CREATED == true ]]; then
		rm -rf "$AGENT_DIR"
	fi
	if [[ -n "$KB_SOURCE" ]]; then
		"${MAKE[@]}" kb-delete SOURCE="$KB_SOURCE" >>"$RESULTS_DIR/kb.log" 2>&1 || true
	fi
	if [[ -n "$KB_TMP_DIR" ]]; then
		rm -rf "$KB_TMP_DIR"
	fi
	if [[ $KB_ROOT_CREATED == true ]]; then
		rmdir "$KB_ROOT_DIR" 2>/dev/null || true
	fi
	if [[ $TELEGRAM_STARTED == true ]]; then
		compose --profile telegram stop telegram-bot-api >>"$RESULTS_DIR/telegram.log" 2>&1 || true
	fi
	if [[ -d "$RESULTS_DIR" ]]; then
		mask_logs
	fi
	if [[ $SUMMARY_DONE != true ]]; then
		printf '\n%sm0-check прерван (код %d): сводка не записана, временные файлы убраны%s\n' \
			"$C_FAIL" "$status" "$C_OFF" >&2
	fi
}
trap cleanup EXIT
# Ctrl-C и SIGTERM прерывают весь прогон, а не только текущую команду с переходом к следующему шагу.
trap 'exit 130' INT
trap 'exit 143' TERM

# --- 1. Окружение ---------------------------------------------------------------------------------

REQUIRED_VARS=(POSTGRES_USER POSTGRES_PASSWORD POSTGRES_DB REDIS_PASSWORD S3_ACCESS_KEY S3_SECRET_KEY)

# dev_default <NAME>: значение из .env.dist, которое в продакшне нужно сменить.
dev_default() {
	case $1 in
	POSTGRES_PASSWORD) printf 'postgres' ;;
	REDIS_PASSWORD) printf 'redis-dev-password' ;;
	S3_ACCESS_KEY | S3_SECRET_KEY) printf 'rustfsadmin' ;;
	esac
}

# join <items...>: элементы через «; ».
join() {
	local out="" item
	for item in "$@"; do
		out+="${out:+; }$item"
	done
	printf '%s' "$out"
}

step_env() {
	local problems=() tool

	for tool in docker make curl; do
		if command -v "$tool" >/dev/null 2>&1; then
			info "$tool: есть"
		else
			problems+=("нет $tool")
		fi
	done
	if command -v git >/dev/null 2>&1 && git rev-parse --git-dir >/dev/null 2>&1; then
		HAS_GIT=true
		info "git: есть"
	else
		warn "нет git или папка не git-клон: в отчёте будет Git: unknown"
	fi

	if command -v docker >/dev/null 2>&1; then
		if docker compose version >/dev/null 2>&1; then
			info "docker compose: $(docker compose version --short 2>/dev/null)"
		else
			problems+=("нет docker compose v2")
		fi
		if docker info >/dev/null 2>&1; then
			info "docker daemon: $(docker_server_version)"
		else
			problems+=("docker daemon недоступен (Docker Desktop запущен?)")
		fi
	fi

	if [[ ! -f "$ROOT/.env" ]]; then
		problems+=("нет .env: cp .env.dist .env")
		result env FAIL "$(join "${problems[@]}")"
		return 0
	fi
	info ".env: есть"

	local name value
	for name in "${REQUIRED_VARS[@]}"; do
		value=$(env_get "$name")
		if [[ -z "$value" ]]; then
			problems+=("$name не задан")
			continue
		fi
		if [[ "$value" == "$(dev_default "$name")" ]]; then
			warn "$name — dev-значение из .env.dist: для продакшна смени"
		fi
	done

	# Переменные, которые появились в .env.dist после того, как был создан .env.
	local missing=()
	while IFS= read -r name; do
		env_lookup "$name" >/dev/null || missing+=("$name")
	done < <(sed -nE 's/^([A-Z][A-Z0-9_]*)=.*/\1/p' "$ROOT/.env.dist")
	if ((${#missing[@]} > 0)); then
		warn "в .env нет переменных из .env.dist (берутся значения по умолчанию): ${missing[*]}"
	fi

	for name in ANTHROPIC_API_KEY OPENAI_API_KEY TELEGRAM_API_ID TELEGRAM_API_HASH; do
		info "$name: $(describe_secret "$(env_get "$name")")"
	done
	info "M0_BOT_TOKEN: $(describe_secret "${M0_BOT_TOKEN-}")"

	if ((${#problems[@]} > 0)); then
		result env FAIL "$(join "${problems[@]}")"
		return 0
	fi

	ENV_OK=true
	result env PASS "инструменты есть, обязательные переменные заданы"
}

# --- 2. Стек ---------------------------------------------------------------------------------------

step_stack() {
	if [[ $ENV_OK != true ]]; then
		result stack SKIP "окружение не готово (шаг 1)"
		return 0
	fi

	info "make up (сборка и запуск, лог: m0-results/up.log)…"
	if ! "${MAKE[@]}" up >"$RESULTS_DIR/up.log" 2>&1; then
		compose ps -a >>"$RESULTS_DIR/up.log" 2>&1 || true
		result stack FAIL "make up не дождался healthy: m0-results/up.log, docker compose logs"
		return 0
	fi

	local port body
	port=$(env_get APP_HOST_PORT)
	port=${port:-8080}
	if ! body=$(curl -sS --max-time 10 "http://127.0.0.1:${port}/readyz" 2>&1); then
		result stack FAIL "/readyz недоступен на 127.0.0.1:${port}: $body"
		return 0
	fi
	info "/readyz: $body"
	if [[ "$body" != *'"status":"ok"'* || "$body" == *'"fail"'* ]]; then
		result stack FAIL "/readyz не ok: $body"
		return 0
	fi

	local latest state
	latest=$(find migrations -name '*.up.sql' -exec basename {} \; | sed -E 's/^0*([0-9]+)_.*/\1/' | sort -n | tail -n 1)
	if ! state=$(psql_query <<<'SELECT version, dirty FROM schema_migrations' 2>&1); then
		result stack FAIL "не удалось прочитать schema_migrations: $state"
		return 0
	fi
	info "schema_migrations: ${state} (последняя в migrations/: ${latest})"
	if [[ "$state" != "${latest}|f" ]]; then
		result stack FAIL "версия миграций ${state}, ожидается ${latest}|f"
		return 0
	fi

	STACK_OK=true
	result stack PASS "стек healthy, /readyz ok, миграции ${latest} (не dirty)"
}

# --- 3. LLM ----------------------------------------------------------------------------------------

step_llm() {
	if [[ -z "$(env_get ANTHROPIC_API_KEY)" ]]; then
		result llm SKIP "ANTHROPIC_API_KEY не задан"
		return 0
	fi
	if [[ $STACK_OK != true ]]; then
		result llm SKIP "стек не поднят (шаг 2)"
		return 0
	fi
	if [[ -e "$AGENT_DIR" ]]; then
		result llm FAIL "agents/${AGENT_NAME} уже существует: не трогаю"
		return 0
	fi
	# mkdir без -p падает, если папка появилась между проверкой и созданием: чужое не займём.
	if ! mkdir "$AGENT_DIR" 2>/dev/null; then
		result llm FAIL "не удалось создать agents/${AGENT_NAME}"
		return 0
	fi
	AGENT_CREATED=true
	cp -R agents/_example/. "$AGENT_DIR/"
	sed "s/^name: _example\$/name: ${AGENT_NAME}/" agents/_example/agent.md >"$AGENT_DIR/agent.md"

	LLM_SINCE=$(psql_query <<<'SELECT now()' 2>/dev/null || true)

	info "make agent-run AGENT=${AGENT_NAME} PROMPT=\"${LLM_PROMPT}\" (лог: m0-results/llm.log)…"
	local out="$RESULTS_DIR/llm.out" code=0
	"${MAKE[@]}" agent-run AGENT="$AGENT_NAME" PROMPT="$LLM_PROMPT" >"$out" 2>"$RESULTS_DIR/llm.log" || code=$?

	# Образ ai-agents-app:local собран с временным агентом: пересобираем без него, чтобы образ совпадал с репозиторием.
	rm -rf "$AGENT_DIR"
	AGENT_CREATED=false
	compose build -q migrate >>"$RESULTS_DIR/llm.log" 2>&1 || warn "не удалось пересобрать образ без agents/${AGENT_NAME}: make up"

	# stdout agent-run: JSON-логи, ответ, затем блок «--- usage ---».
	local answer usage
	answer=$(sed -n '/^--- usage ---$/q;p' "$out" | grep -v -e '^{' -e '^--- partial answer' | sed '/^[[:space:]]*$/d' || true)
	usage=$(sed -n '/^--- usage ---$/,$p' "$out" | grep -v '^{' || true)

	if [[ $code -ne 0 ]]; then
		result llm FAIL "agent-run завершился с кодом $code: m0-results/llm.log"
		return 0
	fi
	info "ответ: $(printf '%s' "$answer" | head -n 3)"
	printf '%s\n' "$usage" | sed -n '2,$p' | sed 's/^/    /'
	if [[ -z "$answer" ]]; then
		result llm FAIL "пустой ответ агента: m0-results/llm.out"
		return 0
	fi
	if ! grep -q '^tokens: input [0-9]' <<<"$usage"; then
		result llm FAIL "нет строки расхода (tokens): m0-results/llm.out"
		return 0
	fi
	result llm PASS "ответ получен, $(grep '^cost:' <<<"$usage" || echo 'cost: ?')"
}

# --- 4. База знаний --------------------------------------------------------------------------------

# kb_prepare: KB_ROOT (если его нет — создаётся и потом удаляется, если остался пустым) и своя временная папка в нём.
kb_prepare() {
	local kb_root dir
	kb_root=$(env_get KB_ROOT)
	KB_ROOT_DIR=${kb_root:-./kb}
	KB_ROOT_DIR=${KB_ROOT_DIR%/}
	if [[ ! -d "$KB_ROOT_DIR" ]]; then
		mkdir "$KB_ROOT_DIR" 2>/dev/null || return 1
		KB_ROOT_CREATED=true
	fi
	# mktemp -d атомарно создаёт новую папку: чужую он не вернёт.
	dir=$(mktemp -d "$KB_ROOT_DIR/_m0check.XXXXXX") || return 1
	KB_TMP_DIR=$dir
}

step_kb() {
	if [[ -z "$(env_get OPENAI_API_KEY)" ]]; then
		result kb SKIP "OPENAI_API_KEY не задан"
		return 0
	fi
	if [[ $STACK_OK != true ]]; then
		result kb SKIP "стек не поднят (шаг 2)"
		return 0
	fi
	if ! command -v go >/dev/null 2>&1; then
		result kb SKIP "нет Go: make kb-* запускает go run на хосте (docs/SETUP.md, «Требования»)"
		return 0
	fi
	if ! kb_prepare; then
		result kb FAIL "не удалось создать временную папку в KB_ROOT (${KB_ROOT_DIR})"
		return 0
	fi

	local file expected existing log="$RESULTS_DIR/kb.log" out
	file="m0-check-$$-$(date +%s).md"
	# source — путь относительно KB_ROOT: kb ingest берёт файлы только оттуда.
	expected="${KB_TMP_DIR#"$KB_ROOT_DIR"/}/$file"
	: >"$log"

	if ! existing=$(psql_query <<<"SELECT count(*) FROM kb_documents WHERE source = '${expected}'" 2>&1); then
		result kb FAIL "не удалось проверить kb_documents: $existing"
		return 0
	fi
	if [[ "$existing" != 0 ]]; then
		result kb FAIL "документ ${expected} уже есть в базе знаний: не трогаю"
		return 0
	fi

	cat >"$KB_TMP_DIR/$file" <<EOF
# Проверка M0

Временный документ скрипта scripts/m0-check.sh: он удаляется сразу после проверки.

${KB_PHRASE}. Этот абзац нужен только для того, чтобы поиск по смыслу нашёл документ первым.
EOF
	# До ingest: если прогон прервут, trap удалит документ.
	KB_SOURCE=$expected

	info "make kb-ingest KB_PATH=${KB_TMP_DIR} KB_KIND=other (лог: m0-results/kb.log)…"
	if ! out=$("${MAKE[@]}" kb-ingest KB_PATH="$KB_TMP_DIR" KB_KIND=other 2>>"$log"); then
		printf '%s\n' "$out" >>"$log"
		result kb FAIL "kb-ingest не прошёл: m0-results/kb.log"
		return 0
	fi
	printf '%s\n' "$out" >>"$log"
	info "$out"

	# Строка ingest: «<статус> <source> (<N> chunks)».
	local printed
	printed=$(grep -F "$file (" <<<"$out" | grep -v '^failed' | sed -E 's/^[a-z]+ +(.*) \([0-9]+ chunks\)$/\1/' | head -n 1)
	if [[ "$printed" != "$expected" ]]; then
		# Не должно случаться (source = путь от KB_ROOT), но файл с уникальным именем загружен этим прогоном:
		# удаляем документ под тем source, что напечатал ingest.
		[[ -z "$printed" ]] || KB_SOURCE=$printed
		result kb FAIL "ingest напечатал source «${printed}», ожидался «${expected}» (KB_ROOT): m0-results/kb.log"
		return 0
	fi

	info "make kb-search Q=\"${KB_PHRASE}\" LIMIT=3…"
	if ! out=$("${MAKE[@]}" kb-search Q="$KB_PHRASE" LIMIT=3 2>>"$log"); then
		result kb FAIL "kb-search не прошёл: m0-results/kb.log"
		return 0
	fi
	printf '%s\n' "$out" >>"$log"
	local first
	first=$(grep -m 1 '^\[1\] ' <<<"$out" || true)
	info "первый результат: ${first:-нет}"
	if [[ "$first" != "[1] ${KB_SOURCE} "* ]]; then
		result kb FAIL "тестовый документ ${KB_SOURCE} не первый в поиске: m0-results/kb.log"
		return 0
	fi

	info "make kb-delete SOURCE=${KB_SOURCE}…"
	if ! out=$("${MAKE[@]}" kb-delete SOURCE="$KB_SOURCE" 2>>"$log"); then
		result kb FAIL "kb-delete не прошёл: m0-results/kb.log"
		return 0
	fi
	printf '%s\n' "$out" >>"$log"
	KB_SOURCE=""

	result kb PASS "документ загружен, найден первым и удалён"
}

# --- 5. Транскрипция -------------------------------------------------------------------------------

# transcribe_in_image <video> <log>: transcribe в образе media. Переменные — явно через -e из разобранного .env:
# значения уходят в окружение docker run, а не в его аргументы (не видны в ps).
transcribe_in_image() {
	local video=$1 log=$2 dir name
	dir=$(cd "$(dirname "$video")" && pwd)
	name=$(basename "$video")
	"${MAKE[@]}" media-image >"$log" 2>&1 || return 1
	(
		flags=()
		for var in "${TRANSCRIBE_VARS[@]}"; do
			if value=$(env_lookup "$var"); then
				export "$var=$value"
				flags+=(-e "$var")
			fi
		done
		docker run --rm "${flags[@]}" --user "$(id -u):$(id -g)" \
			-v "$dir:/in:ro" -v "$RESULTS_DIR:/work" ai-agents-media:local \
			-in "/in/$name" -out transcript.json
	) >>"$log" 2>&1
}

step_transcribe() {
	local video=${M0_VIDEO-}
	if [[ -z "$(env_get OPENAI_API_KEY)" ]]; then
		result transcribe SKIP "OPENAI_API_KEY не задан"
		return 0
	fi
	if [[ -z "$video" ]]; then
		result transcribe SKIP "нет видео: make m0-check M0_VIDEO=./clip.mp4 (ролик с речью, 10–60 с)"
		return 0
	fi
	if [[ ! -f "$video" ]]; then
		result transcribe FAIL "M0_VIDEO=${video}: файл не найден"
		return 0
	fi

	local json="$RESULTS_DIR/transcript.json" log="$RESULTS_DIR/transcribe.log" code=0
	rm -f "$json"
	if command -v go >/dev/null 2>&1 && command -v ffmpeg >/dev/null 2>&1 && command -v ffprobe >/dev/null 2>&1; then
		info "make transcribe (go run на хосте, лог: m0-results/transcribe.log)…"
		"${MAKE[@]}" transcribe ARGS="-in $(printf '%q' "$video") -out $(printf '%q' "$json")" >"$log" 2>&1 || code=$?
	elif [[ $ENV_OK == true ]]; then
		info "go/ffmpeg на хосте нет: transcribe в образе media (лог: m0-results/transcribe.log)…"
		transcribe_in_image "$video" "$log" || code=$?
	else
		result transcribe FAIL "нужны go+ffmpeg на хосте или Docker (шаг 1)"
		return 0
	fi

	if [[ $code -ne 0 || ! -s "$json" ]]; then
		result transcribe FAIL "transcribe завершился с кодом $code: m0-results/transcribe.log"
		return 0
	fi

	# JSON пишет json.MarshalIndent, поэтому формат строк стабилен.
	local words language
	words=$(grep -c '"word":' "$json" || true)
	language=$(sed -nE 's/^  "language": "([^"]*)",?$/\1/p' "$json" | head -n 1)
	info "язык: ${language:-?}, слов с таймкодами: ${words}, результат: m0-results/transcript.json"
	if [[ -z "$language" ]]; then
		result transcribe FAIL "пустой language: m0-results/transcript.json"
		return 0
	fi
	if ((words == 0)); then
		result transcribe FAIL "words пуст: m0-results/transcript.json"
		return 0
	fi
	result transcribe PASS "language=${language}, слов: ${words}"
}

# --- 6. Telegram Bot API ---------------------------------------------------------------------------

# bot_get_me <token>: getMe через локальный сервер изнутри контейнера. Токен идёт через stdin (не в аргументах
# и не в логах), ответ не сохраняется; при ошибке печатается её начало без токена.
bot_get_me() {
	local token=$1 response
	# shellcheck disable=SC2016 # $t и $url раскрывает shell контейнера
	response=$(printf '%s\n' "$token" | compose --profile telegram exec -T telegram-bot-api sh -c '
		read -r t
		url="http://127.0.0.1:8081/bot$t/getMe"
		if command -v curl >/dev/null 2>&1; then curl -sS --max-time 20 "$url"
		elif command -v wget >/dev/null 2>&1; then wget -qO- -T 20 "$url"
		else echo "no curl or wget in the container"; exit 1
		fi' 2>&1 || true)
	if [[ "$response" == *'"ok":true'* ]]; then
		return 0
	fi
	response=${response//"$token"/***}
	printf '%s' "$response" | head -c 200 | tr '\n' ' '
	return 1
}

step_telegram() {
	if [[ -z "$(env_get TELEGRAM_API_ID)" || -z "$(env_get TELEGRAM_API_HASH)" ]]; then
		result telegram SKIP "TELEGRAM_API_ID/TELEGRAM_API_HASH не заданы (необязательно)"
		return 0
	fi
	if [[ $ENV_OK != true ]]; then
		result telegram SKIP "окружение не готово (шаг 1)"
		return 0
	fi

	local running log="$RESULTS_DIR/telegram.log"
	running=$(compose --profile telegram ps -q --status running telegram-bot-api 2>/dev/null || true)

	info "docker compose --profile telegram up -d --wait telegram-bot-api…"
	# Bot API, поднятый только для проверки, trap остановит; запущенный пользователем — не трогает.
	[[ -n "$running" ]] || TELEGRAM_STARTED=true
	if ! compose --profile telegram up -d --wait --wait-timeout 120 telegram-bot-api >"$log" 2>&1; then
		compose --profile telegram logs --tail 20 telegram-bot-api >>"$log" 2>&1 || true
		result telegram FAIL "bot-api не стал healthy: m0-results/telegram.log"
		return 0
	fi

	local id health
	id=$(compose --profile telegram ps -q telegram-bot-api 2>/dev/null || true)
	health=$(docker inspect --format '{{.State.Health.Status}}' "$id" 2>/dev/null || echo unknown)
	info "healthcheck: $health"
	if [[ "$health" != healthy ]]; then
		result telegram FAIL "healthcheck bot-api: $health"
		return 0
	fi

	local token=${M0_BOT_TOKEN-} error
	if [[ -z "$token" ]]; then
		result telegram PASS "порт 8081 открыт, ключи не проверены (проверка: M0_BOT_TOKEN тестового бота)"
		return 0
	fi
	info "getMe тестового бота через локальный сервер…"
	if ! error=$(bot_get_me "$token"); then
		result telegram FAIL "getMe не ok: ${error:-нет ответа} (неверный токен или api_id/api_hash?)"
		return 0
	fi
	result telegram PASS "getMe ok через локальный сервер: api_id/api_hash и токен приняты"
}

# --- 7. Сводка -------------------------------------------------------------------------------------

llm_usage_report() {
	[[ $STACK_OK == true ]] || return 0
	local rows="" total
	if [[ -n "$LLM_SINCE" ]]; then
		local sql="SELECT agent, model, count(*), sum(input_tokens), sum(output_tokens),
coalesce(sum(cost_usd)::text, 'n/a') FROM llm_usage WHERE created_at >= TIMESTAMPTZ '${LLM_SINCE}'
GROUP BY 1, 2 ORDER BY 1, 2"
		rows=$(psql_query <<<"$sql" 2>/dev/null || true)
	fi
	total=$(psql_query <<<"SELECT count(*), coalesce(sum(cost_usd), 0) FROM llm_usage" 2>/dev/null || true)
	[[ -n "$total" ]] || return 0

	printf '\n## Расход LLM (llm_usage)\n\n'
	if [[ -n "$rows" ]]; then
		printf '| Агент | Модель | Вызовов | Input | Output | USD |\n|---|---|---|---|---|---|\n'
		while IFS='|' read -r agent model calls input output cost; do
			printf '| %s | %s | %s | %s | %s | %s |\n' "$agent" "$model" "$calls" "$input" "$output" "$cost"
		done <<<"$rows"
		printf '\n'
	else
		printf 'В этом прогоне вызовов Claude API не было.\n\n'
	fi
	printf 'Всего в llm_usage: %s вызовов, $%s.\n' "${total%%|*}" "${total#*|}"
}

# table_cell <text>: `|` ломает таблицу markdown — заменяем на `/` (одинаково в bash 3.2 и 5).
table_cell() { printf '%s' "$1" | tr '|' '/'; }

write_report() {
	local git_line="unknown"
	if [[ $HAS_GIT == true ]]; then
		git_line="$(git rev-parse --short HEAD 2>/dev/null || echo unknown), ветка $(git rev-parse --abbrev-ref HEAD 2>/dev/null || echo unknown)"
		[[ -z "$(git status --porcelain --untracked-files=no 2>/dev/null)" ]] || git_line+=" (есть незакоммиченные изменения)"
	fi

	{
		printf '# Отчёт M0-проверки\n\n'
		printf -- '- Дата: %s\n' "$(date -u '+%Y-%m-%d %H:%M:%S UTC')"
		printf -- '- Git: %s\n' "$git_line"
		printf -- '- Хост: %s\n' "$(uname -sm)"
		if command -v docker >/dev/null 2>&1; then
			printf -- '- Docker: %s, compose %s\n' "$(docker_server_version)" \
				"$(docker compose version --short 2>/dev/null || echo '?')"
		fi
		printf -- '- Видео для транскрипции: %s\n' "$(if [[ -n "${M0_VIDEO-}" ]]; then basename "$M0_VIDEO"; else echo нет; fi)"
		printf '\n## Шаги\n\n| Шаг | Результат | Время, с | Комментарий |\n|---|---|---|---|\n'
		local i
		for i in "${!STEP_IDS[@]}"; do
			printf '| %s | %s | %s | %s |\n' "${STEP_TITLE[i]}" "${STEP_STATUS[i]}" "${STEP_SECONDS[i]}" \
				"$(table_cell "${STEP_NOTE[i]}")"
		done
		if ((${#WARNINGS[@]} > 0)); then
			printf '\n## Предупреждения\n\n'
			printf -- '- %s\n' "${WARNINGS[@]}"
		fi
		llm_usage_report
		if [[ $ENV_OK == true ]]; then
			printf '\n## Образы\n\n```\n'
			compose --profile telegram config --images 2>/dev/null | sort -u
			printf -- '---\n'
			docker image inspect --format '{{index .RepoTags 0}} {{.Id}} {{.Created}}' ai-agents-app:local 2>/dev/null || true
			docker image inspect --format '{{index .RepoTags 0}} {{.Id}} {{.Created}}' ai-agents-media:local 2>/dev/null || true
			printf '```\n'
		fi
	} >"$REPORT"
}

print_summary() {
	local i color failed=0
	printf '\n%s==> Сводка M0%s\n\n' "$C_BOLD" "$C_OFF"
	printf '    %s %-6s %6s  %s\n' "$(pad "Шаг" "$TITLE_WIDTH")" "Итог" "Время" "Комментарий"
	for i in "${!STEP_IDS[@]}"; do
		case ${STEP_STATUS[i]} in
		PASS) color=$C_PASS ;;
		FAIL)
			color=$C_FAIL
			failed=$((failed + 1))
			;;
		*) color=$C_SKIP ;;
		esac
		printf '    %s %s%-6s%s %5ss  %s\n' "$(pad "${STEP_TITLE[i]}" "$TITLE_WIDTH")" "$color" "${STEP_STATUS[i]}" "$C_OFF" \
			"${STEP_SECONDS[i]}" "${STEP_NOTE[i]}"
	done
	printf '\n    Отчёт: m0-results/report.md'
	((${#WARNINGS[@]} == 0)) || printf ', предупреждений: %d' "${#WARNINGS[@]}"
	printf '\n    Логи m0-results/*.log — для разбора на месте (ключи замаскированы), не пересылай их\n'
	return "$failed"
}

main() {
	mkdir -p "$RESULTS_DIR"
	load_env
	log "${C_BOLD}M0-проверка${C_OFF}, результаты: m0-results/"

	local started
	for CURRENT in "${!STEP_IDS[@]}"; do
		header
		started=$SECONDS
		"step_${STEP_IDS[CURRENT]}"
		STEP_SECONDS[CURRENT]=$((SECONDS - started))
	done

	write_report
	SUMMARY_DONE=true
	if print_summary; then
		printf '\n%sM0: FAIL нет%s\n' "$C_PASS" "$C_OFF"
		return 0
	fi
	printf '\n%sM0: есть FAIL — см. комментарии выше и m0-results/%s\n' "$C_FAIL" "$C_OFF"
	return 1
}

main "$@"
