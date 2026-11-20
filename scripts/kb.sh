#!/usr/bin/env bash
# CLI базы знаний `kb`. По умолчанию запускается в образе приложения через docker compose: Go на хосте не нужен.
# Цели make kb-* вызывают этот скрипт (README, «База знаний бренда»):
#
#   scripts/kb.sh ingest <файл или папка в ./kb> [-kind brand] [-force]   # путь — первым аргументом
#   scripts/kb.sh search [-limit 5] [-kind brand,post] -- "<запрос>"
#   scripts/kb.sh delete -- <source>
#   scripts/kb.sh reembed
#   scripts/kb.sh container-path <путь>   # только печатает путь внутри контейнера (проверки в CI)
#
# В контейнере папка ./kb проекта смонтирована read-only в /kb, KB_ROOT=/kb, база — сервис postgres из compose.
# Путь для ingest пишется как на хосте (./kb/brand, kb/brand или абсолютный) и переводится в /kb/brand;
# путь вне ./kb — ошибка. Перед запуском проверяется, что образ собран и postgres запущен (иначе — ошибка
# с подсказкой make up); если код менялся после сборки образа, печатается предупреждение. Контейнер работает
# с uid:gid пользователя хоста (если это не root), поэтому читает и файлы с правами 600.
#
# KB_HOST=1 — запуск на хосте через go run (для разработчиков): KB_ROOT из .env, Postgres на 127.0.0.1.
set -euo pipefail

CONTAINER_ROOT=/kb
# Образ приложения из docker-compose.yml (x-app-image); его собирает make up.
APP_IMAGE=ai-agents-app:local

# Каталог, из которого вызвали скрипт: относительные пути считаются от него.
CALLER_DIR=$PWD
cd "$(dirname "${BASH_SOURCE[0]}")/.."
ROOT=$PWD
ROOT_PHYSICAL=$(pwd -P)
HOST_ROOT="$ROOT/kb"

die() {
	printf 'kb: %s\n' "$*" >&2
	exit 2
}

