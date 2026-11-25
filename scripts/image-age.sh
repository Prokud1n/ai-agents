#!/usr/bin/env bash
# Общие функции скриптов make kb-* и make edit (подключаются через `.`, сами ничего не запускают).

# image_older_than_code <Created образа> [пути...]: код 0, если файлы, из которых собирается образ (по умолчанию cmd,
# internal, go.mod, go.sum, Dockerfile; пути — от корня проекта), изменены после сборки образа.
# Сравниваются mtime файлов (git pull, checkout и правки их обновляют), а не дата коммита: коммит, сделанный раньше
# сборки, но подтянутый после неё, иначе не заметить. Created — время UTC вида 2026-11-20T08:50:31.123Z; оно
# переводится в метку touch -t, которую GNU и BSD touch понимают одинаково (TZ=UTC), а find -newer сравнивает
# с ней. Если формат другой или что-то не удалось, предупреждения нет.
image_older_than_code() {
	local created=$1 stamp ref newer
	if [[ $# -lt 2 ]]; then
		set -- "$created" cmd internal go.mod go.sum Dockerfile
	fi
	[[ $created =~ ^([0-9]{4})-([0-9]{2})-([0-9]{2})T([0-9]{2}):([0-9]{2}):([0-9]{2})(\.[0-9]+)?Z$ ]] || return 1
	stamp="${BASH_REMATCH[1]}${BASH_REMATCH[2]}${BASH_REMATCH[3]}${BASH_REMATCH[4]}${BASH_REMATCH[5]}.${BASH_REMATCH[6]}"
	ref=$(mktemp) || return 1
	if ! TZ=UTC touch -t "$stamp" "$ref" 2>/dev/null; then
		rm -f "$ref"
		return 1
	fi
	newer=$(find "${@:2}" -type f -newer "$ref" 2>/dev/null | head -n 1 || true)
	rm -f "$ref"
	[[ -n $newer ]]
}
