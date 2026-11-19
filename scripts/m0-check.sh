#!/usr/bin/env bash
# M0: сквозная проверка окружения и всех «кирпичей» с реальными ключами (docs/SETUP.md, раздел «Проверка M0»).
#
#   make m0-check                       # шаги без ключей пропускаются (SKIP)
#   make m0-check M0_VIDEO=./clip.mp4   # плюс транскрипция этого видео
#
# Шаги: 1 окружение, 2 стек, 3 LLM, 4 база знаний, 5 транскрипция, 6 Telegram Bot API; сводка — в конце
# и в m0-results/report.md. Код выхода 1, если хоть один шаг FAIL.
#
# Скрипт не печатает значения секретов и не трогает пользовательские данные: создаёт только свои временные
# agents/m0check и KB_ROOT/_m0check (удаляются в trap) и документ базы знаний из них (удаляется kb-delete).
# `down -v` не делает. Повторный запуск безопасен.
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."
ROOT="$PWD"

RESULTS_DIR="$ROOT/m0-results"
REPORT="$RESULTS_DIR/report.md"
AGENT_NAME="m0check"
AGENT_DIR="$ROOT/agents/$AGENT_NAME"
KB_TEST_SUBDIR="_m0check"
KB_TEST_FILE="m0-check.md"
# Уникальная фраза тестового документа: по ней ищем его в базе знаний.
KB_PHRASE="Кодовая фраза проверки M0: лазурный кенгуру делает выпады на рассвете"
LLM_PROMPT="Ответь одним словом: работает"
MAKE=(make --no-print-directory -s)

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

ENV_OK=false
STACK_OK=false
AGENT_CREATED=false
KB_DIR_CREATED=false
KB_ROOT_CREATED=false
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
header() { printf '\n%s==> %s%s\n' "$C_BOLD" "${STEP_TITLE[$CURRENT]}" "$C_OFF"; }

# result <step> PASS|FAIL|SKIP <note>; шаг передаётся для читаемости, записывается текущий (CURRENT).
result() {
	STEP_STATUS[CURRENT]=$2
	STEP_NOTE[CURRENT]=$3
	local color=$C_PASS
	[[ $2 == FAIL ]] && color=$C_FAIL
	[[ $2 == SKIP ]] && color=$C_SKIP
	printf '    %s%s%s %s\n' "$color" "$2" "$C_OFF" "$3"
}

# env_get <NAME>: значение переменной из .env (как его читают make и compose); в окружение скрипта не экспортируется.
env_get() {
	[[ -f "$ROOT/.env" ]] || return 0
	# shellcheck disable=SC1091 # .env создаёт пользователь: cp .env.dist .env
	(set -a && . "$ROOT/.env" >/dev/null 2>&1 && printf '%s' "${!1-}") || true
}

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

