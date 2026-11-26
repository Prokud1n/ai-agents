#!/usr/bin/env bash
# make edit: Монтажёр без Telegram (README, «Проверка Монтажёра без Telegram»). Запускает /app/edit-local в образе
# приложения через docker compose, в сети запущенного стека (make up): тот же путь монтажа, что у бота (модель,
# решения, правки, media-worker), но без шлюза Telegram и без очереди задач — в группу ничего не уходит.
#
#   make edit FILE="$HOME/видео.mov" [CAPTION="подпись"] [OUT=./edits/<имя>]   # версия 1
#   make edit OUT=./edits/<имя> REVISION="сделай 40 секунд"                    # правка последней версии
#   make edit OUT=./edits/<имя>                                                # докачать недокачанную версию
#
# Переменные приходят из окружения (Makefile экспортирует их без раскрытия `$`). Видео монтируется в контейнер
# только для чтения (один файл), OUT — для записи; контейнер работает с uid:gid пользователя (если это не root),
# поэтому файлы в OUT принадлежат ему. `~` в начале FILE и OUT раскрывается в $HOME (zsh на macOS сам этого не делает
# в `make FILE=~/…`). OUT по умолчанию — ./edits/<имя файла без расширения>. Внутри проекта OUT — только в ./edits/,
# вне проекта — любая отдельная папка, кроме / и папок, в которых лежат проект или домашняя папка.
# Ctrl+C останавливает монтаж: задание снимается с очереди воркера.
#
#   scripts/edit.sh check-out <путь>   # только проверяет OUT и печатает абсолютный путь (проверки в CI)
set -euo pipefail

APP_IMAGE=ai-agents-app:local
MEDIA_IMAGE=ai-agents-media:local
CONTAINER_OUT=/out
CONTAINER_IN=/in

CALLER_DIR=$PWD
cd "$(dirname "${BASH_SOURCE[0]}")/.."
ROOT=$(pwd -P)

# shellcheck source=scripts/image-age.sh
. "$ROOT/scripts/image-age.sh"

