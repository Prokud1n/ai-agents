#!/usr/bin/env bash
# make edit: Монтажёр без Telegram (README, «Проверка Монтажёра без Telegram»). Запускает /app/edit-local в образе
# приложения через docker compose, в сети запущенного стека (make up): тот же путь монтажа, что у бота (модель,
# решения, правки, media-worker), но без шлюза Telegram и без очереди задач — в группу ничего не уходит.
#
#   make edit FILE=/путь/к/видео.mov [CAPTION="подпись"] [OUT=./edits/<имя>]   # версия 1
#   make edit OUT=./edits/<имя> REVISION="сделай 40 секунд"                    # правка последней версии
#
# Переменные приходят из окружения (make передаёт переменные командной строки). Видео монтируется в контейнер
# только для чтения (один файл), OUT — для записи; контейнер работает с uid:gid пользователя (если это не root),
# поэтому файлы в OUT принадлежат ему. OUT по умолчанию — ./edits/<имя файла без расширения> в корне проекта;
# явно можно указать любую отдельную папку, но не /, не домашнюю и не корень проекта.
#
#   scripts/edit.sh check-out <путь>   # только проверяет OUT и печатает абсолютный путь (проверки в CI)
set -euo pipefail

APP_IMAGE=ai-agents-app:local
CONTAINER_OUT=/out
CONTAINER_IN=/in

CALLER_DIR=$PWD
cd "$(dirname "${BASH_SOURCE[0]}")/.."
ROOT=$PWD

usage() {
	cat >&2 <<'EOF'
usage:
  make edit FILE=/путь/к/видео.mov [CAPTION="подпись"] [OUT=./edits/<имя>]   # новый монтаж: версия 1 в OUT/v1
  make edit OUT=./edits/<имя> REVISION="сделай 40 секунд"                    # правка последней версии в OUT
Нужен запущенный стек (make up) и ANTHROPIC_API_KEY, OPENAI_API_KEY в .env.
EOF
	exit 2
}

die() {
	printf 'edit: %s\n' "$*" >&2
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

absolute() {
	if [[ $1 == /* ]]; then
		normalize "$1"
	else
		normalize "$CALLER_DIR/$1"
	fi
}

# check_path <имя> <путь>: путь можно передать в docker -v (без перевода строки и двоеточия).
check_path() {
	[[ $2 != *$'\n'* ]] || die "$1: в пути есть перевод строки — переименуйте файл или папку"
	[[ $2 != *:* ]] || die "$1: в пути есть двоеточие, docker такой путь не смонтирует — переименуйте файл или папку"
}

# out_dir <OUT>: абсолютный путь папки результатов; слишком широкие папки (/, домашняя, корень проекта, .git) — ошибка.
out_dir() {
	local out
	check_path OUT "$1"
	out=$(absolute "$1")
	case $out in
	/ | "$(normalize "${HOME:-/}")" | "$ROOT" | "$ROOT/.git" | "$ROOT/.git/"*)
		die "OUT=$1 — слишком широкая папка: укажите отдельную, например OUT=./edits/<имя>"
		;;
	esac
	[[ ! -e $out || -d $out ]] || die "OUT=$1 — не папка"
	printf '%s' "$out"
}

check_stack() {
	local service
	docker image inspect "$APP_IMAGE" >/dev/null 2>&1 || die "образ ${APP_IMAGE} не собран: make up"
	for service in postgres redis s3 media-worker; do
		[[ -n $(docker compose ps -q --status running "$service" 2>/dev/null) ]] ||
			die "стек не поднят ($service не запущен): make up"
	done
}

if [[ ${1-} == check-out ]]; then
	[[ $# -eq 2 ]] || die "usage: scripts/edit.sh check-out <путь>"
	out_dir "$2"
	printf '\n'
	exit 0
fi

FILE=${FILE-}
OUT=${OUT-}
CAPTION=${CAPTION-}
REVISION=${REVISION-}

[[ -n $FILE || -n $REVISION ]] || usage
[[ -z $FILE || -z $REVISION ]] ||
	die "FILE и REVISION вместе нельзя: FILE — новый монтаж, REVISION — правка уже сделанного в OUT"
[[ -z $REVISION || -z $CAPTION ]] || die "CAPTION — подпись нового исходника; для правки нужен только REVISION"

args=(run --rm --no-deps -T --entrypoint /app/edit-local)
cli=(-out "$CONTAINER_OUT")

if [[ -n $FILE ]]; then
	check_path FILE "$FILE"
	file=$(absolute "$FILE")
	[[ -f $file ]] || die "нет файла $FILE"
	[[ -r $file ]] || die "файл $FILE не читается"
	name=$(basename "$file")
	if [[ -z $OUT ]]; then
		stem=${name%.*}
		OUT="./edits/${stem:-video}"
	fi
	args+=(-v "$file:$CONTAINER_IN/$name:ro")
	cli+=(-in "$CONTAINER_IN/$name")
	if [[ -n $CAPTION ]]; then
		cli+=(-caption "$CAPTION")
	fi
else
	[[ -n $OUT ]] || die "для правки укажите OUT — папку прошлого монтажа, например OUT=./edits/<имя>"
	cli+=(-revision "$REVISION")
fi

out=$(out_dir "$OUT")
if [[ -n $REVISION ]]; then
	[[ -f $out/state.json ]] || die "в $OUT нет state.json: сначала make edit FILE=<видео> OUT=$OUT"
fi

check_stack
mkdir -p "$out"
args+=(-v "$out:$CONTAINER_OUT")

uid=$(id -u)
# Образ и так nonroot; uid:gid хоста — чтобы файлы в OUT принадлежали пользователю. От root хоста контейнер root
# не получает: остаётся nonroot из образа.
if [[ $uid != 0 ]]; then
	args+=(--user "$uid:$(id -g)")
fi

printf 'edit: OUT = %s\n' "$out" >&2
exec docker compose "${args[@]}" app "${cli[@]}"
