#!/usr/bin/env bash
# Stop: если менялись .go-файлы, гоняет линтер и не даёт завершить ответ с ошибками.
set -uo pipefail

cd "${CLAUDE_PROJECT_DIR:-.}"

stop_hook_active=$(python3 -c 'import json, sys; print(str(json.load(sys.stdin).get("stop_hook_active", False)).lower())')

# Защита от бесконечного цикла: повторно не блокируем, остальное поймают pre-commit и CI.
if [[ "$stop_hook_active" == "true" ]] || [[ ! -f go.mod ]]; then
	exit 0
fi

if [[ -z "$(git status --porcelain -- '*.go')" ]]; then
	exit 0
fi

if ! output=$(./scripts/golangci-lint.sh run ./... 2>&1); then
	echo "golangci-lint нашёл нарушения код-стайла (см. CODE_STYLE.md). Исправь их, не отключая линтер:" >&2
	echo "$output" >&2
	exit 2
fi