cleanup() {
	local status=$?
	if [[ $AGENT_CREATED == true ]]; then
		rm -rf "$AGENT_DIR"
	fi
	if [[ -n "$KB_SOURCE" ]]; then
		"${MAKE[@]}" kb-delete SOURCE="$KB_SOURCE" >>"$RESULTS_DIR/kb.log" 2>&1 || true
	fi
	if [[ $KB_DIR_CREATED == true ]]; then
		rm -rf "${KB_ROOT_DIR:?}/$KB_TEST_SUBDIR"
	fi
	if [[ $KB_ROOT_CREATED == true ]]; then
		rmdir "$KB_ROOT_DIR" 2>/dev/null || true
	fi
	if [[ $SUMMARY_DONE != true ]]; then
		printf '\n%sm0-check прерван (код %d): сводка не записана%s\n' "$C_FAIL" "$status" "$C_OFF" >&2
	fi
}
trap cleanup EXIT

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
	local problems=()

	for tool in docker make curl git; do
		if command -v "$tool" >/dev/null 2>&1; then
			info "$tool: есть"
		else
			problems+=("нет $tool")
		fi
	done

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
		grep -qE "^[[:space:]]*(export[[:space:]]+)?${name}=" "$ROOT/.env" || missing+=("$name")
	done < <(sed -nE 's/^([A-Z][A-Z0-9_]*)=.*/\1/p' "$ROOT/.env.dist")
	if ((${#missing[@]} > 0)); then
		warn "в .env нет переменных из .env.dist (берутся значения по умолчанию): ${missing[*]}"
	fi

	for name in ANTHROPIC_API_KEY OPENAI_API_KEY TELEGRAM_API_ID TELEGRAM_API_HASH; do
		info "$name: $(describe_secret "$(env_get "$name")")"
	done

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
		tail -n 20 "$RESULTS_DIR/up.log" | sed 's/^/      /'
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

	LLM_SINCE=$(psql_query <<<'SELECT now()' 2>/dev/null || true)

	rm -rf "$AGENT_DIR"
	AGENT_CREATED=true
	cp -r agents/_example "$AGENT_DIR"
	sed -i.bak "s/^name: _example\$/name: ${AGENT_NAME}/" "$AGENT_DIR/agent.md"
	rm -f "$AGENT_DIR/agent.md.bak"

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
		tail -n 5 "$RESULTS_DIR/llm.log" | sed 's/^/      /'
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
		result kb FAIL "make kb-* запускает go run на хосте: нужен Go (docs/SETUP.md, «Требования»)"
		return 0
	fi

	local kb_root
	kb_root=$(env_get KB_ROOT)
	KB_ROOT_DIR=${kb_root:-./kb}
	[[ -d "$KB_ROOT_DIR" ]] || KB_ROOT_CREATED=true
	KB_DIR_CREATED=true
	mkdir -p "$KB_ROOT_DIR/$KB_TEST_SUBDIR"
	cat >"$KB_ROOT_DIR/$KB_TEST_SUBDIR/$KB_TEST_FILE" <<EOF
# Проверка M0

Временный документ скрипта scripts/m0-check.sh: он удаляется сразу после проверки.

${KB_PHRASE}. Этот абзац нужен только для того, чтобы поиск по смыслу нашёл документ первым.
EOF

	local log="$RESULTS_DIR/kb.log" out
	: >"$log"
	info "make kb-ingest KB_PATH=${KB_ROOT_DIR}/${KB_TEST_SUBDIR} KB_KIND=other (лог: m0-results/kb.log)…"
	if ! out=$("${MAKE[@]}" kb-ingest KB_PATH="$KB_ROOT_DIR/$KB_TEST_SUBDIR" KB_KIND=other 2>>"$log"); then
		printf '%s\n' "$out" >>"$log"
		tail -n 5 "$log" | sed 's/^/      /'
		result kb FAIL "kb-ingest не прошёл: m0-results/kb.log"
		return 0
	fi
	printf '%s\n' "$out" >>"$log"
	info "$out"

	# Строка ingest: «<статус> <source> (<N> chunks)»; source зависит от KB_ROOT, поэтому берём его из вывода.
	KB_SOURCE=$(grep -F "$KB_TEST_FILE (" <<<"$out" | grep -v '^failed' | sed -E 's/^[a-z]+ +(.*) \([0-9]+ chunks\)$/\1/' | head -n 1)
	if [[ -z "$KB_SOURCE" ]]; then
		result kb FAIL "в выводе kb-ingest нет ${KB_TEST_FILE}: m0-results/kb.log"
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

step_transcribe() {
	local video=${M0_VIDEO-}
	if [[ -z "$(env_get OPENAI_API_KEY)" ]]; then
		result transcribe SKIP "OPENAI_API_KEY не задан"
		return 0
	fi
	if [[ -z "$video" ]]; then
		result transcribe SKIP "нет видео: make m0-check M0_VIDEO=./clip.mp4 (ролик с русской речью, 10–60 с)"
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
		# Без Go и ffmpeg на хосте — тот же transcribe в образе media (README, «Транскрипция видео»).
		info "go/ffmpeg на хосте нет: transcribe в образе media (лог: m0-results/transcribe.log)…"
		local dir name
		dir=$(cd "$(dirname "$video")" && pwd)
		name=$(basename "$video")
		{
			"${MAKE[@]}" media-image &&
				docker run --rm --env-file .env --user "$(id -u):$(id -g)" \
					-v "$dir:/in:ro" -v "$RESULTS_DIR:/work" ai-agents-media:local \
					-in "/in/$name" -out transcript.json
		} >"$log" 2>&1 || code=$?
	else
		result transcribe FAIL "нужны go+ffmpeg на хосте или Docker (шаг 1)"
		return 0
	fi

	if [[ $code -ne 0 || ! -s "$json" ]]; then
		tail -n 5 "$log" | sed 's/^/      /'
		result transcribe FAIL "transcribe завершился с кодом $code: m0-results/transcribe.log"
		return 0
	fi

	# JSON пишет json.MarshalIndent, поэтому формат строк стабилен.
	local words
	words=$(grep -c '"word":' "$json" || true)
	info "слов с таймкодами: ${words}, результат: m0-results/transcript.json"
	if ! grep -q '"language": "ru"' "$json"; then
		result transcribe FAIL "language не ru: m0-results/transcript.json"
		return 0
	fi
	if ((words == 0)); then
		result transcribe FAIL "words пуст: m0-results/transcript.json"
		return 0
	fi
	result transcribe PASS "language=ru, слов: ${words}"
}

# --- 6. Telegram Bot API ---------------------------------------------------------------------------

step_telegram() {
	if [[ -z "$(env_get TELEGRAM_API_ID)" || -z "$(env_get TELEGRAM_API_HASH)" ]]; then
		result telegram SKIP "TELEGRAM_API_ID/TELEGRAM_API_HASH не заданы (необязательно)"
		return 0
	fi
	if [[ $ENV_OK != true ]]; then
		result telegram SKIP "окружение не готово (шаг 1)"
		return 0
	fi

	local id running
	running=$(compose --profile telegram ps -q --status running telegram-bot-api 2>/dev/null || true)
	[[ -n "$running" ]] || TELEGRAM_STARTED=true

	info "docker compose --profile telegram up -d --wait telegram-bot-api…"
	if ! compose --profile telegram up -d --wait --wait-timeout 120 telegram-bot-api >"$RESULTS_DIR/telegram.log" 2>&1; then
		compose --profile telegram logs --tail 20 telegram-bot-api >>"$RESULTS_DIR/telegram.log" 2>&1 || true
		tail -n 10 "$RESULTS_DIR/telegram.log" | sed 's/^/      /'
		stop_telegram
		result telegram FAIL "bot-api не стал healthy: m0-results/telegram.log"
		return 0
	fi

	id=$(compose --profile telegram ps -q telegram-bot-api 2>/dev/null || true)
	local health
	health=$(docker inspect --format '{{.State.Health.Status}}' "$id" 2>/dev/null || echo unknown)
	info "healthcheck: $health"
	stop_telegram
	if [[ "$health" != healthy ]]; then
		result telegram FAIL "healthcheck bot-api: $health"
		return 0
	fi
	result telegram PASS "bot-api healthy (порт 8081 принимает соединения)"
}

# Bot API, поднятый только для проверки, останавливаем; запущенный пользователем — не трогаем.
stop_telegram() {
	if [[ $TELEGRAM_STARTED == true ]]; then
		compose --profile telegram stop telegram-bot-api >>"$RESULTS_DIR/telegram.log" 2>&1 || true
		info "bot-api был запущен проверкой и остановлен; постоянно: docker compose --profile telegram up -d"
	fi
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
	if [[ -n "${rows-}" ]]; then
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

write_report() {
	local sha dirty=""
	sha=$(git rev-parse --short HEAD 2>/dev/null || echo unknown)
	[[ -z "$(git status --porcelain --untracked-files=no 2>/dev/null)" ]] || dirty=" (есть незакоммиченные изменения)"

	{
		printf '# Отчёт M0-проверки\n\n'
		printf -- '- Дата: %s\n' "$(date -u '+%Y-%m-%d %H:%M:%S UTC')"
		printf -- '- Git: %s%s, ветка %s\n' "$sha" "$dirty" "$(git rev-parse --abbrev-ref HEAD 2>/dev/null || echo unknown)"
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
				"${STEP_NOTE[i]//|/\\|}"
		done
		if ((${#WARNINGS[@]} > 0)); then
			printf '\n## Предупреждения\n\n'
			printf -- '- %s\n' "${WARNINGS[@]}"
		fi
		llm_usage_report
		if [[ $ENV_OK == true ]]; then
			printf '\n## Образы\n\n```\n'
			compose --profile telegram config --images 2>/dev/null | sort
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
	printf '\n'
	return "$failed"
}

main() {
	mkdir -p "$RESULTS_DIR"
	log "${C_BOLD}M0-проверка$C_OFF ($(git rev-parse --short HEAD 2>/dev/null || echo '?')), результаты: m0-results/"

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