# normalize <абсолютный путь>: убирает «.», «..» и повторные «/» без обращения к диску (как filepath.Clean).
normalize() {
	local part parts=() out=""
	IFS=/ read -r -a parts <<<"$1"
	for part in ${parts[@]+"${parts[@]}"}; do
		case $part in
		"" | .) ;;
		..) out=${out%/*} ;;
		*) out+="/$part" ;;
		esac
	done
	printf '%s' "${out:-/}"
}

# under <путь> <корень>: путь внутри корня относительно него («.» для самого корня), код 1 — снаружи.
under() {
	local path=$1 root
	root=$(normalize "$2")
	if [[ $path == "$root" ]]; then
		printf '.'
	elif [[ $path == "$root"/* ]]; then
		printf '%s' "${path#"$root"/}"
	else
		return 1
	fi
}

# container_path <путь на хосте>: путь внутри контейнера для пути внутри ./kb, иначе ошибка.
container_path() {
	local path=$1 abs rel
	[[ -n $path ]] || die "пустой путь: укажи файл или папку внутри ./kb, например KB_PATH=./kb/brand"
	[[ $path != *$'\n'* ]] || die "в пути есть перевод строки: переименуй файл или папку"
	[[ $path != -* ]] || die "$path — не путь, а флаг: первым аргументом ingest идёт файл или папка в ./kb, флаги (-kind, -force) — после него"
	if [[ $path == /* ]]; then
		abs=$path
	else
		abs="$CALLER_DIR/$path"
	fi
	abs=$(normalize "$abs")

	if ! rel=$(under "$abs" "$HOST_ROOT") && ! rel=$(under "$abs" "$ROOT_PHYSICAL/kb"); then
		die "$path — вне папки базы знаний ${HOST_ROOT}: в контейнер монтируется только она (KB_ROOT=${CONTAINER_ROOT}). Положи материалы в ./kb, например ./kb/brand"
	fi
	if [[ $rel == . ]]; then
		printf '%s' "$CONTAINER_ROOT"
	else
		printf '%s/%s' "$CONTAINER_ROOT" "$rel"
	fi
}

run_host() {
	# .env пишется не для bash: пустые ссылки на переменные в нём не должны ронять скрипт (set -u).
	set -a +u
	# shellcheck disable=SC1091
	if [[ -f .env ]]; then . ./.env; fi
	set +a -u
	export POSTGRES_HOST=127.0.0.1 POSTGRES_PORT="${POSTGRES_HOST_PORT:-5432}"

	# go run работает из корня проекта: относительный путь вызова делаем абсолютным.
	if [[ ${1-} == ingest && $# -ge 2 && $2 != /* ]]; then
		set -- "$1" "$CALLER_DIR/$2" "${@:3}"
	fi
	exec go run ./cmd/kb "$@"
}

run_container() {
	local args=(run --rm --no-deps -T --entrypoint /app/kb -e "KB_ROOT=$CONTAINER_ROOT")
	local uid
	uid=$(id -u)
	# Образ и так nonroot (65532); uid:gid хоста нужен, чтобы читать файлы, доступные только владельцу (600).
	# От root хоста контейнер root не получает: остаётся nonroot из образа.
	if [[ $uid != 0 ]]; then
		args+=(--user "$uid:$(id -g)")
	fi

	if [[ ${1-} == ingest && $# -ge 2 ]]; then
		local path
		path=$(container_path "$2")
		# Без папки docker создал бы её сам (на Linux — от root): лучше понятная ошибка.
		[[ -d $HOST_ROOT ]] || die "нет папки ${HOST_ROOT}: создай её и положи туда материалы (mkdir -p kb/brand)"
		args+=(-v "$HOST_ROOT:$CONTAINER_ROOT:ro")
		set -- "$1" "$path" "${@:3}"
	fi

	check_stack
	exec docker compose "${args[@]}" app "$@"
}

# check_stack: дешёвые проверки перед запуском, чтобы вместо ошибки docker или подключения к базе была подсказка.
check_stack() {
	local created
	created=$(docker image inspect --format '{{.Created}}' "$APP_IMAGE" 2>/dev/null) ||
		die "образ ${APP_IMAGE} не собран: make up"
	[[ -n $(docker compose ps -q --status running postgres 2>/dev/null) ]] ||
		die "стек не поднят (postgres не запущен): make up"
	if image_older_than_code "$created"; then
		printf 'kb: образ %s старше кода (cmd, internal, go.mod, go.sum или Dockerfile менялись после сборки), выполните make up\n' \
			"$APP_IMAGE" >&2
	fi
}

# image_older_than_code <Created образа>: код 0, если файлы, из которых собирается kb, изменены после сборки образа.
# Сравниваются mtime файлов (git pull, checkout и правки их обновляют), а не дата коммита: коммит, сделанный раньше
# сборки, но подтянутый после неё, иначе не заметить. Created — время UTC вида 2026-11-20T08:50:31.123Z; оно
# переводится в метку touch -t, которую GNU и BSD touch понимают одинаково (TZ=UTC), а find -newer сравнивает
# с ней. Если формат другой или что-то не удалось, предупреждения нет.
image_older_than_code() {
	local created=$1 stamp ref newer
	[[ $created =~ ^([0-9]{4})-([0-9]{2})-([0-9]{2})T([0-9]{2}):([0-9]{2}):([0-9]{2})(\.[0-9]+)?Z$ ]] || return 1
	stamp="${BASH_REMATCH[1]}${BASH_REMATCH[2]}${BASH_REMATCH[3]}${BASH_REMATCH[4]}${BASH_REMATCH[5]}.${BASH_REMATCH[6]}"
	ref=$(mktemp) || return 1
	if ! TZ=UTC touch -t "$stamp" "$ref" 2>/dev/null; then
		rm -f "$ref"
		return 1
	fi
	newer=$(find cmd internal go.mod go.sum Dockerfile -type f -newer "$ref" 2>/dev/null | head -n 1 || true)
	rm -f "$ref"
	[[ -n $newer ]]
}

if [[ ${1-} == container-path ]]; then
	[[ $# -eq 2 ]] || die "usage: scripts/kb.sh container-path <путь>"
	container_path "$2"
	printf '\n'
	exit 0
fi

if [[ ${KB_HOST-} == 1 ]]; then
	run_host "$@"
fi
run_container "$@"
