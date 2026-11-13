#!/usr/bin/env bash
# PostToolUse: форматирует отредактированный .go-файл.
set -euo pipefail

cd "${CLAUDE_PROJECT_DIR:-.}"

file_path=$(python3 -c 'import json, sys; print(json.load(sys.stdin).get("tool_input", {}).get("file_path", ""))')

if [[ "$file_path" != *.go ]] || [[ ! -f "$file_path" ]] || [[ ! -f go.mod ]]; then
	exit 0
fi

if ! command -v golangci-lint >/dev/null 2>&1; then
	echo "golangci-lint не установлен — форматирование пропущено (brew install golangci-lint)" >&2
	exit 0
fi

golangci-lint fmt "$file_path" >&2 || true