usage() {
	cat >&2 <<'EOF'
usage:
  make edit FILE="$HOME/видео.mov" [CAPTION="подпись"] [OUT=./edits/<имя>]   # новый монтаж: версия 1 в OUT/v1
  make edit OUT=./edits/<имя> REVISION="сделай 40 секунд"                    # правка последней версии в OUT
  make edit OUT=./edits/<имя>                                                # докачать версию, которую не скачал прерванный запуск
Нужен запущенный стек (make up) и ANTHROPIC_API_KEY, OPENAI_API_KEY в .env. Ctrl+C останавливает монтаж.
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

# expand_home <путь>: `~` и `~/…` → $HOME.
expand_home() {
	case $1 in
	\~) printf '%s' "$HOME" ;;
	\~/*) printf '%s/%s' "$HOME" "${1#\~/}" ;;
	*) printf '%s' "$1" ;;
	esac
}

absolute() {
	local path
	path=$(expand_home "$1")
	if [[ $path == /* ]]; then
		normalize "$path"
	else
		normalize "$CALLER_DIR/$path"
	fi
}

# physical <абсолютный путь>: путь без символических ссылок; несуществующий хвост дописывается к реальному пути
# ближайшей существующей папки (как realpath -m, которого нет в macOS).
physical() {
	local path=$1 tail=""
	while [[ ! -d $path ]]; do
		tail="/${path##*/}$tail"
		path=${path%/*}
		path=${path:-/}
	done
	path=$(cd -P "$path" && pwd -P)
	normalize "$path$tail"
}

# check_path <имя> <путь>: путь можно передать в docker -v (без перевода строки и двоеточия).
check_path() {
	[[ $2 != *$'\n'* ]] || die "$1: в пути есть перевод строки — переименуйте файл или папку"
	[[ $2 != *:* ]] || die "$1: в пути есть двоеточие, docker такой путь не смонтирует — переименуйте файл или папку"
}

# fold <путь>: на macOS файловая система по умолчанию не различает регистр, поэтому пути сравниваются в нижнем.
fold() {
	if [[ $(uname -s) == Darwin ]]; then
		printf '%s' "$1" | tr '[:upper:]' '[:lower:]'
	else
		printf '%s' "$1"
	fi
}

# is_ancestor <a> <b>: a — сама b или папка, в которой лежит b (с учётом регистра на macOS).
is_ancestor() {
	local a b
	a=$(fold "$1")
	b=$(fold "$2")
	[[ $a == "$b" || $a == / || $b == "$a"/* ]]
}

# out_dir <OUT>: реальный путь папки результатов. Нельзя: / и папки, в которых лежат проект или домашняя папка;
# внутри проекта — всё, кроме подпапок ./edits/.
out_dir() {
	local out home
	check_path OUT "$1"
	out=$(physical "$(absolute "$1")")
	home=$(physical "$(normalize "${HOME:-/}")")
	if is_ancestor "$out" "$ROOT" || is_ancestor "$out" "$home"; then
		die "OUT=$1 — слишком широкая папка (в ней проект или домашняя папка): укажите отдельную, например OUT=./edits/<имя>"
	fi
	if is_ancestor "$ROOT" "$out" && ! is_ancestor "$ROOT/edits" "$out"; then
		die "OUT=$1 — внутри проекта результаты кладутся только в ./edits/<имя> (папки с кодом не трогаем)"
	fi
	if [[ $(fold "$out") == "$(fold "$ROOT/edits")" ]]; then
		die "OUT=$1 — внутри проекта результаты кладутся только в ./edits/<имя> (папки с кодом не трогаем)"
	fi
	[[ ! -e $out || -d $out ]] || die "OUT=$1 — не папка"
	printf '%s' "$out"
}

# check_image <образ> <пути...>: образ собран и не старше кода, из которого собирается.
check_image() {
	local image=$1 created
	created=$(docker image inspect --format '{{.Created}}' "$image" 2>/dev/null) || die "образ ${image} не собран: make up"
	if image_older_than_code "$created" "${@:2}"; then
		die "образ ${image} старше кода (менялись ${*:2} после сборки): выполните make up — он пересоберёт app и media-worker"
	fi
}

check_stack() {
	local service
	check_image "$APP_IMAGE" cmd internal go.mod go.sum Dockerfile agents skills
	check_image "$MEDIA_IMAGE" cmd internal go.mod go.sum Dockerfile assets
	for service in postgres redis s3 media-worker; do
		[[ -n $(docker compose ps -q --status running "$service" 2>/dev/null) ]] ||
			die "стек не поднят ($service не запущен): make up"
	done
}

# check_lock <OUT>: .lock прерванного запуска снимается. В контейнере PID всегда 1, а hostname — ID контейнера, поэтому
# живость прошлого запуска проверяется по его контейнеру: нет такого контейнера — запуск прерван (убит), lock
# удаляется; контейнер работает — в этой папке идёт другой монтаж.
check_lock() {
	local lock=$1/.lock host
	[[ -f $lock ]] || return 0
	host=$(sed -n 2p "$lock")
	# Не hex — lock не от контейнера (запуск на хосте): решает сам edit-local по PID и возрасту.
	[[ $host =~ ^[0-9a-f]{12,64}$ ]] || return 0
	if [[ -n $(docker ps -q --no-trunc --filter "id=$host") ]]; then
		die "в OUT уже идёт другой запуск make edit (контейнер $host): дождитесь его или остановите (docker stop $host)"
	fi
	printf 'edit: предыдущий запуск прерван, снимаю блокировку %s\n' "$lock" >&2
	rm -f "$lock"
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

[[ -n $FILE || -n $REVISION || -n $OUT ]] || usage
[[ -z $FILE || -z $REVISION ]] ||
	die "FILE и REVISION вместе нельзя: FILE — новый монтаж, REVISION — правка уже сделанного в OUT"
[[ -n $FILE || -z $CAPTION ]] || die "CAPTION — подпись нового исходника (с FILE); для правки нужен только REVISION"

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
		OUT="$ROOT/edits/${stem:-video}"
	fi
	args+=(-v "$file:$CONTAINER_IN/$name:ro")
	cli+=(-in "$CONTAINER_IN/$name")
	if [[ -n $CAPTION ]]; then
		cli+=(-caption "$CAPTION")
	fi
elif [[ -n $REVISION ]]; then
	[[ -n $OUT ]] || die "для правки укажите OUT — папку прошлого монтажа, например OUT=./edits/<имя>"
	cli+=(-revision "$REVISION")
fi

out=$(out_dir "$OUT")
if [[ -z $FILE ]]; then
	[[ -f $out/state.json ]] || die "в $OUT нет state.json: сначала make edit FILE=<видео> OUT=$OUT"
fi

check_stack
mkdir -p "$out"
check_lock "$out"
args+=(-v "$out:$CONTAINER_OUT")

uid=$(id -u)
# Образ и так nonroot; uid:gid хоста — чтобы файлы в OUT принадлежали пользователю. От root хоста контейнер root
# не получает: остаётся nonroot из образа.
if [[ $uid != 0 ]]; then
	args+=(--user "$uid:$(id -g)")
fi

printf 'edit: OUT = %s\n' "$out" >&2
exec docker compose "${args[@]}" app "${cli[@]}"
